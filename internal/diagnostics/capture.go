package diagnostics

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type Options struct {
	Capture    time.Duration
	Format     string
	Verbosity  string
	Follow     bool
	TextPath   string
	JSONPath   string
	FollowFile string
}

type Event struct {
	Time     time.Time `json:"time"`
	Type     string    `json:"type"`
	Message  string    `json:"message,omitempty"`
	Snapshot *Snapshot `json:"snapshot,omitempty"`
	Log      string    `json:"log,omitempty"`
}

func (application *Collector) Capture(ctx context.Context, options Options) (resultErr error) {
	signalContext, stop := signalContext(ctx)
	defer stop()
	startedAt := time.Now()
	endsAt := startedAt.Add(options.Capture)
	ctx, cancel := context.WithDeadline(signalContext, endsAt)
	defer cancel()
	sanitizer := newDiagnosticSanitizer(application.ConfigPath)
	addressFailures, err := sanitizer.watch(ctx)
	if err != nil {
		return fmt.Errorf("observe device addresses: %w", err)
	}
	var jsonFile *os.File
	if options.JSONPath != "" {
		jsonFile, err = openReport(options.JSONPath)
		if err != nil {
			return fmt.Errorf("open JSON capture output: %w", err)
		}
		defer func() { resultErr = errors.Join(resultErr, jsonFile.Close()) }()
	}
	var textFile *os.File
	if options.TextPath != "" {
		textFile, err = openReport(options.TextPath)
		if err != nil {
			return fmt.Errorf("open text capture output: %w", err)
		}
		defer func() { resultErr = errors.Join(resultErr, textFile.Close()) }()
	}
	writer := diagnosticWriter{}
	if jsonFile != nil {
		writer.json = jsonFile
	}
	if textFile != nil {
		writer.text = textFile
	}
	defer func() { resultErr = errors.Join(resultErr, writer.flush()) }()
	write := writer.write
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
	interval := 15 * time.Second
	switch options.Verbosity {
	case "summary":
		interval = 2 * time.Minute
	case "debug":
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	reserve := min(2*time.Second, options.Capture/2)
	finalize := time.NewTimer(time.Until(endsAt.Add(-reserve)))
	defer finalize.Stop()
	loop := true
	for loop {
		select {
		case <-ctx.Done():
			loop = false
		case addressErr := <-addressFailures:
			return fmt.Errorf("observe device addresses: %w", addressErr)
		case <-finalize.C:
			loop = false
		case <-ticker.C:
			sanitizer.refresh()
			current, snapshotErr := application.snapshotWithin(ctx)
			if snapshotErr != nil {
				if ctx.Err() != nil {
					loop = false

					continue
				}
				err := write(Event{Time: time.Now().UTC(), Type: "error", Message: sanitizer.sanitize(snapshotErr.Error())})
				if err != nil {
					return err
				}

				continue
			}
			err := write(Event{Time: current.Timestamp, Type: "sample", Snapshot: &current})
			if err != nil {
				return err
			}
		}
	}
	if signalContext.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return write(Event{Time: time.Now().UTC(), Type: "completed", Message: "Capture stopped by signal."})
	}
	if final, finalErr := application.snapshotWithin(ctx); finalErr == nil {
		err := write(Event{Time: final.Timestamp, Type: "final", Snapshot: &final})
		if err != nil {
			return err
		}
	}
	select {
	case addressErr := <-addressFailures:
		return fmt.Errorf("observe device addresses: %w", addressErr)
	default:
	}
	sanitizer.refresh()
	for _, line := range journalLines(ctx, cursor, 10_000) {
		if ctx.Err() != nil {
			break
		}
		err := write(Event{Time: time.Now().UTC(), Type: "log", Log: sanitizer.sanitize(line)})
		if err != nil {
			return err
		}
	}
	if ctx.Err() != nil {
		return write(Event{Time: time.Now().UTC(), Type: "timeout", Message: "Capture deadline reached; a collector may have stalled."})
	}

	return write(Event{Time: time.Now().UTC(), Type: "completed", Message: "Capture finished within its deadline."})
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

		return zero, ctx.Err()
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
