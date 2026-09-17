package runtimebundle

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
)

func TestRuntimeRejectsInvalidInputs(t *testing.T) {
	root := t.TempDir()
	err := Preserve(root, "../../oops", filepath.Join(root, "unused"))
	if err == nil {
		t.Fatal("invalid program")
	}
	if _, _, err := Command(root, "improxy", nil); err == nil {
		t.Fatal("missing runtime accepted")
	}
	file := filepath.Join(root, "invalid")
	err = atomicfile.Write(file, []byte("not an ELF binary"), 0o700)
	if err != nil {
		t.Fatal(err)
	}
	err = Preserve(root, "improxy", file)
	if err == nil {
		t.Fatal("invalid executable")
	}
}

func TestPruneGenerationsKeepsLinkedRuntimes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, name := range []string{"genA", "genB", "genC", ".bundle-staging"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("genA", filepath.Join(root, "improxy")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("genB", filepath.Join(root, "igmpproxy")); err != nil {
		t.Fatal(err)
	}
	if err := pruneGenerations(root); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"genA", "genB", ".bundle-staging"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Errorf("%s was removed: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "genC")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unreferenced generation survived: %v", err)
	}
}

func TestPruneGenerationsWithoutEveryProxyLink(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, name := range []string{"genA", "genB"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("genA", filepath.Join(root, "improxy")); err != nil {
		t.Fatal(err)
	}
	if err := pruneGenerations(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "genA")); err != nil {
		t.Fatalf("linked generation removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "genB")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unreferenced generation survived: %v", err)
	}
}
