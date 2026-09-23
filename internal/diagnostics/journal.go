package diagnostics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	journalOutputLimit = 4 << 20
	// failureLogLines is how many recent lines a failure report attaches.
	failureLogLines = 100
	serviceUnit     = "udm-iptv.service"
	// udapiUnit is the UniFi configuration server. It logs every iptables
	// rule it finds that it did not write, which includes the NAT rules here.
	udapiUnit = "ubios-udapi-server.service"
)

// journalEntry is one journal record with the fields a capture keeps: the
// time the record was logged, who logged it, and the message.
type journalEntry struct {
	Time     time.Time
	Source   string
	Message  string
	Metadata journalRecord
	Problem  string
}

type boundedJournal struct {
	buf     []byte
	limit   int
	dropped int
}

func (output *boundedJournal) Write(data []byte) (int, error) {
	if output.limit <= 0 {
		output.dropped += len(output.buf) + len(data)
		output.buf = output.buf[:0]
		return len(data), nil
	}
	if len(data) >= output.limit {
		output.dropped += len(output.buf) + len(data) - output.limit
		output.buf = append(output.buf[:0], data[len(data)-output.limit:]...)
		return len(data), nil
	}
	total := len(output.buf) + len(data)
	if total > output.limit {
		output.dropped += total - output.limit
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

type journalResult struct {
	data   []byte
	stderr []byte
}

func journalOutput(ctx context.Context, limit int, arguments ...string) (journalResult, error) {
	output := &boundedJournal{limit: limit}
	stderr := &boundedJournal{limit: journalRecordLimit}
	command := exec.CommandContext(ctx, "journalctl", arguments...)
	command.Stdout = output
	command.Stderr = stderr
	command.WaitDelay = time.Second
	err := journalCommandError(command.Run(), stderr)
	if output.dropped > 0 {
		err = errors.Join(err, fmt.Errorf("%w: discarded %d leading output bytes; retained %d bytes", errJournalLimit, output.dropped, len(output.buf)))
	}
	return journalResult{data: output.Bytes(), stderr: stderr.Bytes()}, err
}

func journalCommandError(err error, stderr *boundedJournal) error {
	if stderr.dropped > 0 {
		err = errors.Join(err, fmt.Errorf("%w: discarded %d leading stderr bytes", errJournalLimit, stderr.dropped))
	}
	if detail := strings.TrimSpace(stderr.String()); detail != "" && err != nil {
		return fmt.Errorf("journalctl stderr %q: %w", detail, err)
	}
	if err != nil {
		return fmt.Errorf("read journal: %w", err)
	}
	return nil
}

// journalRecord is a journalctl JSON record, keyed by journal field name.
type journalRecord map[string]json.RawMessage

// parseJournal retains malformed records as explicit errors, including the raw
// bytes. A bounded read can cut the first record; that loss stays observable.
func parseJournal(data []byte) []journalEntry {
	var entries []journalEntry
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		entries = append(entries, parseJournalLine(line))
	}

	return entries
}

func parseJournalLine(line []byte) journalEntry {
	var record journalRecord
	if err := json.Unmarshal(line, &record); err != nil {
		return journalEntry{Time: time.Now().UTC(), Problem: fmt.Sprintf("Malformed journal record: %v; raw=%q", err, line)}
	}
	if record == nil {
		return journalEntry{Time: time.Now().UTC(), Problem: fmt.Sprintf("Malformed journal record: expected object; raw=%q", line)}
	}
	return record.entry()
}

func (entry journalEntry) event() Event {
	if entry.Problem != "" {
		return Event{Time: entry.Time, Type: EventError, Message: entry.Problem}
	}
	return Event{Time: entry.Time, Type: EventLog, Source: entry.Source, Log: entry.Message, Journal: entry.Metadata}
}

func (record journalRecord) entry() journalEntry {
	entry := journalEntry{Time: journalTime(record.text("__REALTIME_TIMESTAMP")), Source: record.text("SYSLOG_IDENTIFIER"), Message: record.text("MESSAGE"), Metadata: record}
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

	return string(raw)
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
	var prefix string
	if !at.IsZero() {
		prefix = at.UTC().Format(time.RFC3339Nano) + " "
	}
	if source != "" {
		prefix += source + ": "
	}
	return prefix + message
}
