package diagnostics

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"time"
)

const reportFailureTimeout = 10 * time.Second

// ReportFailure writes the snapshot and recent service logs to output.
func (application *Collector) ReportFailure(parent context.Context, output io.Writer) error {
	return application.ReportFailureWithExport(parent, output, io.Discard)
}

// ReportFailureWithExport keeps full local evidence separate from the outbound
// sanitized attachment. Both are derived from the same observation.
func (application *Collector) ReportFailureWithExport(parent context.Context, output, shared io.Writer) error {
	ctx, cancel := context.WithTimeout(parent, reportFailureTimeout)
	defer cancel()
	exporter, err := NewExporter()
	if err != nil {
		return err
	}
	writeShared := func(event Event) error {
		clean, err := exporter.Event(event)
		if err != nil {
			return err
		}
		return writeString(shared, RenderEvent(clean))
	}
	if err := writeString(output, "\n=== udm-iptv failure diagnostics ===\n"); err != nil {
		return fmt.Errorf("write failure diagnostics header: %w", err)
	}
	if err := application.writeFailureSnapshot(ctx, output, writeShared); err != nil {
		return err
	}
	logs, err := journalOutput(ctx, journalOutputLimit, "-n", strconv.Itoa(failureLogLines), "--no-pager", "-o", "json", "-u", serviceUnit)
	if err != nil {
		return fmt.Errorf("collect failure journal: %w", err)
	}
	if err := writeString(output, "--- recent service logs ---\n"); err != nil {
		return fmt.Errorf("write journal header: %w", err)
	}
	for _, entry := range parseJournal(logs) {
		if err := writef(output, "%s\n", renderJournalEntry(entry.Time, entry.Source, entry.Message)); err != nil {
			return fmt.Errorf("write failure journal: %w", err)
		}
		if err := writeShared(Event{Time: entry.Time, Type: EventLog, Source: entry.Source, Log: entry.Message}); err != nil {
			return err
		}
	}

	return nil
}

func (application *Collector) writeFailureSnapshot(ctx context.Context, output io.Writer, writeShared func(Event) error) error {
	value, err := application.Snapshot(ctx)
	if err != nil {
		if err := writef(output, "Snapshot unavailable: %s\n", err.Error()); err != nil {
			return fmt.Errorf("write snapshot failure: %w", err)
		}
		return nil
	}
	if err := writeString(output, RenderSnapshot(value)); err != nil {
		return fmt.Errorf("write failure snapshot: %w", err)
	}
	return writeShared(Event{Time: value.Timestamp, Type: EventInitial, Snapshot: &value})
}
