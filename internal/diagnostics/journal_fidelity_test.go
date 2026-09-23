package diagnostics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
)

func TestJournalOutputPreservesPartialStdoutAndStderr(t *testing.T) {
	installJournalScript(t, "printf '%s\\n' '"+liveJournalRecord+"'\nprintf '%s\\n' 'Permission denied reading journal' >&2\nexit 7\n")
	data, err := journalOutput(t.Context(), journalOutputLimit)
	if err == nil {
		t.Fatal("journalctl exit failure disappeared")
	}
	assertDiagnosticDetails(t, string(data.data), "joined multicast group")
	assertDiagnosticDetails(t, string(data.stderr), "Permission denied reading journal")
	assertDiagnosticDetails(t, err.Error(), "Permission denied reading journal", "exit status 7")
	var report bytes.Buffer
	err = (&Collector{ConfigPath: filepath.Join(t.TempDir(), "absent.json")}).ReportFailure(t.Context(), &report)
	if err == nil {
		t.Fatal("partial report lost collection error")
	}
	assertDiagnosticDetails(t, report.String(), "joined multicast group", "Permission denied reading journal", "Journal collection incomplete")
}

func TestJournalOutputMakesDiscardedBytesExplicit(t *testing.T) {
	installJournalScript(t, "printf '%s' '1234567890'\n")
	data, err := journalOutput(t.Context(), 4)
	if string(data.data) != "7890" || !errors.Is(err, errJournalLimit) {
		t.Fatalf("bounded output = %q, %v", data, err)
	}
	assertDiagnosticDetails(t, err.Error(), "discarded 6 leading output bytes", "retained 4 bytes")
}

func installJournalScript(t *testing.T, body string) {
	t.Helper()
	directory := t.TempDir()
	if err := atomicfile.Write(filepath.Join(directory, "journalctl"), []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
}

func TestJournalMetadataAndMalformedRecordsSurvive(t *testing.T) {
	t.Parallel()
	const record = `{"__REALTIME_TIMESTAMP":"1789690127123456","MESSAGE":"joined 239.1.2.3","PRIORITY":"4","_SYSTEMD_UNIT":"udm-iptv.service","_BOOT_ID":"boot-123","__CURSOR":"cursor-7","UNKNOWN_FIELD":[255,0,42]}`
	entries := parseJournal([]byte(record + "\n{broken\n"))
	if len(entries) != 2 {
		t.Fatal("malformed record silently dropped")
	}
	data, err := json.Marshal(entries[0].event())
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{string(data), RenderEvent(entries[0].event())} {
		assertDiagnosticDetails(t, output, `"PRIORITY":"4"`, `"_BOOT_ID":"boot-123"`, `"__CURSOR":"cursor-7"`, `"UNKNOWN_FIELD":[255,0,42]`, "2026-09-18T00:08:47.123456Z")
	}
	assertDiagnosticDetails(t, RenderEvent(entries[1].event()), "Malformed journal record", "{broken")
}

func TestLiveJournalRetainsMalformedAndOversizedRecordEvidence(t *testing.T) {
	t.Parallel()
	for _, input := range []string{"{broken\n", strings.Repeat("x", journalRecordLimit+1)} {
		events := make(chan Event, 2)
		err := readJournalStream(t.Context(), io.NopCloser(strings.NewReader(input)), journalLimits{records: 2, bytes: journalOutputLimit}, events)
		if len(input) > journalRecordLimit && !errors.Is(err, errJournalLimit) {
			t.Fatalf("oversized record did not report truncation: %v", err)
		}
		if len(events) != 1 {
			t.Fatal("lost invalid record evidence")
		}
		event := <-events
		if event.Type != EventError || !strings.Contains(event.Message, input[:7]) {
			t.Fatalf("missing raw record prefix: %s", event.Message)
		}
	}
}

func TestJournalDrainRetainsBufferedEventsAtDeadline(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	events := make(chan Event, 1)
	events <- Event{Type: EventLog, Log: "last queued log"}
	close(events)
	var output []Event
	if err := drainJournal(ctx, events, func(event Event) error { output = append(output, event); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(output) != 1 || output[0].Log != "last queued log" {
		t.Fatalf("deadline discarded buffered records: %+v", output)
	}
}

func TestLiveJournalReportsUnexpectedEOFAndStderr(t *testing.T) {
	installJournalScript(t, "printf '%s\\n' '"+liveJournalRecord+"'\nprintf '%s\\n' 'journal index damaged' >&2\n")
	events, stop := followJournal(t.Context())
	defer stop()
	var text strings.Builder
	for event := range events {
		text.WriteString(RenderEvent(event))
	}
	assertDiagnosticDetails(t, text.String(), "joined multicast group", "ended before the capture finished", "journal index damaged")
}

func TestJournalCancellationReportsUndeliveredRawRecord(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	limits := journalLimits{records: 1, bytes: len(liveJournalRecord)}
	err := forwardJournalLine(ctx, []byte(liveJournalRecord), &limits, make(chan Event))
	if !errors.Is(err, errJournalDelivery) {
		t.Fatalf("undelivered record vanished: %v", err)
	}
	assertDiagnosticDetails(t, err.Error(), "joined multicast group", "raw=")
}

func TestFinalSnapshotErrorAppearsInCapture(t *testing.T) {
	t.Parallel()
	collector := &Collector{ConfigPath: filepath.Join(t.TempDir(), "missing.json")}
	var output strings.Builder
	if err := collector.finalizeCapture(t.Context(), func(event Event) error { output.WriteString(RenderEvent(event)); return nil }); err != nil {
		t.Fatal(err)
	}
	assertDiagnosticDetails(t, output.String(), "Final snapshot unavailable", "missing.json", "no such file")
}

func TestTextSampleKeepsSnapshotAndAttachedDetails(t *testing.T) {
	t.Parallel()
	event := Event{Type: EventSample, Snapshot: &Snapshot{Config: configSummary{MACAddress: "02:11:22:33:44:55"}}, Message: "snapshot explanation", Log: "extra event log"}
	assertDiagnosticDetails(t, RenderEvent(event), "02:11:22:33:44:55", "snapshot explanation", "extra event log")
	assertDiagnosticDetails(t, RenderEvent(Event{Type: EventFailed, Message: "capture disk full"}), "Capture failed", "capture disk full")
}
