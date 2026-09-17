package diagnostics

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
)

var (
	errOutputUnavailable = errors.New("output unavailable")
	errCaptureFailed     = errors.New("capture failed at 192.168.1.1")
	errRecordFailure     = errors.New("failure")
	errDiskFull          = errors.New("disk full")
)

type failedOutput struct{ err error }

func TestJournalOutputBound(t *testing.T) {
	output := &boundedJournal{limit: 8}
	if n, err := output.Write([]byte("123456")); n != 6 || err != nil {
		t.Fatal(n, err)
	}
	if n, err := output.Write([]byte("78901234")); n != 2 || !errors.Is(err, io.ErrShortBuffer) {
		t.Fatal(n, err)
	}
	if output.String() != "12345678" {
		t.Fatal("journal exceeded memory limit")
	}
	if n, err := output.Write([]byte("more")); n != 0 || !errors.Is(err, io.ErrShortBuffer) {
		t.Fatal(n, err)
	}
}

func (output failedOutput) Write([]byte) (int, error) { return 0, output.err }

func TestFailureReportPropagatesOutputError(t *testing.T) {
	want := errOutputUnavailable
	err := (&Collector{}).ReportFailure(context.Background(), failedOutput{want})
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "header") {
		t.Fatalf("lost output error: %v", err)
	}
}

func TestFailureRecordingAttemptsBothFormats(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "report.jsonl")
	if err := atomicfile.Write(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := RecordFailure(Options{TextPath: directory, JSONPath: path}, errCaptureFailed)
	if err == nil || !strings.Contains(err.Error(), "record capture failure") {
		t.Fatal("missing text failure")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var event Event
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "failed" || strings.Contains(event.Message, "192.168.1.1") {
		t.Fatal("missing or unsafe failure record")
	}
}

func TestFailureRecordingRejectsSymlink(t *testing.T) {
	directory := t.TempDir()
	target, link := filepath.Join(directory, "target"), filepath.Join(directory, "report")
	if err := atomicfile.Write(target, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := RecordFailure(Options{TextPath: link}, errRecordFailure); err == nil {
		t.Fatal("followed symlink")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "unchanged" {
		t.Fatal("modified symlink target")
	}
}

func TestAppendFailurePropagatesWriteError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report")
	if err := atomicfile.Write(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	want := errDiskFull
	err := appendFailure(path, func(io.Writer) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("lost write error: %v", err)
	}
}
