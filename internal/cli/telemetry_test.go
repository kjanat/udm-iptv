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

type telemetrySelection struct {
	enabled   bool
	logs      bool
	traceRate float64
}

func telemetrySelectionOf(value config.Telemetry) telemetrySelection {
	return telemetrySelection{enabled: value.Enabled, logs: value.Logs, traceRate: value.TraceRate}
}

func TestTelemetryConfigurationIsOptInAndPreservesSelection(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := config.Save(path, config.Default()); err != nil {
		t.Fatal(err)
	}
	application := &Application{ConfigPath: path, StateDir: dir, Out: io.Discard, Err: io.Discard}
	for _, testCase := range []struct {
		name string
		args []string
		want telemetrySelection
	}{
		{
			name: "product-flags",
			args: []string{"configure", "--non-interactive", "--telemetry", "--telemetry-logs=false", "--telemetry-trace-rate=1"},
			want: telemetrySelection{enabled: true, logs: false, traceRate: 1},
		},
		{
			name: "profile-keeps-selection",
			args: []string{"configure", "--non-interactive", "--profile=kpn"},
			want: telemetrySelection{enabled: true, logs: false, traceRate: 1},
		},
		{
			name: "opt-out",
			args: []string{"configure", "--non-interactive", "--telemetry=false"},
			want: telemetrySelection{enabled: false, logs: false, traceRate: 1},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			command := application.root()
			command.SetArgs(testCase.args)
			if err := command.Execute(); err != nil {
				t.Fatal(err)
			}
			value, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := telemetrySelectionOf(value.Telemetry); got != testCase.want {
				t.Fatalf("telemetry choices lost: %+v", value.Telemetry)
			}
		})
	}
}
