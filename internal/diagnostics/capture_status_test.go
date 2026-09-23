package diagnostics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
)

func TestCaptureStatusFollowsDurableLifecycleInBothFormats(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"text", "jsonl"} {
		t.Run(format, func(t *testing.T) {
			checkCaptureLifecycle(t, format)
		})
	}
}

func checkCaptureLifecycle(t *testing.T, format string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "capture."+format)
	options := Options{TextPath: path}
	if format == "jsonl" {
		options = Options{JSONPath: path}
	}
	if err := atomicfile.Write(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := openCaptureOutput(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := output.close(); err != nil {
			t.Error(err)
		}
	})
	deadline := time.Now().Add(time.Minute).UTC()
	for _, kind := range []string{EventStarted, EventCompleted, EventTimeout} {
		event := Event{Time: time.Now().UTC(), Deadline: deadline, Type: kind}
		if err := output.writer.write(event); err != nil {
			t.Fatal(err)
		}
		acknowledged := readCaptureStatus(t, options)
		if acknowledged.Type != kind || !acknowledged.Deadline.Equal(deadline) {
			t.Fatalf("lifecycle acknowledgement: %+v", acknowledged)
		}
	}
	info, err := os.Stat(StatusPath(options))
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "status permissions", info.Mode().Perm(), 0o600)
}

func TestCaptureFailureAcknowledgedWhenOutputCannotOpen(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "missing.jsonl")
	options := Options{JSONPath: path}
	if err := RecordFailure(options, errCaptureFailed); err == nil {
		t.Fatal("missing capture output was accepted")
	}
	status := readCaptureStatus(t, options)
	if status.Type != EventFailed || status.Message != errCaptureFailed.Error() {
		t.Fatalf("failure acknowledgement: %+v", status)
	}
}

func readCaptureStatus(t *testing.T, options Options) Event {
	t.Helper()
	data, err := os.ReadFile(StatusPath(options))
	if err != nil {
		t.Fatal(err)
	}
	var status Event
	if err := json.Unmarshal(data, &status); err != nil {
		t.Fatal(err)
	}
	return status
}
