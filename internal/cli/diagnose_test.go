package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
)

const captureTestGrace = 2 * time.Second

func captureTestPaths(t *testing.T) (string, string) {
	t.Helper()
	directory := t.TempDir()
	capturePath := filepath.Join(directory, "capture.jsonl")
	errorLogPath := filepath.Join(directory, "capture-errors.log")
	if err := atomicfile.Write(capturePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := atomicfile.Write(errorLogPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	return capturePath, errorLogPath
}

func startCaptureTestWorker(t *testing.T, script string, capturePath string) *exec.Cmd {
	t.Helper()
	worker := exec.CommandContext(context.WithoutCancel(t.Context()), "sh", "-c", script, "worker", capturePath)
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = worker.Process.Kill() })

	return worker
}

func TestConfirmCaptureStartedReportsAnEarlyExit(t *testing.T) {
	t.Parallel()
	capturePath, errorLogPath := captureTestPaths(t)
	if err := atomicfile.Write(errorLogPath, []byte("open capture output: permission denied\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	worker := startCaptureTestWorker(t, "exit 1", capturePath)
	err := confirmCaptureStarted(worker, capturePath, errorLogPath, captureTestGrace)
	if !errors.Is(err, errCaptureWorkerExited) {
		t.Fatalf("early exit reported as started: %v", err)
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("failure omits the worker's error log: %v", err)
	}
	if _, statErr := os.Stat(errorLogPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("error log left behind: %v", statErr)
	}
}

func TestConfirmCaptureStartedAcceptsTheFirstRecord(t *testing.T) {
	t.Parallel()
	capturePath, errorLogPath := captureTestPaths(t)
	worker := startCaptureTestWorker(t, `echo '{"type":"started"}' > "$1"; sleep 30`, capturePath)
	started := time.Now()
	if err := confirmCaptureStarted(worker, capturePath, errorLogPath, captureTestGrace); err != nil {
		t.Fatalf("running worker reported as failed: %v", err)
	}
	if elapsed := time.Since(started); elapsed >= captureTestGrace {
		t.Fatalf("confirmation waited out the grace period: %s", elapsed)
	}
}

// A one-second capture can finish before the launcher looks; that is a
// completed capture, not an initialisation failure.
func TestConfirmCaptureStartedAcceptsACompletedShortCapture(t *testing.T) {
	t.Parallel()
	capturePath, errorLogPath := captureTestPaths(t)
	worker := startCaptureTestWorker(t, `echo '{"type":"completed"}' > "$1"; exit 0`, capturePath)
	if err := confirmCaptureStarted(worker, capturePath, errorLogPath, captureTestGrace); err != nil {
		t.Fatalf("completed capture reported as failed: %v", err)
	}
}

func TestConfirmCaptureStartedRejectsAStalledWorker(t *testing.T) {
	t.Parallel()
	capturePath, errorLogPath := captureTestPaths(t)
	worker := startCaptureTestWorker(t, "sleep 30", capturePath)
	err := confirmCaptureStarted(worker, capturePath, errorLogPath, 200*time.Millisecond)
	if !errors.Is(err, errCaptureNotConfirmed) {
		t.Fatalf("stalled worker reported as started: %v", err)
	}
	if !strings.Contains(err.Error(), errorLogPath) {
		t.Fatalf("stall report omits the error log: %v", err)
	}
}
