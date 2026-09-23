package installer

import (
	"errors"
	"os"
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

func TestUninstallKeepsOperationLockAcrossCleanup(t *testing.T) {
	t.Parallel()
	for _, keep := range []bool{false, true} {
		stateDir := t.TempDir()
		release, err := AcquireLock(stateDir)
		if err != nil {
			t.Fatal(err)
		}
		root := stateRoot(t, stateDir)
		before, err := os.Stat(filepath.Join(stateDir, lockName))
		if err != nil {
			t.Fatal(err)
		}
		remover := uninstaller{root: root, stateDir: stateDir, options: UninstallOptions{KeepConfig: keep}}
		if err := remover.removeInstallationState(t.Context()); err != nil {
			t.Fatal(err)
		}
		assertOperationLocked(t, stateDir)
		if err := release(); err != nil {
			t.Fatal(err)
		}
		after, err := os.Stat(filepath.Join(stateDir, lockName))
		if err != nil || !os.SameFile(before, after) {
			t.Fatalf("cleanup replaced the lock inode: %v", err)
		}
	}
}

func assertOperationLocked(t *testing.T, stateDir string) {
	t.Helper()
	second, err := AcquireLock(stateDir)
	if second != nil {
		if err := second(); err != nil {
			t.Error(err)
		}
	}
	if !errors.Is(err, errOperationInProgress) {
		t.Errorf("cleanup released the operation lock: %v", err)
	}
}
