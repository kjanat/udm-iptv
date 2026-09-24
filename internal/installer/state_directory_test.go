package installer

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
)

func stateRoot(t *testing.T, directory string) *os.Root {
	t.Helper()
	root, err := openStateDirectory(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	})

	return root
}

func populateState(t *testing.T, root *os.Root, directories, files []string) {
	t.Helper()
	for _, name := range directories {
		if err := root.Mkdir(name, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range files {
		if err := root.WriteFile(name, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func assertStatePresent(t *testing.T, root *os.Root, paths []string) {
	t.Helper()
	for _, path := range paths {
		if _, err := root.Stat(path); err != nil {
			t.Fatalf("preserved entry removed: %s: %v", path, err)
		}
	}
}

func assertStateAbsent(t *testing.T, root *os.Root, paths []string) {
	t.Helper()
	for _, path := range paths {
		if _, err := root.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("installation entry remains: %s: %v", path, err)
		}
	}
}

func TestPurgeRemovesOwnedStateOnly(t *testing.T) {
	directory := t.TempDir()
	root := stateRoot(t, directory)
	populateState(t, root,
		[]string{"bin", "runtime", "diagnostics", "sigstore", "sigstore/tuf"},
		[]string{"bin/udm-iptv", "bin/udhcpc-hook", "config.json", "telemetry-research.json", "telemetry-research.lock", "telemetry-errors.rate", "sigstore/tuf/root.json"})
	external := filepath.Join(t.TempDir(), "config.json")
	if err := atomicfile.Write(external, []byte("external configuration"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeStateFiles(root, external, UninstallOptions{}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("owned entries remain: %v: %v", entries, err)
	}
	data, err := os.ReadFile(external)
	if err != nil || string(data) != "external configuration" {
		t.Fatalf("external configuration changed: %v", err)
	}
}

func TestRejectUnsafeStatePaths(t *testing.T) {
	for _, path := range []string{"", ".", "relative", "/", "/data", "/tmp", "/etc", "/usr/local", "/var/lib", "/home/user", "/root", "/data/iptv/..", "/data//iptv"} {
		if err := ValidateStatePath(path); err == nil {
			t.Errorf("unsafe directory accepted: %q", path)
		}
		plan := testPlan()
		plan.StateDir = path
		if err := plan.Validate(); err == nil {
			t.Errorf("unsafe install plan accepted: %q", path)
		}
	}
}

func TestRejectSymlinkedStateDirectory(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "real")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "alias")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	root, err := openStateDirectory(link)
	if root != nil {
		if err := root.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err == nil {
		t.Fatal("symlinked state directory accepted")
	}
}

func TestRemoveStatePreservesUnrelatedFiles(t *testing.T) {
	owned := []string{"bin/udm-iptv", "bin/udhcpc-hook", "bin/.udm-iptv.previous-example", "diagnostics", "sigstore"}
	unrelated := []string{"bin/unrelated", "notes.txt"}
	configured := []string{"runtime", "config.json", "custom.json", "telemetry-errors.rate"}
	for _, test := range []struct {
		name               string
		keep               bool
		preserved, removed []string
	}{
		{"purge", false, unrelated, append(slices.Clone(owned), configured...)},
		{"keep-config", true, append(slices.Clone(unrelated), configured...), owned},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			root := stateRoot(t, directory)
			populateState(t, root,
				[]string{"bin", "runtime", "diagnostics", "sigstore", "sigstore/tuf"},
				[]string{"bin/udm-iptv", "bin/udhcpc-hook", "bin/.udm-iptv.previous-example", "bin/unrelated", "runtime/proxy", "diagnostics/capture", "sigstore/tuf/root.json", "config.json", "custom.json", "telemetry-errors.rate", "notes.txt"})
			if err := removeStateFiles(root, filepath.Join(directory, "custom.json"), UninstallOptions{KeepConfig: test.keep}); err != nil {
				t.Fatal(err)
			}
			assertStatePresent(t, root, test.preserved)
			assertStateAbsent(t, root, test.removed)
		})
	}
}

func TestStateCleanupCannotFollowExternalSymlinks(t *testing.T) {
	for _, entry := range []string{"bin", "runtime", "diagnostics", "sigstore"} {
		t.Run(entry, func(t *testing.T) {
			directory, outside := t.TempDir(), t.TempDir()
			sentinel := filepath.Join(outside, "udm-iptv")
			if err := atomicfile.Write(sentinel, []byte("unrelated"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(directory, entry)); err != nil {
				t.Fatal(err)
			}
			root := stateRoot(t, directory)
			err := removeStateFiles(root, "/external/config.json", UninstallOptions{})
			if entry == "bin" && err == nil {
				t.Fatal("external bin link accepted")
			}
			data, err := os.ReadFile(sentinel)
			if err != nil || string(data) != "unrelated" {
				t.Fatalf("external file changed: %v", err)
			}
		})
	}
}

func TestPackageCleanupLeavesDpkgBinary(t *testing.T) {
	t.Parallel()
	for _, keep := range []bool{false, true} {
		directory := t.TempDir()
		root := stateRoot(t, directory)
		populateState(t, root, []string{"bin"}, []string{"bin/udm-iptv", "bin/udhcpc-hook", "config.json", lockName})
		remover := uninstaller{
			root: root, stateDir: directory, configPath: filepath.Join(directory, "config.json"),
			options: UninstallOptions{KeepConfig: keep, FromPackage: true},
		}
		if err := remover.removeInstallationState(t.Context()); err != nil {
			t.Fatal(err)
		}
		assertStatePresent(t, root, []string{"bin/udm-iptv", lockName})
		assertStateAbsent(t, root, []string{"bin/udhcpc-hook"})
		if keep {
			assertStatePresent(t, root, []string{"config.json"})
		} else {
			assertStateAbsent(t, root, []string{"config.json"})
		}
	}
}
