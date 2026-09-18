package ui

import (
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kjanat/udm-iptv/internal/diagnostics"
	"github.com/kjanat/udm-iptv/internal/mroute"
)

func snapshotJSON(t *testing.T) string {
	t.Helper()

	return `{"time":"2026-09-18T20:00:00Z","type":"started","message":"Capture started; expected completion 2026-09-18T20:15:00Z"}
{"time":"2026-09-18T20:00:01Z","type":"initial","snapshot":{"service":{"activeState":"active","subState":"running","proxy":"improxy","proxyPID":1745611},"network":{"target":"iptv","linkState":"up","addresses":["10.207.71.227/20"],"routes":["10.207.64.0/20","213.75.112.0/21 via 10.207.64.1"]},"multicast":{"routes":0,"unresolved":3,"packets":0,"bytes":0,"entries":[]},"memberships":[{"bridge":"br0","port":"switch0.1","group":"239.255.255.250"}]}}
{"time":"2026-09-18T20:00:06Z","type":"marker","message":"TV switched on"}
{"time":"2026-09-18T20:00:11Z","type":"sample","snapshot":{"service":{"activeState":"active","subState":"running","proxy":"improxy","proxyPID":1745611},"network":{"target":"iptv","linkState":"up","addresses":["10.207.71.227/20"],"routes":["10.207.64.0/20","213.75.112.0/21 via 10.207.64.1"]},"multicast":{"routes":1,"unresolved":3,"packets":1000,"bytes":1500000,"entries":[{"group":"224.0.250.64","source":"195.121.94.212","input":"iptv","outputs":["br0"],"packets":1000,"bytes":1500000,"wrong":0}]},"memberships":[{"bridge":"br0","port":"switch0.1","group":"239.255.255.250"},{"bridge":"br0","port":"switch0.1","group":"224.0.250.64"}]}}
{"time":"2026-09-18T20:00:21Z","type":"sample","snapshot":{"service":{"activeState":"active","subState":"running","proxy":"improxy","proxyPID":1745611},"network":{"target":"iptv","linkState":"up","addresses":["10.207.71.227/20"],"routes":["10.207.64.0/20","213.75.112.0/21 via 10.207.64.1"]},"multicast":{"routes":1,"unresolved":3,"packets":4000,"bytes":6000000,"entries":[{"group":"224.0.250.64","source":"195.121.94.212","input":"iptv","outputs":["br0"],"packets":4000,"bytes":6000000,"wrong":0}]},"memberships":[{"bridge":"br0","port":"switch0.1","group":"239.255.255.250"},{"bridge":"br0","port":"switch0.1","group":"224.0.250.64"}]}}
{"time":"2026-09-18T20:00:31Z","type":"completed","message":"Capture finished within its deadline."}
`
}

func TestTimelineShowsWhatChanged(t *testing.T) {
	t.Parallel()
	events := parseCapture(snapshotJSON(t))
	if len(events) != 6 {
		t.Fatalf("parsed %d events", len(events))
	}
	lines := timeline(events)
	want := []string{
		"20:00:00 started: Capture started; expected completion 2026-09-18T20:15:00Z",
		"20:00:01 service active/running, proxy improxy pid 1745611; 0 forwarding routes, 3 unresolved",
		"20:00:06 >>> TV switched on",
		"20:00:11 + 224.0.250.64 from 195.121.94.212 iptv -> br0",
		"20:00:11 + member br0 switch0.1 224.0.250.64",
		"20:00:21 224.0.250.64 from 195.121.94.212 iptv -> br0: +3000 packets (300 pps, 3.60 Mbit/s)",
		"20:00:31 completed: Capture finished within its deadline.",
	}
	if !slices.Equal(lines, want) {
		t.Fatalf("timeline =\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}

func TestChangesCoverServiceNetworkAndRoutesGoingAway(t *testing.T) {
	t.Parallel()
	route := mroute.Route{Group: netip.MustParseAddr("224.0.250.64"), Source: netip.MustParseAddr("195.121.94.212"), Input: "iptv", Outputs: []string{"br0"}, Packets: 5}
	before := &diagnostics.Snapshot{}
	before.Service.ActiveState, before.Service.SubState, before.Service.ProxyPID = "active", "running", 10
	before.Network.Target, before.Network.LinkState, before.Network.Routes = "iptv", "up", []string{"10.207.64.0/20"}
	after := &diagnostics.Snapshot{}
	after.Service.ActiveState, after.Service.SubState, after.Service.ProxyPID, after.Service.Restarts = "activating", "auto-restart", 11, 1
	after.Network.Target, after.Network.LinkState = "iptv", "down"
	before.Multicast = snapshotMulticast(route)
	after.Multicast = snapshotMulticast()
	got := changes(before, after, 10*time.Second)
	want := []string{
		"service active/running -> activating/auto-restart",
		"proxy  pid 10 -> 11",
		"systemd restarts 0 -> 1",
		"iptv link up -> down",
		"- route 10.207.64.0/20",
		"- 224.0.250.64 from 195.121.94.212 iptv -> br0",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("changes =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if got := status(nil); got[0] != "waiting for the first snapshot" {
		t.Fatal(got)
	}
	if lines := status(after); len(lines) != 3 || lines[2] != "multicast 0 forwarding (0 packets), 0 unresolved" {
		t.Fatalf("status = %q", lines)
	}
}

func TestTextCaptureHasNoEvents(t *testing.T) {
	t.Parallel()
	if events := parseCapture("udm-iptv diagnostics\nCapture started; expected completion 2026-09-18T20:15:00Z\n"); len(events) != 0 {
		t.Fatalf("text parsed as %d events", len(events))
	}
}

func snapshotMulticast(routes ...mroute.Route) *diagnostics.MulticastInfo {
	usage := &diagnostics.MulticastInfo{Routes: len(routes), Entries: routes}
	for _, route := range routes {
		usage.Packets += route.Packets
		usage.Bytes += route.Bytes
	}

	return usage
}
