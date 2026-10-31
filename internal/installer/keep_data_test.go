package installer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKeepDataPreservesTheEntireStateDirectory(t *testing.T) {
	t.Parallel()
	for _, fromPackage := range []bool{false, true} {
		directory := t.TempDir()
		root := stateRoot(t, directory)
		files := []string{
			"config.json", "custom.json", "udm-iptv.conf", "udm-iptv.deb", "telemetry-errors.rate", lockName,
			"bin/udm-iptv", "bin/udhcpc-hook", "bin/.udm-iptv.previous-example",
			"runtime/lease.json", "diagnostics/capture.jsonl", "sigstore/tuf/root.json", "notes.txt",
		}
		populateState(t, root, []string{"bin", "runtime", "diagnostics", "sigstore", "sigstore/tuf", "empty"}, files)
		before := make(map[string]string, len(files))
		for _, name := range files {
			data, err := root.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			before[name] = string(data)
		}
		remover := uninstaller{
			root: root, stateDir: directory, configPath: filepath.Join(directory, "custom.json"),
			options: UninstallOptions{KeepData: true, FromPackage: fromPackage},
		}
		if err := remover.removeInstallationState(t.Context()); err != nil {
			t.Fatal(err)
		}
		assertStatePresent(t, root, files)
		assertStatePresent(t, root, []string{"empty"})
		for name, expected := range before {
			data, err := root.ReadFile(name)
			if err != nil || string(data) != expected {
				t.Fatalf("retained state %s changed: %v", name, err)
			}
		}
	}
}

func TestKeepDataPreservesAnEmptyStateDirectory(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	root := stateRoot(t, directory)
	remover := uninstaller{root: root, stateDir: directory, options: UninstallOptions{KeepData: true}}
	if err := remover.removeInstallationState(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(directory); err != nil {
		t.Fatalf("keep-data removed the state directory: %v", err)
	}
}
