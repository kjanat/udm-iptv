package diagnostics

import (
	"encoding/json"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kjanat/udm-iptv/internal/mroute"
	"github.com/kjanat/udm-iptv/internal/network"
	"github.com/kjanat/udm-iptv/internal/service"
)

func TestReadableDiagnosticsIncludeProxySettings(t *testing.T) {
	t.Parallel()
	for _, proxy := range []string{"improxy", "igmpproxy"} {
		for _, enabled := range []bool{true, false} {
			value := Snapshot{Config: configSummary{Proxy: proxy, IGMPVersion: 3, QuickLeave: enabled, Debug: enabled}, Service: serviceStatus{Proxy: proxy}}
			output := RenderSnapshot(value)
			want := "IGMP version: 3, quickleave enabled: false, proxy debug logging: false"
			if enabled {
				want = "IGMP version: 3, quickleave enabled: true, proxy debug logging: true"
			}
			if !strings.Contains(output, want) {
				t.Fatalf("missing proxy settings: %s", output)
			}
			for _, kind := range []string{"initial", "final"} {
				if !strings.Contains(RenderEvent(Event{Type: kind, Snapshot: &value}), want) {
					t.Fatalf("%s capture Snapshot lost proxy settings", kind)
				}
			}
		}
	}
}

func TestRenderSnapshotSeparatesUnreadableCountersFromZero(t *testing.T) {
	t.Parallel()
	idle, none := MulticastInfo{}, []network.NATRule{}
	readable := RenderSnapshot(Snapshot{Multicast: &idle, NAT: &none})
	for _, want := range []string{"Active NAT rules: 0", "Multicast routes: 0 forwarding (0 packets, 0 B), 0 unresolved"} {
		if !strings.Contains(readable, want) {
			t.Fatalf("readable snapshot missing %q: %s", want, readable)
		}
	}
	if strings.Contains(readable, "NAT rules on the IPTV interface") || strings.Contains(readable, "NAT evidence") {
		t.Fatalf("empty NAT sections rendered: %s", readable)
	}
	unreadable := RenderSnapshot(Snapshot{})
	for _, want := range []string{"Active NAT rules: unavailable", "Multicast routes: unavailable"} {
		if !strings.Contains(unreadable, want) {
			t.Fatalf("unreadable snapshot missing %q: %s", want, unreadable)
		}
	}
}

func TestSnapshotJSONKeepsUnreadableCountersNull(t *testing.T) {
	t.Parallel()
	data, err := json.Marshal(Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"multicast":null`, `"natRules":null`, `"natEvidence":null`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %s: %s", want, data)
		}
	}
}

func TestCaptureSampleSeparatesUnreadableMulticastFromZero(t *testing.T) {
	t.Parallel()
	idle := MulticastInfo{}
	if got := RenderEvent(Event{Type: "sample", Snapshot: &Snapshot{Multicast: &idle}}); !strings.Contains(got, "multicast=0") {
		t.Fatalf("idle sample = %s", got)
	}
	if got := RenderEvent(Event{Type: "sample", Snapshot: &Snapshot{}}); !strings.Contains(got, "multicast=unavailable") {
		t.Fatalf("unreadable sample = %s", got)
	}
}

func TestSnapshotRendersEveryForwardedRouteAndTheLease(t *testing.T) {
	t.Parallel()
	usage := MulticastInfo{Routes: 1, Unresolved: 4, Packets: 182931, Bytes: 76894939, Entries: []mroute.Route{{
		Group: netip.MustParseAddr("224.0.250.64"), Source: netip.MustParseAddr("195.121.94.212"), Input: "iptv", Outputs: []string{"br0"}, Packets: 182931, Bytes: 76894939,
	}}}
	memberships := []Membership{{Bridge: "br0", Port: "switch0.1", Group: "224.0.250.64"}}
	rules := []network.NATRule{{Destination: "213.75.0.0/16", Managed: true, Packets: 187, Bytes: 27452}}
	lease := service.LeaseState{Received: time.Date(2026, 9, 17, 18, 53, 0, 0, time.UTC), Lease: network.Lease{
		Action: "bound", Interface: "iptv", Address: "10.207.71.227", Mask: "20", Routers: []string{"10.207.64.1"},
		StaticRoutes: []string{"213.75.112.0/21", "10.207.64.1"}, Options: map[string]string{"dns": "195.121.1.34", "lease": "3600"},
	}}
	value := Snapshot{
		Network:   networkStatus{Target: "iptv", Addresses: []string{"10.207.71.227/20"}, Routes: []string{"10.207.64.0/20", "213.75.112.0/21 via 10.207.64.1"}},
		Multicast: &usage, Memberships: &memberships, NAT: &rules, Lease: &lease,
	}
	output := RenderSnapshot(value)
	for _, want := range []string{
		"Addresses: 10.207.71.227/20",
		"Routes: 10.207.64.0/20, 213.75.112.0/21 via 10.207.64.1",
		"Multicast routes: 1 forwarding (182931 packets, 76.9 MB), 4 unresolved",
		"  224.0.250.64 from 195.121.94.212: iptv -> br0, 182931 packets, 76.9 MB",
		"Active NAT rules: 1",
		"NAT rules on the IPTV interface:\n  managed 213.75.0.0/16: 187 packets, 27.5 kB",
		"Bridge memberships: 1",
		"  br0 switch0.1 224.0.250.64",
		"DHCP lease: bound at 2026-09-17T18:53:00Z, address 10.207.71.227/20, routers 10.207.64.1, static routes 213.75.112.0/21 10.207.64.1",
		"  dns=195.121.1.34",
		"  lease=3600",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing %q in:\n%s", want, output)
		}
	}
	sample := RenderEvent(Event{Type: "sample", Snapshot: &value})
	if !strings.Contains(sample, "multicast=1 packets=182931") {
		t.Fatalf("sample = %s", sample)
	}
	marker := RenderEvent(Event{Type: "marker", Time: lease.Received, Message: "TV switched on"})
	if marker != "\n>>> 2026-09-17T18:53:00Z TV switched on\n\n" {
		t.Fatalf("marker = %q", marker)
	}
}

// A NAT rule on the IPTV interface only matches traffic a route sends
// there, and a rule ahead of the managed one takes the traffic first, so
// zero counters need the routes and the other rules to be read.
func TestNATEvidenceSeparatesRoutedFromUnreachableDestinations(t *testing.T) {
	t.Parallel()
	destinations := []string{"213.75.0.0/16", "217.166.0.0/16", "195.121.0.0/16"}
	routes := []string{"10.207.64.0/20", "213.75.112.0/21 via 10.207.64.1"}
	rules := []network.NATRule{
		{Destination: "213.75.0.0/16", Packets: 187, Bytes: 27452},
		{Destination: "213.75.0.0/16", Managed: true},
		{Destination: "217.166.0.0/16", Managed: true},
		{Destination: "195.121.0.0/16", Managed: true},
	}
	evidence := natEvidence(destinations, routes, rules)
	want := []NATEvidence{
		{Destination: "213.75.0.0/16", Routes: []string{"213.75.112.0/21 via 10.207.64.1"}, UnmanagedRules: 1, UnmanagedPackets: 187, UnmanagedBytes: 27452},
		{Destination: "217.166.0.0/16", Routes: []string{}},
		{Destination: "195.121.0.0/16", Routes: []string{}},
	}
	for index := range want {
		if evidence[index].Destination != want[index].Destination || !slices.Equal(evidence[index].Routes, want[index].Routes) ||
			evidence[index].UnmanagedRules != want[index].UnmanagedRules || evidence[index].UnmanagedPackets != want[index].UnmanagedPackets {
			t.Fatalf("evidence[%d] = %+v, want %+v", index, evidence[index], want[index])
		}
	}
	output := RenderSnapshot(Snapshot{Network: networkStatus{Target: "iptv", Routes: routes}, NAT: &rules, NATEvidence: &evidence})
	for _, line := range []string{
		"Active NAT rules: 3 managed, 1 unmanaged",
		"  unmanaged 213.75.0.0/16: 187 packets, 27.5 kB",
		"  213.75.0.0/16: routed (213.75.112.0/21 via 10.207.64.1), 0 packets, 0 B; unmanaged rules for it: 1, 187 packets, 27.5 kB",
		"  217.166.0.0/16: no route via iptv, 0 packets, 0 B",
	} {
		if !strings.Contains(output, line) {
			t.Fatalf("missing %q in:\n%s", line, output)
		}
	}
}

func TestSnapshotSaysWhoInstalledTheExecutable(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		snapshot Snapshot
		want     string
	}{
		"standalone": {Snapshot{Version: "5.0.0"}, "Installation: standalone"},
		"package":    {Snapshot{Version: "5.0.0-preview.2", Service: serviceStatus{Package: "5.0.0-preview.2"}}, "Installation: package 5.0.0-preview.2\n"},
		"stale":      {Snapshot{Version: "5.0.0-preview.2", Service: serviceStatus{Package: "5.0.0-preview.1"}}, "Installation: package 5.0.0-preview.1 recorded by dpkg while 5.0.0-preview.2 runs; udm-iptv upgrade reinstalls the package"},
	} {
		if got := RenderSnapshot(test.snapshot); !strings.Contains(got, test.want) {
			t.Errorf("%s: missing %q in:\n%s", name, test.want, got)
		}
	}
}

func TestNATEvidenceCountsBroadRoutesAndTheUnrestrictedDestination(t *testing.T) {
	t.Parallel()
	broad := natEvidence([]string{"213.75.0.0/16"}, []string{"default via 10.207.64.1"}, nil)
	if !slices.Equal(broad[0].Routes, []string{"default via 10.207.64.1"}) {
		t.Fatalf("default route not counted as reaching the destination: %+v", broad)
	}
	routes := []string{"10.207.64.0/20", "213.75.112.0/21 via 10.207.64.1"}
	unrestricted := natEvidence([]string{"0.0.0.0/0"}, routes, []network.NATRule{{Destination: "0.0.0.0/0", Managed: true, Packets: 3}})
	if unrestricted[0].Packets != 3 || len(unrestricted[0].Routes) != 2 {
		t.Fatalf("unrestricted destination = %+v", unrestricted)
	}
}

func TestParseBridgeMemberships(t *testing.T) {
	t.Parallel()
	data := []byte(`[{"mdb":[{"index":29,"dev":"br0","port":"switch0.1","grp":"224.0.250.64","state":"temp","flags":[]},{"index":30,"dev":"br2","port":"switch0.2","grp":"ff02::fb","state":"temp","flags":[]}],"router":{}}]`)
	memberships, err := parseMemberships(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(memberships) != 2 || memberships[0] != (Membership{Bridge: "br0", Port: "switch0.1", Group: "224.0.250.64"}) || memberships[1].Group != "ff02::fb" {
		t.Fatalf("memberships = %+v", memberships)
	}
	if _, err := parseMemberships([]byte("not json")); err == nil {
		t.Fatal("garbage accepted")
	}
}
