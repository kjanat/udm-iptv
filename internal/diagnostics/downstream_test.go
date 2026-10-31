package diagnostics

import (
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

func TestDownstreamMissingDataIsNotHealthy(t *testing.T) {
	t.Parallel()
	system := fstest.MapFS{
		"class/net/br0/operstate":                 {Data: []byte("up\n")},
		"class/net/br0/bridge/multicast_snooping": {Data: []byte("1\n")},
		"class/net/br0/bridge/multicast_querier":  {Data: []byte("0\n")},
		"class/net/br1/bridge/multicast_snooping": {Data: []byte("secret-unexpected-value")},
	}
	links := inspectDownstream(system, []string{"br0", "br1", "eth0.10"})
	want := []downstreamStatus{
		{Interface: "br0", Link: "up", Snooping: "enabled", Querier: "disabled"},
		{Interface: "br1", Link: "", Snooping: `unrecognized: "secret-unexpected-value"`, Querier: ""},
		{Interface: "eth0.10", Link: "", Snooping: "", Querier: ""},
	}
	if !reflect.DeepEqual(links, want) {
		t.Fatalf("unknown became healthy: %+v", links)
	}
	text := RenderSnapshot(Snapshot{Downstream: links, Switches: "firmware 4.1.13, switch0 up", NativeProxy: "igmpproxy.service inactive, no extra proxy processes", Playback: "5 multicast routes (20173 packets); receivers: br0: 1 group on 1 port; 2 LAN groups joined by the router itself"})
	for _, required := range []string{
		"br0: link=up, snooping=enabled, querier=disabled",
		"br1: snooping=unrecognized",
		"eth0.10: no sysfs",
		"Switch: firmware 4.1.13, switch0 up",
		"Native UniFi proxy: igmpproxy.service inactive, no extra proxy processes",
		"Forwarding: 5 multicast routes (20173 packets); receivers: br0: 1 group on 1 port; 2 LAN groups joined by the router itself",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("missing %s in %s", required, text)
		}
	}
	if strings.Contains(text, "not checked") {
		t.Fatal(text)
	}
	assertDiagnosticDetails(t, text, "secret-unexpected-value")
}

func TestInspectSwitchListsLocalDSA(t *testing.T) {
	t.Parallel()
	system := fstest.MapFS{
		"class/net/switch0/operstate": {Data: []byte("up\n")},
		"class/net/br0/operstate":     {Data: []byte("up\n")},
		"class/net/eth8/operstate":    {Data: []byte("up\n")},
	}
	got := inspectSwitch(system, "4.1.13")
	if got != "firmware 4.1.13, switch0 up" {
		t.Fatal(got)
	}
}

func TestInspectSwitchSeparatesUnavailableFromEmpty(t *testing.T) {
	t.Parallel()
	empty := fstest.MapFS{"class/net/br0/operstate": {Data: []byte("up\n")}}
	if got := inspectSwitch(empty, "4.1.13"); got != "firmware 4.1.13, no local switch interfaces" {
		t.Fatal(got)
	}
	if got := inspectSwitch(fstest.MapFS{}, "4.1.13"); !strings.HasPrefix(got, "firmware 4.1.13, local switch interfaces unavailable: read class/net:") {
		t.Fatal(got)
	}
	if got := inspectSwitch(fstest.MapFS{}, ""); !strings.HasPrefix(got, "local switch interfaces unavailable: read class/net:") {
		t.Fatal(got)
	}
}

func TestFormatReceiversSeparatesUnavailableFromZero(t *testing.T) {
	t.Parallel()
	none := 0
	usage := MulticastInfo{Routes: 5, Packets: 20173}
	idle := MulticastInfo{}
	if got := formatReceivers(&usage, "br0: none", &none); got != "5 multicast routes (20173 packets); receivers: br0: none; 0 LAN groups joined by the router itself" {
		t.Fatal(got)
	}
	if got := formatReceivers(&usage, "br0: none", nil); got != "5 multicast routes (20173 packets); receivers: br0: none; router's own LAN group memberships unavailable" {
		t.Fatal(got)
	}
	if got := formatReceivers(&idle, "br0: none", &none); got != "0 multicast routes (0 packets); receivers: br0: none; 0 LAN groups joined by the router itself" {
		t.Fatal(got)
	}
	if got := formatReceivers(nil, "bridge MDB unavailable", &none); got != "multicast routes unavailable; receivers: bridge MDB unavailable; 0 LAN groups joined by the router itself" {
		t.Fatal(got)
	}
	if got := formatReceivers(nil, "bridge MDB unavailable", nil); got != "multicast routes unavailable; receivers: bridge MDB unavailable; router's own LAN group memberships unavailable" {
		t.Fatal(got)
	}
}

// A receiver is a bridge port that joined a routable group. The bridge's own
// host entry and link-local groups such as mDNS are not receivers.
func TestReceiverSummaryCountsBridgePortJoins(t *testing.T) {
	t.Parallel()
	memberships := []Membership{
		{Bridge: "br0", Port: "eth1", Group: "224.0.252.138"},
		{Bridge: "br0", Port: "eth1", Group: "224.0.250.64"},
		{Bridge: "br0", Port: "eth2", Group: "224.0.252.138"},
		{Bridge: "br0", Port: "br0", Group: "224.0.252.138"},
		{Bridge: "br0", Port: "eth3", Group: "224.0.0.251"},
		{Bridge: "br2", Port: "br2", Group: "239.255.255.250"},
		{Bridge: "br3", Port: "eth9", Group: "ff05::1"},
	}
	if got := receiverSummary(&memberships, []string{"br0", "br2"}); got != "br0: 2 groups on 2 ports, br2: none" {
		t.Fatal(got)
	}
	if got := receiverSummary(&memberships, []string{"br3"}); got != "br3: 1 group on 1 port" {
		t.Fatal(got)
	}
	if got := receiverSummary(nil, []string{"br0"}); got != "bridge MDB unavailable" {
		t.Fatal(got)
	}
}

func TestCountLANIGMPGroupsSkipsLinkLocal(t *testing.T) {
	t.Parallel()
	table := "" +
		"Idx\tDevice    : Count Querier\tGroup    Users Timer\tReporter\n" +
		"1\tlo        :     1      V3\n" +
		"\t\t\t010000E0     1 0:00000000\t\t0\n" +
		"2\tbr0       :     2      V3\n" +
		"\t\t\t010000E0     1 0:00000000\t\t0\n" +
		"\t\t\tEF010101     1 0:00000000\t\t0\n"
	if got := countLANIGMPGroups(table, []string{"br0"}); got != 1 {
		t.Fatalf("groups = %d", got)
	}
}

func TestFormatNativeProxy(t *testing.T) {
	t.Parallel()
	if got := formatNativeProxy(false, "", nil, nil); got != "igmpproxy.service not loaded, no extra proxy processes" {
		t.Fatal(got)
	}
	if got := formatNativeProxy(true, "active", []int{9, 11}, nil); got != "igmpproxy.service active, extra proxy pids 9 11" {
		t.Fatal(got)
	}
	if got := formatNativeProxy(true, "active", nil, fs.ErrPermission); got != "igmpproxy.service active, extra proxy processes unavailable: permission denied" {
		t.Fatal(got)
	}
}
