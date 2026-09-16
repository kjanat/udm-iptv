package runtimebundle

import (
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
