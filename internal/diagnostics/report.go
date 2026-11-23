package diagnostics

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

type recentJournal struct {
	Events      []Event `json:"events"`
	Stderr      string  `json:"stderr,omitempty"`
	RecordLimit int     `json:"recordLimit"`
	ByteLimit   int     `json:"byteLimit"`
}

const debugLogLines = 300

// ReportSnapshot adds current-boot service and restore logs to a one-shot report.
// Periodic captures use Snapshot and their live journal stream instead.
func (application *Collector) ReportSnapshot(ctx context.Context, verbosity string) (Snapshot, error) {
	value, err := application.Snapshot(ctx)
	if err != nil {
		return value, err
	}
	count := failureLogLines
	if verbosity == "debug" {
		count = debugLogLines
	}
	logs, err := collectRecentJournal(ctx, count)
	value.RecentLogs = &logs
	recordCollectionError(&value.Errors, "journal", err)
	return value, nil
}

func collectRecentJournal(ctx context.Context, count int) (recentJournal, error) {
	logs, err := journalOutput(ctx, journalOutputLimit, "-b", "-n", strconv.Itoa(count), "--no-pager", "-o", "json", "-u", serviceUnit, "-u", restoreUnit)
	value := recentJournal{Events: []Event{}, Stderr: string(logs.stderr), RecordLimit: count, ByteLimit: journalOutputLimit}
	for _, entry := range parseJournal(logs.data) {
		value.Events = append(value.Events, entry.event())
	}
	return value, err
}

func renderRecentJournal(logs *recentJournal) string {
	if logs == nil {
		return ""
	}
	var output strings.Builder
	fmt.Fprintf(&output, "--- recent service logs (current boot; last %d records; output limit %d bytes) ---\n", logs.RecordLimit, logs.ByteLimit)
	for _, entry := range logs.Events {
		output.WriteString(RenderEvent(entry))
	}
	if logs.Stderr != "" {
		fmt.Fprintf(&output, "journalctl stderr: %s\n", logs.Stderr)
	}
	return output.String()
}
