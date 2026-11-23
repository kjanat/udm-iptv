package diagnostics

import (
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/vishvananda/netlink"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/network"
	"github.com/kjanat/udm-iptv/internal/service"
)

var (
	errFixtureLeaseJSON   = errors.New("lease.json: malformed JSON")
	errFixtureDBus        = errors.New("dbus permission denied")
	errFixtureRouteDump   = errors.New("route dump interrupted")
	errFixtureAddressDump = errors.New("address dump interrupted")
)

func TestSnapshotRetainsConfiguredValues(t *testing.T) {
	t.Parallel()
	value := config.Default()
	value.WAN.VLANMAC = "02:11:22:33:44:55"
	value.WAN.StaticAddress = "192.0.2.7/24"
	value.WAN.DHCPOptions = []string{"-V", "IPTV_RG"}
	summary := summarizeConfig(value)
	value.WAN.DHCPOptions[1] = "changed"
	data, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	assertDiagnosticDetails(t, string(data), "02:11:22:33:44:55", "192.0.2.7/24", "IPTV_RG")
	assertDiagnosticDetails(t, RenderSnapshot(Snapshot{Config: summary}), "02:11:22:33:44:55", "192.0.2.7/24", "IPTV_RG")
}

func TestSnapshotRetainsCollectionFailuresWithoutAbsentClaims(t *testing.T) {
	t.Parallel()
	value := Snapshot{Network: networkStatus{Target: "iptv"}}
	recordCollectionError(&value.Errors, "lease", errFixtureLeaseJSON)
	recordCollectionError(&value.Service.Errors, "systemd", errFixtureDBus)
	recordCollectionError(&value.Network.Errors, "routes", errFixtureRouteDump)
	recordCollectionError(&value.Network.Errors, "addresses4", errFixtureAddressDump)
	text := RenderSnapshot(value)
	assertDiagnosticDetails(t, text, "lease.json: malformed JSON", "dbus permission denied", "route dump interrupted", "address dump interrupted", "DHCP lease: unavailable", "Routes: unavailable", "unavailable IPv4 addresses", "restarts: unavailable")
	for _, misleading := range []string{"none recorded", "Default route observed on iptv: none", "0 IPv4 addresses", "restarts: 0"} {
		if strings.Contains(text, misleading) {
			t.Fatalf("collection failure rendered as observed absence: %s", misleading)
		}
	}
}

func TestMissingDiagnosticInterfaceKeepsActualError(t *testing.T) {
	t.Parallel()
	value := inspectLink("diag-missing")
	if value.Errors["link"] == "" {
		t.Fatal("missing interface lost netlink error")
	}
	assertDiagnosticDetails(t, RenderSnapshot(Snapshot{Network: value}), value.Errors["link"], "Default route observed on diag-missing: unavailable")
}

func TestNATEvidenceDoesNotInventUnroutableDestinations(t *testing.T) {
	t.Parallel()
	observed := networkStatus{Errors: map[string]string{"routes": "netlink interrupted"}}
	if evidence := observedNATEvidence([]string{"192.0.2.0/24"}, observed, nil); evidence != nil {
		t.Fatalf("failed route read produced a negative reachability claim: %+v", evidence)
	}
}

type deniedSysfs struct{}

func (deniedSysfs) Open(name string) (fs.File, error) {
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrPermission}
}

func TestSysfsKeepsReadFailuresUnexpectedValuesAndTruncation(t *testing.T) {
	t.Parallel()
	const name = "class/net/br0/operstate"
	assertDiagnosticDetails(t, readSysfsToken(deniedSysfs{}, name, linkOperStates), "permission denied", name)
	if got := readSysfsToken(fstest.MapFS{}, name, linkOperStates); got != "" {
		t.Fatalf("absent optional sysfs attribute was treated as a read failure: %s", got)
	}
	oversized := fstest.MapFS{name: {Data: []byte(strings.Repeat("x", sysfsValueLimit+1))}}
	assertDiagnosticDetails(t, readSysfsToken(oversized, name, linkOperStates), "truncated", strings.Repeat("x", sysfsValueLimit))
	assertDiagnosticDetails(t, formatNativeProxy(true, "active", []int{42}, fs.ErrPermission), "extra proxy pids 42", "incomplete process scan", "permission denied")
}

func TestSnapshotRendersFullLeaseAndObservationTime(t *testing.T) {
	t.Parallel()
	stamp := time.Date(2026, 9, 23, 4, 5, 6, 123, time.UTC)
	address, err := netlink.ParseAddr("192.0.2.7/24")
	if err != nil {
		t.Fatal(err)
	}
	value := Snapshot{Timestamp: stamp, Service: serviceStatus{LoadState: "not-found"}, Lease: &service.LeaseState{
		Received: stamp, Applied: false, Failure: "route rejected", Lease: network.Lease{
			Interface: "iptv", Broadcast: "192.0.2.255", Metric: 37, ManagedAddresses: []netlink.Addr{*address}, ManagedRoutes: []netlink.Route{{LinkIndex: 8, Table: 123, Priority: 37}},
		},
	}}
	assertDiagnosticDetails(t, RenderSnapshot(value), stamp.Format(time.RFC3339Nano), "Service load state: not-found", "Lease interface: iptv, broadcast: 192.0.2.255, route metric: 37", "route rejected", `"Table":123`, `"LinkIndex":8`, "192.0.2.7")
}

func assertDiagnosticDetails(t *testing.T, text string, wanted ...string) {
	t.Helper()
	for _, value := range wanted {
		if !strings.Contains(text, value) {
			t.Fatalf("missing %q in diagnostics:\n%s", value, text)
		}
	}
}
