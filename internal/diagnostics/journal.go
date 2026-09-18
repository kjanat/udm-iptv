package diagnostics

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	journalCursorLimit = 64 << 10
	journalOutputLimit = 4 << 20
	// failureLogLines is how many recent lines a failure report attaches.
	failureLogLines = 100
	serviceUnit     = "udm-iptv.service"
	// udapiUnit is the UniFi configuration server. It logs every iptables
	// rule it finds that it did not write, which includes the NAT rules here.
	udapiUnit = "ubios-udapi-server.service"
	// captureSource labels lines the capture itself adds to the journal block.
	captureSource = "capture"
)

// journalEntry is one journal record with the fields a capture keeps: the
// time the record was logged, who logged it, and the message.
type journalEntry struct {
	Time    time.Time
	Source  string
	Message string
}

type boundedJournal struct {
	buf   []byte
	limit int
}

func (output *boundedJournal) Write(data []byte) (int, error) {
	if output.limit <= 0 {
		output.buf = output.buf[:0]
		return len(data), nil
	}
	if len(data) >= output.limit {
		output.buf = append(output.buf[:0], data[len(data)-output.limit:]...)
		return len(data), nil
	}
	total := len(output.buf) + len(data)
	if total > output.limit {
		output.buf = append(output.buf[total-output.limit:], data...)
		return len(data), nil
	}
	output.buf = append(output.buf, data...)
	return len(data), nil
}

func (output *boundedJournal) Bytes() []byte {
	return output.buf
}

func (output *boundedJournal) String() string {
	return string(output.buf)
}

func journalOutput(ctx context.Context, limit int, arguments ...string) ([]byte, error) {
	output := &boundedJournal{limit: limit}
	command := exec.CommandContext(ctx, "journalctl", arguments...)
	command.Stdout = output
	command.WaitDelay = time.Second
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("read bounded journal output: %w", err)
	}
	return output.Bytes(), nil
}

func journalCursor(ctx context.Context) string {
	output, err := journalOutput(ctx, journalCursorLimit, "-n", "0", "--show-cursor", "--no-pager", "-u", serviceUnit)
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(output), "\n") {
		if cursor, found := strings.CutPrefix(strings.TrimSpace(line), "-- cursor: "); found {
			return cursor
		}
	}

	return ""
}

// captureJournal collects what the service logged after cursor, and what
// the UniFi configuration server logged about udm-iptv in the same window,
// ordered by the time each record was logged.
func captureJournal(ctx context.Context, cursor string, limit int) []journalEntry {
	if cursor == "" {
		return []journalEntry{captureNote("journal cursor unavailable; service logs were not collected")}
	}
	entries, err := journalEntries(ctx, cursor, limit, serviceUnit)
	if err != nil {
		return []journalEntry{captureNote("journal unavailable: " + err.Error())}
	}
	related, err := journalEntries(ctx, cursor, limit, udapiUnit)
	if err != nil {
		entries = append(entries, captureNote(udapiUnit+" journal unavailable: "+err.Error()))
	}
	for _, entry := range related {
		if strings.Contains(entry.Message, "udm-iptv") {
			entries = append(entries, entry)
		}
	}
	slices.SortStableFunc(entries, func(left, right journalEntry) int { return left.Time.Compare(right.Time) })

	return entries
}

func captureNote(message string) journalEntry {
	return journalEntry{Time: time.Now().UTC(), Source: captureSource, Message: message}
}

func journalEntries(ctx context.Context, cursor string, limit int, unit string) ([]journalEntry, error) {
	output, err := journalOutput(ctx, journalOutputLimit, journalArguments(cursor, limit, unit)...)
	if err != nil {
		return nil, err
	}
	entries := parseJournal(output)
	if len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}

	return entries, nil
}

func journalArguments(cursor string, limit int, unit string) []string {
	return []string{"--no-pager", "-o", "json", "-n", strconv.Itoa(limit), "-u", unit, "--after-cursor", cursor}
}

// journalRecord is a journalctl JSON record, keyed by journal field name.
type journalRecord map[string]json.RawMessage

// parseJournal reads one JSON record per line. The bounded reader can cut
// the first line, so a line that does not parse is dropped.
func parseJournal(data []byte) []journalEntry {
	var entries []journalEntry
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var record journalRecord
		if err := json.Unmarshal(line, &record); err != nil {
			continue
		}
		entries = append(entries, record.entry())
	}

	return entries
}

func (record journalRecord) entry() journalEntry {
	entry := journalEntry{Time: journalTime(record.text("__REALTIME_TIMESTAMP")), Source: record.text("SYSLOG_IDENTIFIER"), Message: record.text("MESSAGE")}
	if entry.Source == "" {
		entry.Source = record.text("_COMM")
	}
	if pid := record.text("_PID"); pid != "" {
		entry.Source += "[" + pid + "]"
	}

	return entry
}

// text returns a field as a string. journald serialises a field that is not
// valid UTF-8 as an array of bytes.
func (record journalRecord) text(field string) string {
	raw, ok := record[field]
	if !ok {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var data []byte
	if err := json.Unmarshal(raw, &data); err == nil {
		return string(data)
	}

	return ""
}

// journalTime converts the journal's microsecond timestamp.
func journalTime(value string) time.Time {
	microseconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return time.Time{}
	}

	return time.UnixMicro(microseconds).UTC()
}

// renderJournalEntry formats an entry the way journalctl's short-iso output
// does, with the time the record was logged.
func renderJournalEntry(at time.Time, source, message string) string {
	if source == "" {
		return message
	}

	return at.UTC().Format(time.RFC3339) + " " + source + ": " + message
}
