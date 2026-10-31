package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/telemetry"
)

func TestSavedConfigurationTelemetryDropStaysOffStderr(t *testing.T) {
	capture := captureTelemetry(t)
	directory := t.TempDir()
	now := time.Now().Unix()
	// Exhaust the shared presets budget before this command saves its config.
	rate := fmt.Sprintf("%d 0 %d 600\n", now/60, now/86400)
	if err := atomicfile.Write(filepath.Join(directory, "telemetry-presets.rate"), []byte(rate), 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	application := &Application{
		ConfigPath: filepath.Join(directory, "config.json"), StateDir: directory,
		Out: io.Discard, Err: &stderr, seed: seedBR0,
		networkIdentity: func(context.Context) telemetry.NetworkIdentity { return telemetry.NetworkIdentity{} },
	}
	root := application.root()
	root.SetArgs([]string{"configure", "set", "--profile=kpn"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("telemetry drop wrote to stderr: %s", stderr.String())
	}
	if strings.Contains(capture.output(), "installation configuration") {
		t.Fatal("exhausted budget sent a configuration report")
	}
	value, err := config.Load(application.ConfigPath)
	if err != nil || value.Profile != "kpn" {
		t.Fatal("telemetry drop prevented the configuration save")
	}
}

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
