package installer

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
)

func TestPurgeRemovesOwnedStateOnly(t *testing.T) {
	directory := t.TempDir()
	root, err := openStateDirectory(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	})
	for _, name := range []string{"bin", "runtime", "diagnostics"} {
		if err := root.Mkdir(name, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"bin/udm-iptv", "bin/udhcpc-hook", "config.json", "telemetry-research.json", "telemetry-research.lock", "telemetry-errors.rate"} {
		if err := root.WriteFile(name, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	external := filepath.Join(t.TempDir(), "config.json")
	if err := atomicfile.Write(external, []byte("external configuration"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeStateFiles(root, external, false); err != nil {
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
		if err := validateStatePath(path); err == nil {
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
	for _, keep := range []bool{false, true} {
		directory := t.TempDir()
		root, err := openStateDirectory(directory)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := root.Close(); err != nil {
				t.Error(err)
			}
		})
		for _, path := range []string{"bin", "runtime", "diagnostics"} {
			if err := root.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		for _, path := range []string{"bin/udm-iptv", "bin/udhcpc-hook", "bin/.udm-iptv.previous-example", "bin/unrelated", "runtime/proxy", "diagnostics/capture", "config.json", "custom.json", "telemetry-errors.rate", "notes.txt"} {
			if err := root.WriteFile(path, []byte("fixture"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := removeStateFiles(root, filepath.Join(directory, "custom.json"), keep); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"bin/unrelated", "notes.txt"} {
			if _, err := root.Stat(path); err != nil {
				t.Fatalf("unrelated file removed: %s: %v", path, err)
			}
		}
		for _, path := range []string{"bin/udm-iptv", "bin/udhcpc-hook", "bin/.udm-iptv.previous-example", "diagnostics"} {
			if _, err := root.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("installation entry remains: %s: %v", path, err)
			}
		}
		for _, path := range []string{"runtime", "config.json", "custom.json", "telemetry-errors.rate"} {
			_, err := root.Stat(path)
			if (err == nil) != keep {
				t.Fatalf("keep=%t path=%s: %v", keep, path, err)
			}
		}
	}
}

func TestStateCleanupCannotFollowExternalSymlinks(t *testing.T) {
	for _, entry := range []string{"bin", "runtime", "diagnostics"} {
		t.Run(entry, func(t *testing.T) {
			directory, outside := t.TempDir(), t.TempDir()
			sentinel := filepath.Join(outside, "udm-iptv")
			if err := atomicfile.Write(sentinel, []byte("unrelated"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(directory, entry)); err != nil {
				t.Fatal(err)
			}
			root, err := openStateDirectory(directory)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := root.Close(); err != nil {
					t.Error(err)
				}
			})
			err = removeStateFiles(root, "/external/config.json", false)
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
