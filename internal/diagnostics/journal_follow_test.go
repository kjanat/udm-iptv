package diagnostics

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

const liveJournalRecord = `{"__REALTIME_TIMESTAMP":"1789690127000000","SYSLOG_IDENTIFIER":"udm-iptv","_PID":"42","MESSAGE":"joined multicast group"}`

func TestLiveJournalPublishesBeforeEOF(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reader, writer := io.Pipe()
	defer func() { _ = writer.Close() }()
	events := make(chan Event, 1)
	done := make(chan error, 1)
	go func() { done <- readJournalStream(ctx, reader, journalLimits{records: 10, bytes: 1024}, events) }()
	if _, err := io.WriteString(writer, liveJournalRecord+"\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-events:
		assertEqual(t, "event", event.Type, EventLog)
		assertEqual(t, "source", event.Source, "udm-iptv[42]")
		assertEqual(t, "timestamp", event.Time, time.UnixMicro(1789690127000000).UTC())
	case <-time.After(time.Second):
		t.Fatal("journal waited for EOF before publishing")
	}
	select {
	case err := <-done:
		t.Fatalf("reader ended while pipe writer remains open: %v", err)
	default:
	}
	cancel()
	awaitJournal(t, done)
}

func awaitJournal(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, errJournalDelivery) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("journal reader did not stop after cancellation")
	}
}

func TestLiveJournalCancellationUnblocksSilentPipe(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	reader, writer := io.Pipe()
	defer func() { _ = writer.Close() }()
	done := make(chan error, 1)
	go func() {
		done <- readJournalStream(ctx, reader, journalLimits{records: 10, bytes: 1024}, make(chan Event))
	}()
	cancel()
	awaitJournal(t, done)
}

func TestLiveJournalCancellationUnblocksFullQueue(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	events := make(chan Event, 1)
	done := make(chan error, 1)
	go func() {
		done <- readJournalStream(ctx, io.NopCloser(strings.NewReader(strings.Repeat(liveJournalRecord+"\n", 3))), journalLimits{records: 10, bytes: 4096}, events)
	}()
	select {
	case <-events:
	case <-time.After(time.Second):
		t.Fatal("journal did not publish")
	}
	cancel()
	awaitJournal(t, done)
}

func TestLiveJournalBoundsAcceptedRecordsAndBytes(t *testing.T) {
	t.Parallel()
	for name, limits := range map[string]journalLimits{
		"records": {records: 1, bytes: 4096},
		"bytes":   {records: 10, bytes: len(liveJournalRecord)},
	} {
		t.Run(name, func(t *testing.T) {
			events := make(chan Event, 10)
			err := readJournalStream(t.Context(), io.NopCloser(strings.NewReader(strings.Repeat(liveJournalRecord+"\n", 3))), limits, events)
			if !errors.Is(err, errJournalLimit) || len(events) != 1 {
				t.Fatalf("unbounded journal: retained=%d err=%v", len(events), err)
			}
		})
	}
}

func TestLiveJournalBoundsIndividualRecords(t *testing.T) {
	t.Parallel()
	err := readJournalStream(t.Context(), io.NopCloser(strings.NewReader(strings.Repeat("x", journalRecordLimit+1))), journalLimits{records: 10, bytes: journalOutputLimit}, make(chan Event, 1))
	if err == nil {
		t.Fatal("oversized journal record accepted")
	}
}

func TestLiveJournalFiltersUnrelatedUniFiRecords(t *testing.T) {
	t.Parallel()
	input := `{"_SYSTEMD_UNIT":"ubios-udapi-server.service","MESSAGE":"unrelated configuration"}` + "\n" + liveJournalRecord + "\n"
	events := make(chan Event, 2)
	if err := readJournalStream(t.Context(), io.NopCloser(strings.NewReader(input)), journalLimits{records: 1, bytes: len(liveJournalRecord)}, events); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || (<-events).Log != "joined multicast group" {
		t.Fatal("irrelevant UniFi records consumed journal budget")
	}
}

func TestJournalDrainHonorsCaptureDeadline(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var notices []Event
	if err := drainJournal(ctx, make(chan Event), func(event Event) error { notices = append(notices, event); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(notices) != 1 || notices[0].Type != EventError || !strings.Contains(notices[0].Message, "in-flight records may be missing") {
		t.Fatalf("deadline lost the collection completeness notice: %+v", notices)
	}
}
