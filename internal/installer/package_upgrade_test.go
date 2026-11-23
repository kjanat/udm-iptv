package installer

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
)

func TestAptAllowsDowngradesOnlyWhenRequested(t *testing.T) {
	directory := t.TempDir()
	if err := atomicfile.Write(filepath.Join(directory, "apt-get"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	for _, force := range []bool{false, true} {
		var out bytes.Buffer
		if err := InstallPackage(t.Context(), "/tmp/udm-iptv.deb", force, &out, io.Discard); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "--allow-downgrades\n") != force {
			t.Fatalf("force=%t args=%q", force, out.String())
		}
		if !strings.HasSuffix(out.String(), "/tmp/udm-iptv.deb\n") {
			t.Fatalf("missing package: %q", out.String())
		}
	}
}
