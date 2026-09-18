package diagnostics

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
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
	if value, err := application.Snapshot(ctx); err == nil {
		if err := writeString(output, RenderSnapshot(value)); err != nil {
			return fmt.Errorf("write failure snapshot: %w", err)
		}
	} else {
		if err := writef(output, "Snapshot unavailable: %s\n", err.Error()); err != nil {
			return fmt.Errorf("write snapshot failure: %w", err)
		}
	}
	if logs, err := journalOutput(ctx, journalOutputLimit, "-n", strconv.Itoa(failureLogLines), "--no-pager", "-o", "cat", "-u", "udm-iptv.service"); err == nil {
		if err := writeString(output, "--- recent service logs ---\n"); err != nil {
			return fmt.Errorf("write journal header: %w", err)
		}
		if err := writef(output, "%s\n", strings.TrimSpace(string(logs))); err != nil {
			return fmt.Errorf("write failure journal: %w", err)
		}
	} else {
		return fmt.Errorf("collect failure journal: %w", err)
	}
	return nil
}
