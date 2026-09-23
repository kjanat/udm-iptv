package diagnostics

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

const reportFailureTimeout = 10 * time.Second

// ReportFailure writes the snapshot and recent service logs to output.
func (application *Collector) ReportFailure(parent context.Context, output io.Writer) error {
	// Cancellation can be the failure itself; collect before rollback erases it.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), reportFailureTimeout)
	defer cancel()
	if err := writeString(output, "\n=== udm-iptv failure diagnostics ===\n"); err != nil {
		return fmt.Errorf("write failure diagnostics header: %w", err)
	}
	if err := application.writeFailureSnapshot(ctx, output); err != nil {
		return err
	}
	logs, collectErr := collectRecentJournal(ctx, failureLogLines)
	if err := writeString(output, renderRecentJournal(&logs)); err != nil {
		return errors.Join(collectErr, fmt.Errorf("write failure journal: %w", err))
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
