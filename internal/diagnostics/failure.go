package diagnostics

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"
)

const reportFailureTimeout = 10 * time.Second

// ReportFailure writes the snapshot and recent service logs to output.
func (application *Collector) ReportFailure(parent context.Context, output io.Writer) error {
	ctx, cancel := context.WithTimeout(parent, reportFailureTimeout)
	defer cancel()
	if err := writeString(output, "\n=== udm-iptv failure diagnostics ===\n"); err != nil {
		return fmt.Errorf("write failure diagnostics header: %w", err)
	}
	if err := application.writeFailureSnapshot(ctx, output); err != nil {
		return err
	}
	logs, collectErr := journalOutput(ctx, journalOutputLimit, "-n", strconv.Itoa(failureLogLines), "--no-pager", "-o", "json", "-u", serviceUnit)
	if err := writef(output, "--- recent service logs (last %d records; output limit %d bytes) ---\n", failureLogLines, journalOutputLimit); err != nil {
		return fmt.Errorf("write journal header: %w", err)
	}
	for _, entry := range parseJournal(logs.data) {
		if err := writeString(output, RenderEvent(entry.event())); err != nil {
			return errors.Join(collectErr, fmt.Errorf("write failure journal: %w", err))
		}
	}
	if len(logs.stderr) > 0 {
		if err := writef(output, "journalctl stderr: %s\n", logs.stderr); err != nil {
			return errors.Join(collectErr, err)
		}
	}
	if collectErr != nil {
		return errors.Join(fmt.Errorf("collect failure journal: %w", collectErr), writef(output, "Journal collection incomplete: %v\n", collectErr))
	}

	return nil
}

func (application *Collector) writeFailureSnapshot(ctx context.Context, output io.Writer) error {
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
	return nil
}
