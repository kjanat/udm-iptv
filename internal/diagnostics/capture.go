package diagnostics

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/kjanat/udm-iptv/internal/filemode"
)

const (
	normalSampleInterval  = 15 * time.Second
	summarySampleInterval = 2 * time.Minute
	debugSampleInterval   = 5 * time.Second
	finalizeReserve       = 2 * time.Second
	// finalizeReserveDivisor caps the reserve at half a short capture, so a
	// 1s capture still gets a final snapshot instead of the full reserve.
	finalizeReserveDivisor = 2
	// journalLineLimit bounds how many live journal records a capture retains.
	journalLineLimit = 10_000
)

// Event types written to a capture, in the order they can appear.
const (
	EventStarted   = "started"
	EventInitial   = "initial"
	EventSample    = "sample"
	EventMarker    = "marker"
	EventLog       = "log"
	EventError     = "error"
	EventFailed    = "failed"
	EventFinal     = "final"
	EventCompleted = "completed"
	EventTimeout   = "timeout"
)

// Options defines the configuration options for a diagnostic capture.
type Options struct {
	Capture    time.Duration
	Format     string
	Verbosity  string
	Follow     bool
	TextPath   string
	JSONPath   string
	FollowFile string
}

// Event represents a single diagnostic event, which can be a snapshot,
// log entry, or status message. A log event's Time is when the record was
// logged, and Source names the process that logged it.
type Event struct {
	Privacy string `json:"privacy,omitempty"`
	// AddressOrder lists IPv4 aliases in original numerical order for querier election analysis.
	AddressOrder []string                   `json:"ipv4Order,omitempty"`
	Time         time.Time                  `json:"time"`
	Deadline     time.Time                  `json:"deadline,omitzero"`
	Type         string                     `json:"type"`
	Message      string                     `json:"message,omitempty"`
	Snapshot     *Snapshot                  `json:"snapshot,omitempty"`
	Source       string                     `json:"source,omitempty"`
	Log          string                     `json:"log,omitempty"`
	Journal      map[string]json.RawMessage `json:"journal,omitempty"`
}

// Capture performs a diagnostic capture based on the provided options.
// It collects snapshots, logs, and status messages within the specified duration.
func (application *Collector) Capture(ctx context.Context, options Options) (resultErr error) {
	signalContext, stop := signalContext(ctx)
	defer stop()
	startedAt := time.Now()
	endsAt := startedAt.Add(options.Capture)
	ctx, cancel := context.WithDeadline(signalContext, endsAt)
	defer cancel()
	output, err := openCaptureOutput(options)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, output.close()) }()
	write := output.writer.write
	started := startedAt.UTC()
	ends := endsAt.UTC()
	policy := fmt.Sprintf("\nLive journal: %s and messages containing udm-iptv from %s; limits %d records, %d input bytes, %d bytes per record. Limit/read failures are recorded.", serviceUnit, udapiUnit, journalLineLimit, journalOutputLimit, journalRecordLimit)
	if err := write(Event{Time: started, Deadline: ends, Type: EventStarted, Message: "Capture started; expected completion " + ends.Format(time.RFC3339) + policy}); err != nil {
		return err
	}
	markers := &markerReader{path: MarkerPath(options)}
	logs, stopLogs := followJournal(ctx)
	evidence := captureEvidence{markers: markers, logs: logs, stopLogs: stopLogs}
	defer func() { resultErr = errors.Join(resultErr, evidence.finish(ctx, write)) }()
	initial, err := application.snapshotWithin(ctx)
	if err != nil {
		if ctx.Err() != nil {
			cleanupErr := evidence.finish(ctx, write)
			return errors.Join(cleanupErr, write(Event{Time: time.Now().UTC(), Type: EventTimeout, Message: "Capture deadline reached during the initial snapshot: " + err.Error()}))
		}

		return errors.Join(err, write(Event{Time: time.Now().UTC(), Type: EventError, Message: "Initial snapshot unavailable: " + err.Error()}))
	}
	if err := write(Event{Time: initial.Timestamp, Type: EventInitial, Snapshot: &initial}); err != nil {
		return err
	}
	if err := application.sampleSnapshots(ctx, options, endsAt, markers, logs, write); err != nil {
		return err
	}
	if err := evidence.finish(ctx, write); err != nil {
		return err
	}
	if stoppedBySignal(signalContext, ctx) {
		return write(Event{Time: time.Now().UTC(), Type: EventCompleted, Message: "Capture stopped by signal."})
	}

	return application.finalizeCapture(ctx, write)
}

// finish is registered before collecting snapshots, so every return preserves
// available evidence before closing its writers. It never extends ctx's deadline.
type captureEvidence struct {
	markers  *markerReader
	logs     <-chan Event
	stopLogs context.CancelFunc
	finished bool
}

func (evidence *captureEvidence) finish(ctx context.Context, write func(Event) error) error {
	if evidence.finished {
		return nil
	}
	evidence.finished = true
	evidence.stopLogs()
	journalErr := drainJournal(ctx, evidence.logs, write)
	markerErr := evidence.markers.finish(write)
	return errors.Join(journalErr, markerErr)
}

// MarkerPath is the file a viewer appends manual markers to. Both capture
// files share a base name, so the viewer derives the same path from either.
func MarkerPath(options Options) string {
	path := options.JSONPath
	if path == "" {
		path = options.TextPath
	}

	return MarkerPathFor(path)
}

// MarkerPathFor returns the marker file beside a capture file.
func MarkerPathFor(capturePath string) string {
	return strings.TrimSuffix(capturePath, filepath.Ext(capturePath)) + ".markers"
}

// WriteMarker appends a manual marker for the capture at capturePath.
func WriteMarker(capturePath, text string, at time.Time) error {
	file, err := os.OpenFile(MarkerPathFor(capturePath), os.O_WRONLY|os.O_APPEND|os.O_CREATE, filemode.PrivateFile)
	if err != nil {
		return fmt.Errorf("open marker file: %w", err)
	}
	defer closeIgnoringError(file)
	text = strings.ReplaceAll(strings.TrimSpace(text), "\n", " ")
	if _, err := fmt.Fprintf(file, "%s\t%s\n", at.UTC().Format(time.RFC3339Nano), text); err != nil {
		return fmt.Errorf("write marker: %w", err)
	}

	return nil
}

// markerReader folds complete lines of the marker file into marker events,
// remembering how far it read.
type markerReader struct {
	path   string
	offset int64
}

func (reader *markerReader) drain(write func(Event) error) error {
	return reader.read(false, write)
}

func (reader *markerReader) finish(write func(Event) error) error {
	return reader.read(true, write)
}

var errMarkerFileType = errors.New("marker path is not a regular file")

func (reader *markerReader) read(final bool, write func(Event) error) error {
	file, err := os.OpenFile(reader.path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return reader.failed("open", err, "", write)
	}
	defer closeIgnoringError(file)
	info, err := file.Stat()
	if err != nil {
		return reader.failed("stat", err, "", write)
	}
	if !info.Mode().IsRegular() {
		return reader.failed("inspect", errMarkerFileType, "", write)
	}
	if _, err := file.Seek(reader.offset, io.SeekStart); err != nil {
		return reader.failed("seek", err, "", write)
	}
	// Freeze the readable extent: concurrent marker appends cannot keep cleanup
	// reading indefinitely, and an unfinished line never waits for a newline.
	return reader.readRecords(io.LimitReader(file, max(0, info.Size()-reader.offset)), final, write)
}

func (reader *markerReader) readRecords(input io.Reader, final bool, write func(Event) error) error {
	buffered := bufio.NewReader(input)
	for {
		line, err := buffered.ReadString('\n')
		if err != nil {
			return reader.readEnd(line, err, final, write)
		}
		reader.offset += int64(len(line))
		if err := write(markerEvent(strings.TrimSuffix(line, "\n"))); err != nil {
			return err
		}
	}
}

func (reader *markerReader) readEnd(line string, err error, final bool, write func(Event) error) error {
	if !errors.Is(err, io.EOF) {
		reader.offset += int64(len(line))
		return reader.failed("read", err, line, write)
	}
	if final && line != "" {
		reader.offset += int64(len(line))
		return write(Event{Time: time.Now().UTC(), Type: EventError, Message: fmt.Sprintf("Incomplete final marker in %s; raw=%q", reader.path, line)})
	}
	return nil
}

func (reader *markerReader) failed(operation string, err error, partial string, write func(Event) error) error {
	cause := fmt.Errorf("%s marker file %s at offset %d: %w", operation, reader.path, reader.offset, err)
	message := cause.Error()
	if partial != "" {
		message += fmt.Sprintf("; partial raw=%q", partial)
	}
	return errors.Join(cause, write(Event{Time: time.Now().UTC(), Type: EventError, Message: message}))
}

func markerEvent(line string) Event {
	stamp, text, ok := strings.Cut(line, "\t")
	if ok {
		if when, err := time.Parse(time.RFC3339Nano, stamp); err == nil {
			return Event{Time: when.UTC(), Type: EventMarker, Message: text}
		}
	}

	return Event{Time: time.Now().UTC(), Type: EventMarker, Message: line}
}

func stoppedBySignal(signalContext, ctx context.Context) bool {
	return signalContext.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded)
}

// expired reports whether the capture window already closed.
func expired(ctx context.Context) bool {
	return ctx.Err() != nil
}

// finalizeCapture records the closing snapshot within
// whatever remains of the capture window.
func (application *Collector) finalizeCapture(ctx context.Context, write func(Event) error) error {
	if final, finalErr := application.snapshotWithin(ctx); finalErr == nil {
		if err := write(Event{Time: final.Timestamp, Type: EventFinal, Snapshot: &final}); err != nil {
			return err
		}
	} else if err := write(Event{Time: time.Now().UTC(), Type: EventError, Message: "Final snapshot unavailable: " + finalErr.Error()}); err != nil {
		return err
	}
	if expired(ctx) {
		return write(Event{Time: time.Now().UTC(), Type: EventTimeout, Message: "Capture deadline reached; a collector may have stalled."})
	}

	return write(Event{Time: time.Now().UTC(), Type: EventCompleted, Message: "Capture finished within its deadline."})
}

type captureOutput struct {
	writer diagnosticWriter
	files  []*os.File
}

func openCaptureOutput(options Options) (*captureOutput, error) {
	output := &captureOutput{writer: diagnosticWriter{statusPath: StatusPath(options)}}
	for _, target := range []struct {
		path string
		kind string
		into *syncingWriter
	}{
		{options.JSONPath, "JSON", &output.writer.json},
		{options.TextPath, exportTextFormat, &output.writer.text},
	} {
		if target.path == "" {
			continue
		}
		file, err := openReport(target.path)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("open %s capture output: %w", target.kind, err), output.closeFiles())
		}
		*target.into = file
		output.files = append(output.files, file)
	}

	return output, nil
}

func (output *captureOutput) close() error {
	return errors.Join(output.writer.flush(), output.closeFiles())
}

func (output *captureOutput) closeFiles() error {
	var result error
	for _, file := range slices.Backward(output.files) {
		result = errors.Join(result, file.Close())
	}

	return result
}

func sampleInterval(verbosity string) time.Duration {
	switch verbosity {
	case "summary":
		return summarySampleInterval
	case "debug":
		return debugSampleInterval
	}

	return normalSampleInterval
}

// sampleSnapshots records periodic snapshots until the capture must be
// finalized, reserving time to close the journal and take the final snapshot.
func (application *Collector) sampleSnapshots(ctx context.Context, options Options, endsAt time.Time, markers *markerReader, logs <-chan Event, write func(Event) error) error {
	ticker := time.NewTicker(sampleInterval(options.Verbosity))
	defer ticker.Stop()
	reserve := min(finalizeReserve, options.Capture/finalizeReserveDivisor)
	finalize := time.NewTimer(time.Until(endsAt.Add(-reserve)))
	defer finalize.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-finalize.C:
			return nil
		case event, open := <-logs:
			if !open {
				logs = nil
				continue
			}
			if err := write(event); err != nil {
				return err
			}
		case <-ticker.C:
			finished, err := application.writeMarkedSample(ctx, markers, write)
			if err != nil || finished {
				return err
			}
		}
	}
}

func (application *Collector) writeMarkedSample(ctx context.Context, markers *markerReader, write func(Event) error) (bool, error) {
	if err := markers.drain(write); err != nil {
		return false, err
	}
	return application.writeSample(ctx, write)
}

// writeSample reports whether the capture deadline ended the sample.
func (application *Collector) writeSample(ctx context.Context, write func(Event) error) (bool, error) {
	current, err := application.snapshotWithin(ctx)
	if err == nil {
		return false, write(Event{Time: current.Timestamp, Type: EventSample, Snapshot: &current})
	}
	if expired(ctx) {
		return true, nil
	}

	return false, write(Event{Time: time.Now().UTC(), Type: EventError, Message: err.Error()})
}

type collectedValue[T any] struct {
	value T
	err   error
}

func collectWithin[T any](ctx context.Context, collect func() (T, error)) (T, error) {
	result := make(chan collectedValue[T], 1)
	go func() {
		value, err := collect()
		result <- collectedValue[T]{value: value, err: err}
	}()
	select {
	case <-ctx.Done():
		var zero T

		return zero, fmt.Errorf("wait for diagnostics collection: %w", ctx.Err())
	case value := <-result:
		return value.value, value.err
	}
}

func (application *Collector) snapshotWithin(ctx context.Context) (Snapshot, error) {
	return collectWithin(ctx, func() (Snapshot, error) { return application.Snapshot(ctx) })
}

func signalContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
}
