package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/firmware"
)

func TestUnknownTrackFailsBeforeDiscovery(t *testing.T) {
	root := command()
	root.SetArgs([]string{"catalog", "--track", "unknown"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "unknown firmware track") {
		t.Fatalf("error=%v", err)
	}
}

func TestCommandDefaultsAreIndependent(t *testing.T) {
	t.Setenv("MODEL", "udmpro")
	root := command()
	catalog, _, err := root.Find([]string{"catalog"})
	if err != nil {
		t.Fatal(err)
	}
	model, err := catalog.Flags().GetString("model")
	if err != nil || model != "all" {
		t.Fatalf("catalog default overwritten: %s %v", model, err)
	}
	build, _, err := root.Find([]string{"build"})
	if err != nil {
		t.Fatal(err)
	}
	model, err = build.Flags().GetString("model")
	if err != nil || model != "udmpro" {
		t.Fatalf("build default: %s %v", model, err)
	}
}

func TestMatrixOutput(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "output")
	if err := atomicfile.Write(filename, []byte("existing=preserved\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := writeMatrix(&stdout, filename, firmware.Matrix{Include: []firmware.Pair{{Model: "udmpro"}}}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(content), "existing=preserved\nmatrix={") || stdout.Len() != 0 {
		t.Fatal("incorrect GitHub output")
	}
}
