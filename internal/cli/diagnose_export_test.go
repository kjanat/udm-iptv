package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/diagnostics"
	"github.com/kjanat/udm-iptv/internal/filemode"
	"github.com/kjanat/udm-iptv/internal/telemetry"
)

func TestDiagnoseExportWorksOfflineWithoutConfiguration(t *testing.T) {
	// Export must need only the capture: no tools, router or configured service.
	t.Setenv("PATH", t.TempDir())
	directory := t.TempDir()
	path := filepath.Join(directory, "private.jsonl")
	raw := []byte(`{"type":"initial","time":"2026-09-23T01:00:00Z","message":"password=secret-token","snapshot":{"network":{"target":"private-iptv","addresses":["192.168.10.152/24"]},"lease":{"lease":{"address":"192.168.10.152","options":{"credential":"secret-token"}}}}}` + "\n")
	if err := atomicfile.Write(path, raw, filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{formatJSONL, formatText} {
		t.Run(format, func(t *testing.T) {
			output := runOfflineExportCommand(t, path, format)
			assertExportCommandOutput(t, output, format)
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(after, raw) {
				t.Fatalf("export changed private capture: %v", err)
			}
		})
	}
}

func runOfflineExportCommand(t *testing.T, path, format string) string {
	t.Helper()
	directory := t.TempDir()
	var output, errorOutput bytes.Buffer
	application := &Application{
		ConfigPath: filepath.Join(directory, "absent-config.json"), StateDir: filepath.Join(directory, "absent-state"),
		Out: &output, Err: &errorOutput,
		networkIdentity: func(context.Context) telemetry.NetworkIdentity {
			t.Fatal("offline export requested network identity")
			return telemetry.NetworkIdentity{}
		},
	}
	command := application.root()
	command.SetArgs([]string{"diagnose", "export", path, "--format", format})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("offline export: %v; %s", err, errorOutput.String())
	}
	if application.monitor != nil {
		t.Fatal("offline export initialized telemetry")
	}
	for _, absent := range []string{application.ConfigPath, application.StateDir} {
		if _, err := os.Stat(absent); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("export created router state %s: %v", absent, err)
		}
	}
	return output.String()
}

func assertExportCommandOutput(t *testing.T, output, format string) {
	t.Helper()
	for _, private := range []string{"secret-token", "192.168.10.152", "private-iptv"} {
		if strings.Contains(output, private) {
			t.Fatalf("%s export leaked %q", format, private)
		}
	}
	if format == formatText {
		if !strings.Contains(output, "[sanitized export]") || !strings.Contains(output, "IPv4 aliases in original numerical order:") {
			t.Fatalf("text export missing privacy/election evidence: %s", output)
		}
		return
	}
	var event diagnostics.Event
	if err := json.Unmarshal([]byte(output), &event); err != nil {
		t.Fatal(err)
	}
	if event.Privacy != diagnostics.PrivacySanitized || event.Snapshot == nil || len(event.AddressOrder) != 1 {
		t.Fatalf("invalid sanitized event: %+v", event)
	}
}
