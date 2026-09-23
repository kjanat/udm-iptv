package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/diagnostics"
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
	_, err := confirmCaptureStarted(worker, capturePath, errorLogPath, captureTestGrace)
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
	worker := startCaptureTestWorker(t, `echo '{"type":"started"}' > "$1"; exec sleep 30`, capturePath)
	started := time.Now()
	if _, err := confirmCaptureStarted(worker, capturePath, errorLogPath, captureTestGrace); err != nil {
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
	status, err := confirmCaptureStarted(worker, capturePath, errorLogPath, captureTestGrace)
	if err != nil || status.Type != diagnostics.EventCompleted {
		t.Fatalf("completed capture reported as %s: %v", status.Type, err)
	}
}

func TestConfirmCaptureStartedRejectsAStalledWorker(t *testing.T) {
	t.Parallel()
	capturePath, errorLogPath := captureTestPaths(t)
	worker := startCaptureTestWorker(t, "exec sleep 30", capturePath)
	_, err := confirmCaptureStarted(worker, capturePath, errorLogPath, 200*time.Millisecond)
	if !errors.Is(err, errCaptureNotConfirmed) {
		t.Fatalf("stalled worker reported as started: %v", err)
	}
	if !strings.Contains(err.Error(), errorLogPath) {
		t.Fatalf("stall report omits the error log: %v", err)
	}
}

func TestConfirmCaptureStartedRejectsExitWithoutCompletion(t *testing.T) {
	t.Parallel()
	for _, script := range []string{"exit 0", `echo '{"type":"timeout"}' > "$1"; exit 0`, `echo '{"type":"failed"}' > "$1"; exit 0`} {
		capturePath, errorLogPath := captureTestPaths(t)
		worker := startCaptureTestWorker(t, script, capturePath)
		if _, err := confirmCaptureStarted(worker, capturePath, errorLogPath, captureTestGrace); err == nil {
			t.Fatalf("worker without successful completion accepted: %s", script)
		}
	}
}

func TestCaptureReportUsesWorkerDeadlineAndCompletedState(t *testing.T) {
	t.Parallel()
	deadline := time.Date(2026, 9, 23, 1, 2, 3, 0, time.UTC)
	for _, kind := range []string{diagnostics.EventStarted, diagnostics.EventCompleted} {
		var output bytes.Buffer
		application := &Application{Out: &output}
		status := diagnostics.Event{Type: kind, Deadline: deadline}
		if err := application.reportCaptureStarted(diagnostics.Options{}, 1, "errors.log", status); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), "capture "+kind) {
			t.Fatalf("wrong capture state: %s", output.String())
		}
		if kind == diagnostics.EventStarted && !strings.Contains(output.String(), deadline.Format(time.RFC3339)) {
			t.Fatalf("worker deadline lost: %s", output.String())
		}
		if kind == diagnostics.EventCompleted && strings.Contains(output.String(), "Expected completion:") {
			t.Fatalf("completed capture has a future deadline: %s", output.String())
		}
	}
}
