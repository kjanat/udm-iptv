package diagnostics

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"
)

const (
	normalSampleInterval  = 15 * time.Second
	summarySampleInterval = 2 * time.Minute
	debugSampleInterval   = 5 * time.Second
	finalizeReserve       = 2 * time.Second
	// finalizeReserveDivisor caps the reserve at half a short capture, so a
	// 1s capture still gets a final snapshot instead of the full reserve.
	finalizeReserveDivisor = 2
	// journalLineLimit bounds how many trailing journal lines a capture attaches.
	journalLineLimit = 10_000
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
// log entry, or status message.
type Event struct {
	Time     time.Time `json:"time"`
	Type     string    `json:"type"`
	Message  string    `json:"message,omitempty"`
	Snapshot *Snapshot `json:"snapshot,omitempty"`
	Log      string    `json:"log,omitempty"`
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
	if err := write(Event{Time: started, Type: "started", Message: "Capture started; expected completion " + ends.Format(time.RFC3339)}); err != nil {
		return err
	}
	cursor := journalCursor(ctx)
	initial, err := application.snapshotWithin(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return write(Event{Time: time.Now().UTC(), Type: "timeout", Message: "Capture deadline reached during the initial snapshot."})
		}

		return err
	}
	if err := write(Event{Time: initial.Timestamp, Type: "initial", Snapshot: &initial}); err != nil {
		return err
	}
	if err := application.sampleSnapshots(ctx, options, endsAt, write); err != nil {
		return err
	}
	if stoppedBySignal(signalContext, ctx) {
		return write(Event{Time: time.Now().UTC(), Type: "completed", Message: "Capture stopped by signal."})
	}

	return application.finalizeCapture(ctx, cursor, write)
}

func stoppedBySignal(signalContext, ctx context.Context) bool {
	return signalContext.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded)
}

// expired reports whether the capture window already closed.
func expired(ctx context.Context) bool {
	return ctx.Err() != nil
}

// finalizeCapture records the closing snapshot and the journal within
// whatever remains of the capture window.
func (application *Collector) finalizeCapture(ctx context.Context, cursor string, write func(Event) error) error {
	if final, finalErr := application.snapshotWithin(ctx); finalErr == nil {
		if err := write(Event{Time: final.Timestamp, Type: "final", Snapshot: &final}); err != nil {
			return err
		}
	}
	if err := writeJournal(ctx, cursor, write); err != nil {
		return err
	}
	if expired(ctx) {
		return write(Event{Time: time.Now().UTC(), Type: "timeout", Message: "Capture deadline reached; a collector may have stalled."})
	}

	return write(Event{Time: time.Now().UTC(), Type: "completed", Message: "Capture finished within its deadline."})
}

type captureOutput struct {
	writer diagnosticWriter
	files  []*os.File
}

func openCaptureOutput(options Options) (*captureOutput, error) {
	output := &captureOutput{}
	for _, target := range []struct {
		path string
		kind string
		into *syncingWriter
	}{
		{options.JSONPath, "JSON", &output.writer.json},
		{options.TextPath, "text", &output.writer.text},
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
// finalized, reserving time for the final snapshot and the journal.
func (application *Collector) sampleSnapshots(ctx context.Context, options Options, endsAt time.Time, write func(Event) error) error {
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
		case <-ticker.C:
			finished, err := application.writeSample(ctx, write)
			if err != nil || finished {
				return err
			}
		}
	}
}

// writeSample reports whether the capture deadline ended the sample.
func (application *Collector) writeSample(ctx context.Context, write func(Event) error) (bool, error) {
	current, err := application.snapshotWithin(ctx)
	if err == nil {
		return false, write(Event{Time: current.Timestamp, Type: "sample", Snapshot: &current})
	}
	if expired(ctx) {
		return true, nil
	}

	return false, write(Event{Time: time.Now().UTC(), Type: "error", Message: sanitize(err.Error())})
}

func writeJournal(ctx context.Context, cursor string, write func(Event) error) error {
	for _, line := range journalLines(ctx, cursor, journalLineLimit) {
		if expired(ctx) {
			break
		}
		if err := write(Event{Time: time.Now().UTC(), Type: "log", Log: sanitize(line)}); err != nil {
			return err
		}
	}

	return nil
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
