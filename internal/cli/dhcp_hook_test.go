package cli

import (
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/config/configtest"
)

func TestRecoverableDHCPHooksDoNotReportExceptions(t *testing.T) {
	for _, action := range []string{"leasefail", "nak"} {
		t.Run(action, func(t *testing.T) {
			capture := captureTelemetry(t)
			directory := t.TempDir()
			path := filepath.Join(directory, "config.json")
			value := configtest.Custom()
			value.Telemetry.Enabled, value.Telemetry.Errors, value.Telemetry.Logs = true, true, true
			if err := config.Save(path, value); err != nil {
				t.Fatal(err)
			}
			var output strings.Builder
			application := &Application{ConfigPath: path, StateDir: directory, Out: io.Discard, Err: &output}
			command := application.root()
			command.SetArgs([]string{"dhcp-hook", action})
			if err := command.Execute(); err != nil {
				t.Fatalf("recoverable hook failed: %v", err)
			}
			if !strings.Contains(output.String(), "client will retry") {
				t.Fatal("missing local retry warning")
			}
			reported := capture.output()
			if strings.Contains(reported, `"exception"`) || !strings.Contains(reported, "client will retry") {
				t.Fatalf("expected warning without exception: %s", reported)
			}
		})
	}
}
