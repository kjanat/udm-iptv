package diagnostics

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRenderCompletedEvent(t *testing.T) {
	t.Parallel()
	output := RenderEvent(Event{Type: "completed", Message: "Capture reached its deadline."})
	if !strings.Contains(output, "Capture completed") {
		t.Fatalf("unexpected completion output: %q", output)
	}
}

func TestCollectorsShareCaptureDeadline(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	release := make(chan struct{})
	started := time.Now()
	_, err := collectWithin(ctx, func() (string, error) {
		<-release

		return "late", nil
	})
	close(release)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("collector error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("collector exceeded shared deadline by %s", elapsed)
	}
}

// A journal record keeps the time it was logged and who logged it; a
// message journald stored as bytes is decoded like any other.
func TestParseJournalKeepsSourceTimestamps(t *testing.T) {
	t.Parallel()
	records := `{"__REALTIME_TIMESTAMP":"1789690127000000","SYSLOG_IDENTIFIER":"udm-iptv","_PID":"1745611","MESSAGE":"group 224.0.252.133 exclude mode"}
{"__REALTIME_TIMESTAMP":"1789690080000000","_COMM":"ubios-udapi-se","_PID":"2925","MESSAGE":[102,105,114,101,119,97,108,108]}
not json
`
	entries := parseJournal([]byte(records))
	assertEqual(t, "entries", len(entries), 3)
	assertDiagnosticDetails(t, RenderEvent(entries[2].event()), "Malformed journal record", "not json")
	assertEqual(t, "time", entries[0].Time, time.Date(2026, 9, 18, 0, 8, 47, 0, time.UTC))
	assertEqual(t, "source", entries[0].Source, "udm-iptv[1745611]")
	assertEqual(t, "message", entries[0].Message, "group 224.0.252.133 exclude mode")
	assertEqual(t, "command fallback", entries[1].Source, "ubios-udapi-se[2925]")
	assertEqual(t, "byte message", entries[1].Message, "firewall")
	rendered := RenderEvent(Event{Time: entries[0].Time, Type: EventLog, Source: entries[0].Source, Log: entries[0].Message})
	assertEqual(t, "rendered", rendered, "2026-09-18T00:08:47Z udm-iptv[1745611]: group 224.0.252.133 exclude mode\n")
}

func drainMarkers(t *testing.T, reader *markerReader) []Event {
	t.Helper()
	var events []Event
	err := reader.drain(func(event Event) error {
		events = append(events, event)

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	return events
}

func TestMarkersFoldIntoTheCaptureOnce(t *testing.T) {
	t.Parallel()
	capture := filepath.Join(t.TempDir(), "udm-iptv-20260918T200000Z-1.jsonl")
	text := strings.TrimSuffix(capture, ".jsonl") + ".txt"
	assertEqual(t, "marker path", MarkerPath(Options{JSONPath: capture, TextPath: text}), MarkerPathFor(text))
	at := time.Date(2026, 9, 18, 20, 5, 0, 0, time.UTC)
	if err := WriteMarker(capture, "TV switched on\n", at); err != nil {
		t.Fatal(err)
	}
	reader := &markerReader{path: MarkerPath(Options{JSONPath: capture})}
	first := drainMarkers(t, reader)
	assertEqual(t, "markers after first drain", len(first), 1)
	assertEqual(t, "marker type", first[0].Type, EventMarker)
	assertEqual(t, "marker text", first[0].Message, "TV switched on")
	assertEqual(t, "marker time", first[0].Time, at)
	assertEqual(t, "markers after second drain", len(drainMarkers(t, reader)), 0)
	if err := WriteMarker(capture, "channel changed", at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	second := drainMarkers(t, reader)
	assertEqual(t, "markers after a new line", len(second), 1)
	assertEqual(t, "second marker", second[0].Message, "channel changed")
	unstamped := markerEvent("no stamp")
	assertEqual(t, "unstamped text", unstamped.Message, "no stamp")
	assertEqual(t, "unstamped time set", unstamped.Time.IsZero(), false)
}

func assertEqual[T comparable](t *testing.T, name string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}
