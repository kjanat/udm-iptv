package diagnostics

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

const (
	journalQueueSize   = 128
	journalRecordLimit = 64 << 10
)

var (
	errJournalLimit    = errors.New("journal limit reached; additional log records omitted")
	errJournalDelivery = errors.New("journal record read but not delivered before cancellation")
)

type journalLimits struct {
	records int
	bytes   int
}

// followJournal owns a bounded stream; cancelling it also terminates journalctl.
// The capture goroutine alone writes files, so records cannot interleave.
func followJournal(parent context.Context) (<-chan Event, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	events := make(chan Event, journalQueueSize)
	go func() {
		defer close(events)
		defer cancel()
		command := exec.CommandContext(ctx, "journalctl", "--follow", "--since", "now", "--no-pager", "-o", "json", "-u", serviceUnit, "-u", udapiUnit)
		stderr := &boundedJournal{limit: journalRecordLimit}
		command.Stderr = stderr
		command.WaitDelay = time.Second
		pipe, err := command.StdoutPipe()
		if err == nil {
			err = command.Start()
		}
		if err != nil {
			if pipe != nil {
				_ = pipe.Close()
			}
			sendJournal(ctx, events, journalError("Live journal unavailable", err))
			return
		}
		readErr := readJournalStream(ctx, pipe, journalLimits{records: journalLineLimit, bytes: journalOutputLimit}, events)
		if readErr != nil {
			sendJournal(parent, events, journalError("Live journal stopped", readErr))
			cancel()
		} else if ctx.Err() == nil {
			sendJournal(parent, events, journalError("Live journal ended before the capture finished", io.EOF))
		}
		waitErr := command.Wait()
		if ctx.Err() != nil {
			if waitErr != nil {
				sendJournal(parent, events, Event{Time: time.Now().UTC(), Type: EventLog, Source: "journalctl", Log: "Process result during requested shutdown: " + waitErr.Error()})
			}
			waitErr = nil // journalctl is intentionally terminated with the stream.
		}
		if len(stderr.Bytes()) > 0 {
			sendJournal(parent, events, Event{Time: time.Now().UTC(), Type: EventLog, Source: "journalctl stderr", Log: stderr.String()})
		}
		if err := journalCommandError(waitErr, stderr); err != nil {
			sendJournal(parent, events, journalError("Live journal exited", err))
		}
	}()
	return events, cancel
}

func journalError(message string, err error) Event {
	return Event{Time: time.Now().UTC(), Type: EventError, Message: message + ": " + err.Error()}
}

func sendJournal(ctx context.Context, events chan<- Event, event Event) bool {
	if ctx.Err() != nil {
		return false
	}
	select {
	case events <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

// Closing the reader on cancellation also unblocks a silent pipe whose other
// writers outlive journalctl. The per-record and aggregate input-byte limits
// bound retained messages independently of the number of records.
func readJournalStream(ctx context.Context, input io.ReadCloser, limits journalLimits, events chan<- Event) error {
	stopClose := context.AfterFunc(ctx, func() { _ = input.Close() })
	defer stopClose()
	defer func() { _ = input.Close() }()
	reader := bufio.NewReaderSize(input, journalRecordLimit)
	for {
		line, readErr := reader.ReadSlice('\n')
		if errors.Is(readErr, bufio.ErrBufferFull) {
			sendJournal(ctx, events, Event{Time: time.Now().UTC(), Type: EventError, Message: fmt.Sprintf("Journal record exceeds %d bytes; retained prefix=%q", journalRecordLimit, line)})
			return fmt.Errorf("%w: oversized record and subsequent records not read", errJournalLimit)
		}
		if len(line) > 0 {
			if err := forwardJournalLine(ctx, bytes.TrimSuffix(line, []byte{'\n'}), &limits, events); err != nil {
				return err
			}
		}
		if readErr != nil {
			return journalReadError(ctx, readErr)
		}
	}
}

func journalReadError(ctx context.Context, err error) error {
	// Cancellation deliberately closes the pipe; the capture records its own
	// shutdown status and drains events already collected.
	select {
	case <-ctx.Done():
		return nil
	default:
	}
	if errors.Is(err, io.EOF) {
		return nil
	}
	return fmt.Errorf("read journal record: %w", err)
}

func forwardJournalLine(ctx context.Context, line []byte, limits *journalLimits, events chan<- Event) error {
	event, relevant := journalEvent(line)
	if !relevant {
		return nil
	}
	if limits.records <= 0 || len(line) > limits.bytes {
		return fmt.Errorf("%w: at least one additional %d-byte record was not retained; subsequent records were not read", errJournalLimit, len(line))
	}
	limits.records--
	limits.bytes -= len(line)
	if !sendJournal(ctx, events, event) {
		return fmt.Errorf("%w: raw=%q", errJournalDelivery, line)
	}
	return nil
}

func journalEvent(line []byte) (Event, bool) {
	entry := parseJournalLine(line)
	if entry.Metadata.text("_SYSTEMD_UNIT") == udapiUnit && !strings.Contains(entry.Message, "udm-iptv") {
		return Event{}, false
	}
	return entry.event(), true
}

func drainJournal(ctx context.Context, events <-chan Event, write func(Event) error) error {
	for {
		// Retain records already in the queue even when the capture has expired.
		select {
		case event, open := <-events:
			if !open {
				return nil
			}
			if err := write(event); err != nil {
				return err
			}
			continue
		default:
		}
		select {
		case <-ctx.Done():
			return write(journalError("Journal drain ended before the stream closed; in-flight records may be missing", ctx.Err()))
		case event, open := <-events:
			if !open {
				return nil
			}
			if err := write(event); err != nil {
				return err
			}
		}
	}
}
