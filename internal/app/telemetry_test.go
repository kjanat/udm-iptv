package app

import (
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kjanat/udm-iptv/internal/config"
)

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

func TestMulticastCountersKeepOnlyNumericTotals(t *testing.T) {
	t.Parallel()
	routes, packets, err := multicastCounters(strings.NewReader("Group Origin Iif Pkts Bytes Wrong\n01020304 05060708 1 0019 1000 0\n02030405 06070809 1 12 400 0\n"))
	if err != nil || routes != 2 || packets != 31 {
		t.Fatalf("incorrect counters: %d, %d, %v", routes, packets, err)
	}
	if _, _, err := multicastCounters(strings.NewReader("header\nbroken row\n")); err == nil {
		t.Fatal("malformed counters treated as valid")
	}
}
