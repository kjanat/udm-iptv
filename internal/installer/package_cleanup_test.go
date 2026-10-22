package installer

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the shipped maintainer script against an isolated state directory,
// using the Go ownership list as the fixtures so the two cleanups cannot drift.
func packageCleanupScript(t *testing.T, directory string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "packaging", "deb", "postremove"))
	if err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(string(data), "/data/udm-iptv", directory)
	return strings.ReplaceAll(script, "/usr/share/debconf/confmodule", filepath.Join(directory, "absent-debconf"))
}

func TestPackagePurgeMatchesOwnedStateAndPreservesLock(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	root := stateRoot(t, directory)
	files := ownedStateFiles(root, filepath.Join(directory, "config.json"), UninstallOptions{FromPackage: true})
	populateState(t, root, []string{"bin"}, files)
	populateState(t, root, ownedStateDirectories(false), []string{"bin/unrelated", "notes.txt", "bin/.udm-iptv.previous-test"})
	release, err := AcquireLock(directory)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(filepath.Join(directory, lockName))
	if err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), "sh", "-c", packageCleanupScript(t, directory), "postremove", "purge")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("package purge: %v\n%s", err, output)
	}
	assertStateAbsent(t, root, files)
	assertStateAbsent(t, root, append(ownedStateDirectories(false), "bin/.udm-iptv.previous-test"))
	assertStatePresent(t, root, []string{"bin/unrelated", "notes.txt", lockName})
	after, err := os.Stat(filepath.Join(directory, lockName))
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("purge replaced the operation lock inode: %v", err)
	}
}

func TestPackagePurgeRefusesConcurrentLifecycleOperation(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	root := stateRoot(t, directory)
	populateState(t, root, nil, []string{"config.json"})
	release, err := AcquireLock(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := release(); err != nil {
			t.Error(err)
		}
	}()
	command := exec.CommandContext(t.Context(), "sh", "-c", packageCleanupScript(t, directory), "postremove", "purge")
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("purge ran while lifecycle operation held the lock: %s", output)
	}
	assertStatePresent(t, root, []string{"config.json"})
}

func TestPackageRemovalRetainsConfiguration(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	root := stateRoot(t, directory)
	populateState(t, root, nil, []string{"config.json"})
	command := exec.CommandContext(t.Context(), "sh", "-c", packageCleanupScript(t, directory), "postremove", "remove")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("package removal: %v\n%s", err, output)
	}
	assertStatePresent(t, root, []string{"config.json"})
	if _, err := root.Stat(lockName); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remove unexpectedly entered purge cleanup: %v", err)
	}
}
