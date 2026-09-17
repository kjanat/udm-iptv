package config

import (
	"path/filepath"
	"testing"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
)

func TestTelemetryDefaultsAndExistingChoice(t *testing.T) {
	value := Default()
	if !value.Telemetry.Enabled {
		t.Fatal("new configuration must enable telemetry")
	}
	value.Telemetry.Enabled = false
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, value); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Telemetry.Enabled {
		t.Fatal("existing opt-out lost")
	}
	selected, err := FromProfile("tweak", loaded)
	if err != nil || selected.Telemetry.Enabled {
		t.Fatal("profile switch lost opt-out")
	}
}

func TestMigratedConfigurationKeepsDefaultTelemetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.conf")
	// The packaging always wrote IPTV_WAN_RANGES, and igmpproxy needs a source
	// prefix, so a realistic legacy file carries one.
	legacy := "IPTV_WAN_INTERFACE=eth8\nIPTV_WAN_RANGES=213.75.0.0/16\n"
	if err := atomicfile.Write(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := ImportLegacy(path)
	if err != nil {
		t.Fatal(err)
	}
	if !value.Telemetry.Enabled {
		t.Fatal("migration disabled telemetry")
	}
}
