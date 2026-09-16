package ui

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
)

func TestCaptureCompletion(t *testing.T) {
	t.Parallel()
	want := time.Date(2026, time.September, 14, 13, 15, 6, 0, time.UTC)
	got := captureCompletion("Capture started; expected completion 2026-09-14T13:15:06Z\n")
	if !got.Equal(want) {
		t.Fatalf("captureCompletion() = %s, want %s", got, want)
	}
	jsonValue := captureCompletion(`{"message":"Capture started; expected completion 2026-09-14T13:15:06Z"}`)
	if !jsonValue.Equal(want) {
		t.Fatalf("captureCompletion(JSON) = %s, want %s", jsonValue, want)
	}
}

func TestCaptureModelRecognizesFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "capture.txt")
	err := atomicfile.Write(path, []byte("Capture failed: journal unavailable\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	model := NewCaptureModel(path, time.Time{}, 0)
	updated, _ := model.Update(captureTick(time.Now()))
	result, ok := updated.(captureModel)
	if !ok {
		t.Fatalf("Update() returned %T, want captureModel", updated)
	}
	if !result.failed {
		t.Fatal("failed capture was not recognized")
	}
}

func TestCaptureModelRecognizesTimeout(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "capture.txt")
	err := atomicfile.Write(path, []byte("Capture timed out: collector stalled\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	model := NewCaptureModel(path, time.Time{}, 0)
	updated, _ := model.Update(captureTick(time.Now()))
	result, ok := updated.(captureModel)
	if !ok {
		t.Fatalf("Update() returned %T, want captureModel", updated)
	}
	if !result.failed {
		t.Fatal("timed-out capture was not recognized")
	}
}
