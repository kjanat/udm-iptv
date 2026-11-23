package diagnostics

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
)

var errMarkerReadFixture = errors.New("marker read interrupted")

type interruptedMarkerInput struct{}

func (interruptedMarkerInput) Read(output []byte) (int, error) {
	return copy(output, "partial\tmarker"), errMarkerReadFixture
}

func TestMarkerReadFailureKeepsCauseAndPartialRecord(t *testing.T) {
	t.Parallel()
	reader := markerReader{path: "capture.markers"}
	var events []Event
	err := reader.readRecords(interruptedMarkerInput{}, false, func(event Event) error { events = append(events, event); return nil })
	if !errors.Is(err, errMarkerReadFixture) || len(events) != 1 || events[0].Type != EventError {
		t.Fatalf("read failure lost cause or event: %v, %+v", err, events)
	}
	assertDiagnosticDetails(t, events[0].Message, "read marker file capture.markers", errMarkerReadFixture.Error(), strconv.Quote("partial\tmarker"))
}

func TestMarkerSeekAndOpenFailuresAreReported(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "markers")
	if err := atomicfile.Write(path, []byte("marker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loop := filepath.Join(directory, "loop")
	if err := os.Symlink("loop", loop); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		reader    markerReader
		operation string
	}{
		{markerReader{path: path, offset: -1}, "seek"},
		{markerReader{path: loop}, "open"},
		{markerReader{path: directory}, "inspect"},
	} {
		var message string
		err := fixture.reader.drain(func(event Event) error { message = event.Message; return nil })
		if err == nil || message != err.Error() || !strings.Contains(message, fixture.operation+" marker file") {
			t.Fatalf("%s failure disappeared: %v, %q", fixture.operation, err, message)
		}
	}
}

func TestMarkerFinalizationRetainsIncompleteRecordExactlyOnce(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "capture.markers")
	const partial = "2026-09-23T11:12:13Z\tTV on\x00partial"
	if err := atomicfile.Write(path, []byte(partial), 0o600); err != nil {
		t.Fatal(err)
	}
	reader := markerReader{path: path}
	var events []Event
	write := func(event Event) error { events = append(events, event); return nil }
	if err := reader.drain(write); err != nil || len(events) != 0 || reader.offset != 0 {
		t.Fatalf("unfinished append was consumed before finalization: %v, %+v", err, events)
	}
	if err := reader.finish(write); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != EventError {
		t.Fatalf("incomplete record disappeared: %+v", events)
	}
	assertDiagnosticDetails(t, events[0].Message, "Incomplete final marker", strconv.Quote(partial))
	if err := reader.finish(write); err != nil || len(events) != 1 {
		t.Fatalf("finalized marker duplicated: %v, %+v", err, events)
	}
}

func TestCaptureCleanupKeepsQueuedEvidenceAtDeadlineOnce(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	logs := make(chan Event, 1)
	logs <- Event{Type: EventLog, Log: "already collected"}
	close(logs)
	stops := 0
	evidence := captureEvidence{markers: &markerReader{path: filepath.Join(t.TempDir(), "absent")}, logs: logs, stopLogs: func() { stops++ }}
	var events []Event
	write := func(event Event) error { events = append(events, event); return nil }
	if err := evidence.finish(ctx, write); err != nil {
		t.Fatal(err)
	}
	if err := evidence.finish(ctx, write); err != nil {
		t.Fatal(err)
	}
	if stops != 1 || len(events) != 1 || events[0].Log != "already collected" {
		t.Fatalf("cleanup duplicated or lost evidence: stops=%d, events=%+v", stops, events)
	}
}

func TestInitialSnapshotFailurePreservesMarkersBeforeClosingCapture(t *testing.T) {
	installJournalScript(t, "exit 0\n")
	directory := t.TempDir()
	options := Options{Capture: time.Second, JSONPath: filepath.Join(directory, "capture.jsonl")}
	if err := atomicfile.Write(options.JSONPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := atomicfile.Write(MarkerPath(options), []byte("2026-09-23T11:12:13Z\tcomplete marker\nunfinished marker"), 0o600); err != nil {
		t.Fatal(err)
	}
	collector := Collector{ConfigPath: filepath.Join(directory, "missing.json")}
	if err := collector.Capture(t.Context(), options); err == nil {
		t.Fatal("missing configuration unexpectedly produced a snapshot")
	}
	data, err := os.ReadFile(options.JSONPath)
	if err != nil {
		t.Fatal(err)
	}
	assertDiagnosticDetails(t, string(data), "complete marker", "Incomplete final marker", "unfinished marker", "Initial snapshot unavailable")
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	for {
		var event Event
		err := decoder.Decode(&event)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestSnapshotEventExtraDetailsRenderOnceInOrder(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{EventInitial, EventSample, EventFinal, "snapshot"} {
		event := Event{Type: kind, Snapshot: &Snapshot{Version: "snapshot-version"}, Message: "extra-message", Log: "extra-log"}
		text := RenderEvent(event)
		if strings.Count(text, "extra-message") != 1 || strings.Count(text, "extra-log") != 1 || strings.Index(text, "snapshot-version") > strings.Index(text, "extra-message") || strings.Index(text, "extra-message") > strings.Index(text, "extra-log") {
			t.Fatalf("%s details duplicated or reordered:\n%s", kind, text)
		}
	}
	text := RenderEvent(Event{Type: EventError, Snapshot: &Snapshot{}, Message: "error detail"})
	if strings.Count(text, "error detail") != 1 {
		t.Fatalf("message rendered twice merely because a snapshot was attached: %s", text)
	}
}
