package cli

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/config"
)

func TestDiagnoseRetainsRecentLogsAtRequestedVerbosity(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("PATH", directory)
	const script = `#!/bin/sh
next=0
count=missing
for arg do
  if [ "$next" = 1 ]; then count=$arg; next=0; fi
  if [ "$arg" = -n ]; then next=1; fi
done
printf '{"MESSAGE":"requested %s records","_SYSTEMD_UNIT":"udm-iptv-restore.service"}\n' "$count"
`
	if err := atomicfile.Write(filepath.Join(directory, "journalctl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "config.json")
	settings := config.DefaultKPN()
	settings.WAN.Interface = "test-wan"
	settings.LAN.Interfaces = []string{"br0"}
	settings.Telemetry.Enabled = false
	if err := config.Save(path, settings); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{formatText, formatJSONL} {
		for verbosity, count := range map[string]string{"normal": "100", "debug": "300"} {
			t.Run(format+"/"+verbosity, func(t *testing.T) {
				var output bytes.Buffer
				application := &Application{ConfigPath: path, StateDir: directory, Out: &output, Err: io.Discard}
				command := application.root()
				command.SetArgs([]string{"diagnose", "--format", format, "--verbosity", verbosity})
				if err := command.ExecuteContext(t.Context()); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(output.String(), "requested "+count+" records") || !strings.Contains(output.String(), "udm-iptv-restore.service") {
					t.Fatalf("one-shot diagnostic output lost requested journal evidence: %s", output.String())
				}
			})
		}
	}
}
