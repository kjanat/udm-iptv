package installer

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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

func TestProbeLockReportsAHeldLockAndLeavesNoneBehind(t *testing.T) {
	t.Parallel()
	stateDir := filepath.Join(t.TempDir(), "udm-iptv")
	if err := ProbeLock(stateDir); err != nil {
		t.Fatal(err)
	}
	release, err := AcquireLock(stateDir)
	if err != nil {
		t.Fatalf("acquisition after probe: %v", err)
	}
	if err := ProbeLock(stateDir); !errors.Is(err, errOperationInProgress) {
		t.Fatalf("probe during an operation: %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
}

func TestProbeLockReportsAnUnwritableStateDirectory(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	stateDir := filepath.Join(t.TempDir(), "udm-iptv")
	if err := os.Mkdir(stateDir, 0o500); err != nil {
		t.Fatal(err)
	}
	err := ProbeLock(stateDir)
	if !errors.Is(err, fs.ErrPermission) || !strings.Contains(err.Error(), "open operation lock") {
		t.Fatalf("probe of an unwritable directory: %v", err)
	}
}
