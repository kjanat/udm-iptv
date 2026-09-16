package cli

import (
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kjanat/udm-iptv/internal/config"
)

func TestHelpCopy(t *testing.T) {
	var out strings.Builder
	application := &Application{Out: &out, Err: &out}
	root := application.root()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--help"})
	err := root.Execute()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "telemetry") || strings.Contains(out.String(), "Sentry") {
		t.Fatal("unexpected root help text")
	}
	out.Reset()
	root = application.root()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"configure", "--help"})
	err = root.Execute()
	if err != nil {
		t.Fatal(err)
	}
	help := out.String()
	if !strings.Contains(help, "--telemetry") {
		t.Fatal("configure help missing --telemetry")
	}
	if strings.Contains(help, "telemetry-errors") || strings.Contains(help, "Sentry") {
		t.Fatal("unexpected configure help text")
	}
}

func TestTelemetryConfigurationIsOptInAndPreservesSelection(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := config.Save(path, config.Default()); err != nil {
		t.Fatal(err)
	}
	application := &Application{ConfigPath: path, StateDir: dir, Out: io.Discard, Err: io.Discard}
	for _, args := range [][]string{
		{"configure", "--non-interactive", "--telemetry", "--telemetry-logs=false", "--telemetry-trace-rate=1"},
		{"configure", "--non-interactive", "--profile=kpn"},
	} {
		command := application.root()
		command.SetArgs(args)
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
		value, err := config.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if !value.Telemetry.Enabled || value.Telemetry.Logs || value.Telemetry.TraceRate != 1 {
			t.Fatalf("telemetry choices lost: %+v", value.Telemetry)
		}
	}
	command := application.root()
	command.SetArgs([]string{"configure", "--non-interactive", "--telemetry=false"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	value, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if value.Telemetry.Enabled {
		t.Fatal("opt-out was not saved")
	}
}
