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
		{Interface: "br1", Link: "", Snooping: "unrecognized", Querier: ""},
		{Interface: "eth0.10", Link: "", Snooping: "", Querier: ""},
	}
	if !reflect.DeepEqual(links, want) {
		t.Fatalf("unknown became healthy: %+v", links)
	}
	text := RenderSnapshot(Snapshot{Downstream: links, Switches: "firmware 4.1.13, switch0 up", NativeProxy: "igmpproxy.service inactive, no extra proxy processes", Playback: "5 multicast routes (20173 packets), 2 IGMP groups on LAN"})
	for _, required := range []string{
		"br0: link=up, snooping=enabled, querier=disabled",
		"br1: snooping=unrecognized",
		"eth0.10: no sysfs",
		"Switch: firmware 4.1.13, switch0 up",
		"Native UniFi proxy: igmpproxy.service inactive, no extra proxy processes",
		"Receivers: 5 multicast routes (20173 packets), 2 IGMP groups on LAN",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("missing %s in %s", required, text)
		}
	}
	if strings.Contains(text, "not checked") {
		t.Fatal(text)
	}
	if strings.Contains(text, "secret") {
		t.Fatal("unexpected kernel data leaked")
	}
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
	if got := inspectSwitch(fstest.MapFS{}, "4.1.13"); got != "firmware 4.1.13, local switch interfaces unavailable" {
		t.Fatal(got)
	}
	if got := inspectSwitch(fstest.MapFS{}, ""); got != "local switch interfaces unavailable" {
		t.Fatal(got)
	}
}

func TestFormatReceiversSeparatesUnavailableFromZero(t *testing.T) {
	t.Parallel()
	none := 0
	usage := multicastInfo{Routes: 5, Packets: 20173}
	idle := multicastInfo{}
	if got := formatReceivers(&usage, &none); got != "5 multicast routes (20173 packets), 0 IGMP groups on LAN" {
		t.Fatal(got)
	}
	if got := formatReceivers(&usage, nil); got != "5 multicast routes (20173 packets), IGMP groups on LAN unavailable" {
		t.Fatal(got)
	}
	if got := formatReceivers(&idle, &none); got != "0 multicast routes (0 packets), 0 IGMP groups on LAN" {
		t.Fatal(got)
	}
	if got := formatReceivers(nil, &none); got != "multicast routes unavailable, 0 IGMP groups on LAN" {
		t.Fatal(got)
	}
	if got := formatReceivers(nil, nil); got != "multicast routes unavailable, IGMP groups on LAN unavailable" {
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
	if got := formatNativeProxy(true, "active", nil, fs.ErrPermission); got != "igmpproxy.service active, extra proxy processes unavailable" {
		t.Fatal(got)
	}
}
