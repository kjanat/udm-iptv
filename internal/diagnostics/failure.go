package diagnostics

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

func (application *Collector) ReportFailure(parent context.Context, output io.Writer) error {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	if err := writeString(output, "\n=== udm-iptv failure diagnostics ===\n"); err != nil {
		return fmt.Errorf("write failure diagnostics header: %w", err)
	}
	sanitizer := newDiagnosticSanitizer(application.ConfigPath)
	if value, err := application.Snapshot(ctx); err == nil {
		if err := writeString(output, RenderSnapshot(value)); err != nil {
			return fmt.Errorf("write failure snapshot: %w", err)
		}
	} else {
		if err := writef(output, "Snapshot unavailable: %s\n", sanitizer.sanitize(err.Error())); err != nil {
			return fmt.Errorf("write snapshot failure: %w", err)
		}
	}
	if logs, err := journalOutput(ctx, 4<<20, "-n", "100", "--no-pager", "-o", "cat", "-u", "udm-iptv.service"); err == nil {
		if err := writeString(output, "--- recent service logs ---\n"); err != nil {
			return fmt.Errorf("write journal header: %w", err)
		}
		if err := writef(output, "%s\n", sanitizer.sanitize(strings.TrimSpace(string(logs)))); err != nil {
			return fmt.Errorf("write failure journal: %w", err)
		}
	} else {
		return fmt.Errorf("collect failure journal: %w", err)
	}
	return nil
}
