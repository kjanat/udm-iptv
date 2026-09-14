package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/google/go-github/v80/github"
	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/vishvananda/netlink"
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

func TestDiagnosticSanitizerRetainsObservedAndConfiguredAddresses(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "config.json")
	value := config.Default()
	value.WAN.StaticAddress = "198.51.100.10/24"
	if err := config.Save(path, value); err != nil {
		t.Fatal(err)
	}
	sanitizer := newDiagnosticSanitizer(path)
	sanitizer.observe([]string{"203.0.113.20"})
	sanitizer.observe([]string{"203.0.113.21"})
	output := sanitizer.sanitize("configured 198.51.100.10 old 203.0.113.20 current 203.0.113.21 provider 195.121.94.212")
	for _, secret := range []string{"198.51.100.10", "203.0.113.20", "203.0.113.21"} {
		if strings.Contains(output, secret) {
			t.Errorf("%q was not redacted: %s", secret, output)
		}
	}
	if !strings.Contains(output, "195.121.94.212") {
		t.Fatalf("unobserved provider address was removed: %s", output)
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

func TestReleaseAssetsUseAuthenticatedAPIURLs(t *testing.T) {
	t.Parallel()
	release := &github.RepositoryRelease{Assets: []*github.ReleaseAsset{
		{Name: github.Ptr("udm-iptv-linux-arm64"), URL: github.Ptr("https://api.github.com/repos/kjanat/udm-iptv/releases/assets/1"), BrowserDownloadURL: github.Ptr("https://github.com/kjanat/udm-iptv/releases/download/v1/udm-iptv-linux-arm64")},
		{Name: github.Ptr("SHA256SUMS"), URL: github.Ptr("https://api.github.com/repos/kjanat/udm-iptv/releases/assets/2"), BrowserDownloadURL: github.Ptr("https://github.com/kjanat/udm-iptv/releases/download/v1/SHA256SUMS")},
	}}
	binary, checksums := releaseAssetURLs(release, "udm-iptv-linux-arm64")
	if binary != release.Assets[0].GetURL() || checksums != release.Assets[1].GetURL() {
		t.Fatalf("release asset URLs = %q, %q", binary, checksums)
	}
}

func TestReleaseAssetDownloadScopesAuthentication(t *testing.T) {
	t.Parallel()
	var requests []*http.Request
	client := &http.Client{Transport: &bearerTransport{
		token: "test-token",
		base: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			requests = append(requests, request)
			return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader("asset")), Header: make(http.Header), Request: request}, nil
		}),
	}}
	target := filepath.Join(t.TempDir(), "asset")
	if err := download(context.Background(), client, "https://api.github.com/repos/kjanat/udm-iptv/releases/assets/1", target, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := requests[0].Header.Get("Authorization"); got != "Bearer test-token" {
		t.Fatalf("API authorization header = %q", got)
	}
	if got := requests[0].Header.Get("Accept"); got != "application/octet-stream" {
		t.Fatalf("API accept header = %q", got)
	}
	unrelated, _ := http.NewRequest(http.MethodGet, "https://example.com/asset", nil)
	if _, err := client.Transport.RoundTrip(unrelated); err != nil {
		t.Fatal(err)
	}
	if got := requests[1].Header.Get("Authorization"); got != "" {
		t.Fatalf("token leaked to unrelated host: %q", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
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

func TestCaptureModelRecognizesTimeout(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "capture.txt")
	if err := os.WriteFile(path, []byte("Capture timed out: collector stalled\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	model := newCaptureModel(path, time.Time{}, 0)
	updated, _ := model.Update(captureTick(time.Now()))
	result := updated.(captureModel)
	if !result.failed {
		t.Fatal("timed-out capture was not recognized")
	}
}

func TestCollectorsShareCaptureDeadline(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	release := make(chan struct{})
	started := time.Now()
	_, err := collectWithin(ctx, func() (string, error) {
		<-release
		return "late", nil
	})
	close(release)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("collector error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("collector exceeded shared deadline by %s", elapsed)
	}
}

func TestJournalCollectionIsBoundedAtSource(t *testing.T) {
	t.Parallel()
	arguments := journalArguments("s=cursor", 10_000)
	want := []string{"-n", "10000", "--after-cursor", "s=cursor"}
	for _, value := range want {
		if !slices.Contains(arguments, value) {
			t.Fatalf("journal arguments %q do not contain %q", arguments, value)
		}
	}
}

func TestDHCPReadinessRequiresIPv4(t *testing.T) {
	t.Parallel()
	if hasIPv4Address([]net.Addr{&net.IPNet{IP: net.ParseIP("fe80::1"), Mask: net.CIDRMask(64, 128)}}) {
		t.Fatal("link-local IPv6 address satisfied DHCP readiness")
	}
	if !hasIPv4Address([]net.Addr{&net.IPNet{IP: net.ParseIP("10.0.0.2"), Mask: net.CIDRMask(24, 32)}}) {
		t.Fatal("DHCP-assigned IPv4 address did not satisfy readiness")
	}
}

func TestStaticAddressDeletionRecognition(t *testing.T) {
	t.Parallel()
	deleted := netlink.AddrUpdate{
		LinkAddress: net.IPNet{IP: net.ParseIP("10.20.30.1"), Mask: net.CIDRMask(24, 32)},
		LinkIndex:   8,
	}
	if !staticAddressDeleted("10.20.30.1/24", 8, deleted) {
		t.Fatal("configured static address deletion was not recognized")
	}
	for name, update := range map[string]netlink.AddrUpdate{
		"addition":      {LinkAddress: deleted.LinkAddress, LinkIndex: 8, NewAddr: true},
		"other link":    {LinkAddress: deleted.LinkAddress, LinkIndex: 9},
		"other address": {LinkAddress: net.IPNet{IP: net.ParseIP("10.20.30.2"), Mask: net.CIDRMask(24, 32)}, LinkIndex: 8},
		"other prefix":  {LinkAddress: net.IPNet{IP: net.ParseIP("10.20.30.1"), Mask: net.CIDRMask(32, 32)}, LinkIndex: 8},
	} {
		if staticAddressDeleted("10.20.30.1/24", 8, update) {
			t.Errorf("%s was treated as the configured address deletion", name)
		}
	}
}

func TestParseUintHandlesSystemdRestartCounter(t *testing.T) {
	t.Parallel()
	if got := parseUint(uint32(7)); got != 7 {
		t.Fatalf("parseUint(uint32(7)) = %d", got)
	}
}

func TestWANInterfaceForBoard(t *testing.T) {
	t.Parallel()
	for board, want := range map[string][]string{
		"UDM": {"eth4"}, "udmpro": {"eth8", "eth9"}, "UDMPROSE": {"eth8", "eth9"},
		"UDR7": {"eth3", "eth4", "eth2"}, "UCGF": {"eth6", "eth4"},
	} {
		if got := wanInterfacesForBoard(board); !slices.Equal(got, want) {
			t.Errorf("wanInterfacesForBoard(%q) = %q, want %q", board, got, want)
		}
	}
}

func TestDefaultRouteInterfacePrefersLowestMetric(t *testing.T) {
	t.Parallel()
	routes := `Iface Destination Gateway Flags RefCnt Use Metric Mask
eth8 00000000 0100000A 0003 0 0 200 00000000
eth9 00000000 0100000A 0003 0 0 100 00000000
`
	if got := defaultRouteInterface(strings.NewReader(routes)); got != "eth9" {
		t.Fatalf("default route interface = %q, want eth9", got)
	}
}

func TestUXGDownstreamInterfacesIncludeSubinterfaces(t *testing.T) {
	t.Parallel()
	interfaces := []net.Interface{{Name: "eth0"}, {Name: "eth0.10"}, {Name: "br0"}, {Name: "eth8"}}
	if got, want := selectDownstreamInterfaces("UXG", interfaces), []string{"br0", "eth0.10"}; !slices.Equal(got, want) {
		t.Fatalf("UXG downstream interfaces = %q, want %q", got, want)
	}
	if got, want := selectDownstreamInterfaces("UDMPRO", interfaces), []string{"br0"}; !slices.Equal(got, want) {
		t.Fatalf("UDM Pro downstream interfaces = %q, want %q", got, want)
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
	unit := systemdUnit("/custom state/bin/udm-iptv", "/custom state/config.json", "/custom state")
	for _, expected := range []string{
		"Type=notify", "NotifyAccess=main", "TimeoutStartSec=45s",
		`Environment="UDM_IPTV_STATE_DIR=/custom state"`,
		`ExecStart="/custom state/bin/udm-iptv" daemon --config "/custom state/config.json"`,
	} {
		if !strings.Contains(unit, expected) {
			t.Errorf("unit does not contain %q:\n%s", expected, unit)
		}
	}
}

func TestNoSuchUnitRecognition(t *testing.T) {
	t.Parallel()
	err := dbus.NewError("org.freedesktop.systemd1.NoSuchUnit", []any{"missing"})
	if !noSuchUnit(err) {
		t.Fatal("systemd NoSuchUnit error was not recognized")
	}
	if noSuchUnit(errors.New("stop failed")) {
		t.Fatal("ordinary stop failure was treated as a missing unit")
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

func TestExistingInvalidLegacyConfigurationIsNotIgnored(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	legacy := filepath.Join(directory, "legacy.conf")
	if err := os.WriteFile(legacy, []byte(`IPTV_WAN_INTERFACE="not a valid interface name"`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := importFirstLegacy([]string{filepath.Join(directory, "missing.conf"), legacy}); err == nil {
		t.Fatal("invalid existing legacy configuration was silently ignored")
	}
}
