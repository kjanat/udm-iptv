package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
)

func TestConfirmCaptureStartedReportsAnEarlyExit(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	errorLogPath := filepath.Join(directory, "capture-errors.log")
	if err := atomicfile.Write(errorLogPath, []byte("open capture output: permission denied\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	worker := exec.CommandContext(t.Context(), "false")
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	err := confirmCaptureStarted(worker, errorLogPath)
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

func TestConfirmCaptureStartedAcceptsARunningWorker(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	errorLogPath := filepath.Join(directory, "capture-errors.log")
	if err := atomicfile.Write(errorLogPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	worker := exec.CommandContext(context.WithoutCancel(t.Context()), "sleep", "30")
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = worker.Process.Kill() })
	if err := confirmCaptureStarted(worker, errorLogPath); err != nil {
		t.Fatalf("running worker reported as failed: %v", err)
	}
}
