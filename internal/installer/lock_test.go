package installer

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestLockRefusesAConcurrentOperation(t *testing.T) {
	t.Parallel()
	stateDir := filepath.Join(t.TempDir(), "udm-iptv")
	release, err := AcquireLock(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireLock(stateDir); !errors.Is(err, errOperationInProgress) {
		t.Fatalf("second acquisition: %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	release, err = AcquireLock(stateDir)
	if err != nil {
		t.Fatalf("acquisition after release: %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
}

func TestLockRejectsSharedDirectories(t *testing.T) {
	t.Parallel()
	if _, err := AcquireLock("/tmp"); !errors.Is(err, errStateDirectoryShared) {
		t.Fatalf("shared directory accepted: %v", err)
	}
}
