package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kjanat/udm-iptv/internal/config"
)

func TestProxyConfigurationKeepsNATOutOfImproxy(t *testing.T) {
	t.Parallel()
	value := config.Default()
	value.WAN.NATDestinations = []string{"213.75.0.0/16"}
	value.Proxy.SourceRanges = []string{"198.51.100.0/24"}
	output, err := renderProxyConfig(value)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output, "213.75.0.0/16") || strings.Contains(output, "198.51.100.0/24") {
		t.Fatalf("improxy config contains source or NAT ranges:\n%s", output)
	}
}

func TestSanitizeCommonIdentifiers(t *testing.T) {
	t.Parallel()
	input := "private 192.168.10.1 fe80::1 AA-BB-CC-DD-EE-FF aabb.ccdd.eeff public 195.121.94.212"
	output := sanitize(input)
	for _, secret := range []string{"192.168.10.1", "fe80::1", "AA-BB-CC-DD-EE-FF", "aabb.ccdd.eeff"} {
		if strings.Contains(output, secret) {
			t.Errorf("%q was not redacted: %s", secret, output)
		}
	}
	if !strings.Contains(output, "195.121.94.212") {
		t.Fatalf("public provider address was removed: %s", output)
	}
}

func TestVerifyChecksum(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	binary := filepath.Join(directory, "udm-iptv-linux-arm64")
	checksums := filepath.Join(directory, "SHA256SUMS")
	if err := os.WriteFile(binary, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(checksums, []byte("9a3a45d01531a20e89ac6ae10b0b0beb0492acd7216a368aa062d1a5fecaf9cd  udm-iptv-linux-arm64\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyChecksum(binary, checksums, "udm-iptv-linux-arm64"); err != nil {
		t.Fatal(err)
	}
}

func TestRenderCompletedEvent(t *testing.T) {
	t.Parallel()
	output := renderEvent(diagnosticEvent{Type: "completed", Message: "Capture reached its deadline."})
	if !strings.Contains(output, "Capture completed") {
		t.Fatalf("unexpected completion output: %q", output)
	}
}

func TestCaptureCompletion(t *testing.T) {
	t.Parallel()
	want := time.Date(2026, time.September, 14, 13, 15, 6, 0, time.UTC)
	got := captureCompletion("Capture started; expected completion 2026-09-14T13:15:06Z\n")
	if !got.Equal(want) {
		t.Fatalf("captureCompletion() = %s, want %s", got, want)
	}
	jsonValue := captureCompletion(`{"message":"Capture started; expected completion 2026-09-14T13:15:06Z"}`)
	if !jsonValue.Equal(want) {
		t.Fatalf("captureCompletion(JSON) = %s, want %s", jsonValue, want)
	}
}

func TestCaptureModelRecognizesFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "capture.txt")
	if err := os.WriteFile(path, []byte("Capture failed: journal unavailable\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	model := newCaptureModel(path, time.Time{}, 0)
	updated, _ := model.Update(captureTick(time.Now()))
	result := updated.(captureModel)
	if !result.failed {
		t.Fatal("failed capture was not recognized")
	}
}

func TestWANInterfaceForBoard(t *testing.T) {
	t.Parallel()
	for board, want := range map[string]string{
		"UDM": "eth4", "udmpro": "eth8", "UDMPROSE": "eth8", "UDR7": "eth3", "UCGF": "eth6",
	} {
		if got := wanInterfaceForBoard(board); got != want {
			t.Errorf("wanInterfaceForBoard(%q) = %q, want %q", board, got, want)
		}
	}
}

func TestBoardInterfacePreservesProfileSuffix(t *testing.T) {
	t.Parallel()
	value := config.Default()
	value.WAN.Interface = "eth8.35"
	got := withBoardInterface(value, "UDM")
	if got.WAN.Interface != "eth4.35" {
		t.Fatalf("WAN interface = %q, want eth4.35", got.WAN.Interface)
	}
}

func TestSystemdUnitWaitsForNativeReadiness(t *testing.T) {
	t.Parallel()
	unit := systemdUnit("/data/udm-iptv/bin/udm-iptv", "/data/udm-iptv/config.json")
	for _, expected := range []string{"Type=notify", "NotifyAccess=main", "TimeoutStartSec=45s", "ExecStart=/data/udm-iptv/bin/udm-iptv daemon --config /data/udm-iptv/config.json"} {
		if !strings.Contains(unit, expected) {
			t.Errorf("unit does not contain %q:\n%s", expected, unit)
		}
	}
}

func TestNonInteractiveConfigurationAppliesFlagsAfterLoading(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "config.json")
	current := config.Default()
	current.WAN.Interface = "eth8"
	if err := config.Save(path, current); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	application := &Application{Version: "test", ConfigPath: path, StateDir: filepath.Join(directory, "state"), Out: &output, Err: &output}
	command := application.root()
	command.SetArgs([]string{"configure", "--non-interactive", "--wan-interface", "eth9", "--quickleave=true"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	updated, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if updated.WAN.Interface != "eth9" || !updated.Proxy.QuickLeave {
		t.Fatalf("flags were not applied: %#v", updated)
	}
}

func TestNonInteractiveConfigurationRejectsUnknownProfile(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	application := &Application{Version: "test", ConfigPath: filepath.Join(directory, "config.json"), StateDir: filepath.Join(directory, "state"), Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	command := application.root()
	command.SetArgs([]string{"configure", "--non-interactive", "--profile", "missing"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "unknown provider profile") {
		t.Fatalf("unexpected error: %v", err)
	}
}
