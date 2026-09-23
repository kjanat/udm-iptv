package diagnostics

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

const (
	journalQueueSize     = 128
	journalRecordLimit   = 64 << 10
	journalInitialBuffer = 4096
)

var errJournalLimit = errors.New("live journal limit reached; additional log records omitted")

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
		if readErr != nil && ctx.Err() == nil {
			sendJournal(ctx, events, journalError("Live journal stopped", readErr))
			cancel()
		}
		if err := command.Wait(); err != nil && ctx.Err() == nil {
			sendJournal(ctx, events, journalError("Live journal exited", err))
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
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, journalInitialBuffer), journalRecordLimit)
	for scanner.Scan() {
		event, relevant := journalEvent(scanner.Bytes())
		if !relevant {
			continue
		}
		if limits.records <= 0 || len(scanner.Bytes()) > limits.bytes {
			return errJournalLimit
		}
		limits.records--
		limits.bytes -= len(scanner.Bytes())
		if !sendJournal(ctx, events, event) {
			return nil
		}
	}
	if ctx.Err() != nil {
		return nil
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read journal record: %w", err)
	}
	return nil
}

func journalEvent(line []byte) (Event, bool) {
	var record journalRecord
	if json.Unmarshal(line, &record) != nil {
		return Event{}, false
	}
	entry := record.entry()
	if record.text("_SYSTEMD_UNIT") == udapiUnit && !strings.Contains(entry.Message, "udm-iptv") {
		return Event{}, false
	}
	return Event{Time: entry.Time, Type: EventLog, Source: entry.Source, Log: entry.Message}, true
}

func drainJournal(ctx context.Context, events <-chan Event, write func(Event) error) error {
	for {
		select {
		case <-ctx.Done():
			return nil
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
