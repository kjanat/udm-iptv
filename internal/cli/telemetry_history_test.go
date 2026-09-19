package cli

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/telemetry"
)

func envelopeContains(output, token string) bool {
	return strings.Contains(output, token) || strings.Contains(output, strings.ReplaceAll(token, `"`, `\"`))
}

type firstConfigurationCase struct {
	name        string
	args        []string
	wantReport  bool
	wantLookups int
}

func TestFirstConfigurationResearchAndOptOut(t *testing.T) {
	for _, testCase := range []firstConfigurationCase{
		{name: "enabled", args: nil, wantReport: true, wantLookups: 1},
		{name: "disabled", args: []string{"--telemetry=false"}, wantReport: false, wantLookups: 0},
		{name: "no-network", args: []string{"--telemetry-network-identity=false"}, wantReport: true, wantLookups: 0},
		{name: "no-presets", args: []string{"--telemetry-presets=false"}, wantReport: false, wantLookups: 0},
	} {
		t.Run(testCase.name, func(t *testing.T) { runFirstConfigurationCase(t, testCase) })
	}
}

func runFirstConfigurationCase(t *testing.T, testCase firstConfigurationCase) {
	t.Helper()
	capture := captureTelemetry(t)
	directory := t.TempDir()
	lookups := 0
	application := &Application{
		ConfigPath: filepath.Join(directory, "config.json"), StateDir: directory, Out: io.Discard, Err: io.Discard, seed: seedBR0,
		networkIdentity: func(context.Context) telemetry.NetworkIdentity {
			lookups++

			return telemetry.NetworkIdentity{IP: "11.22.33.44", PTR: "example.kpn.net."}
		},
	}
	root := application.root()
	root.SetArgs(append([]string{"configure", "set", "--profile=kpn"}, testCase.args...))
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	output := capture.output()
	if strings.Contains(output, "installation configuration") != testCase.wantReport {
		t.Fatal("first configuration reporting mismatch")
	}
	if lookups != testCase.wantLookups {
		t.Fatalf("network lookup preference ignored: %d lookups", lookups)
	}
	if testCase.wantReport && (!envelopeContains(output, `"applied":false`) || !envelopeContains(output, `"revision":1`)) {
		t.Fatal("uninstalled configuration marked applied")
	}
	value, err := config.Load(application.ConfigPath)
	if err != nil || value.Profile != "kpn" {
		t.Fatal("reporting broke configuration")
	}
}
