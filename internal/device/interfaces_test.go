package device

import (
	"bufio"
	"io"
	"net"
	"slices"
	"strings"
	"testing"

	"github.com/kjanat/udm-iptv/internal/config"
)

type failedRouteReader struct{}

func (failedRouteReader) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

func TestDefaultRouteInterfaceRejectsIncompleteTable(t *testing.T) {
	const route = "eth8 00000000 0100000A 0003 0 0 100 00000000\n"
	for name, reader := range map[string]io.Reader{
		"read failure":                   failedRouteReader{},
		"failure after candidate":        io.MultiReader(strings.NewReader(route), failedRouteReader{}),
		"oversized line after candidate": strings.NewReader(route + strings.Repeat("x", bufio.MaxScanTokenSize+1)),
	} {
		t.Run(name, func(t *testing.T) {
			if got := defaultRouteInterface(reader); got != "" {
				t.Fatalf("incomplete route table selected %q", got)
			}
		})
	}
	if got := defaultRouteInterface(strings.NewReader(route)); got != "eth8" {
		t.Fatalf("complete route table selected %q, want eth8", got)
	}
}

func TestWANHintsForBoard(t *testing.T) {
	t.Parallel()
	for board, want := range map[string][]string{
		"UDM": {"eth4"}, "udmpro": {"eth8", "eth9"}, "UDMPROSE": {"eth8", "eth9"},
		"UDR7": {"eth3", "eth4", "eth2"}, "UCGF": {"eth6", "eth4"},
	} {
		if got := wanHints(board); !slices.Equal(got, want) {
			t.Errorf("wanHints(%q) = %q, want %q", board, got, want)
		}
	}
}

func TestSelectWANPrefersLiveUplink(t *testing.T) {
	t.Parallel()
	ethernet := []string{"eth0", "eth7", "eth8", "eth9", "eth12"}
	bridged := map[string]bool{"eth0": true, "eth7": true}
	carrier := map[string]bool{"eth0": true, "eth7": true, "eth9": true, "eth12": true}
	hints := []string{"eth8", "eth9"}
	if got := selectWAN("eth12", ethernet, bridged, carrier, hints); got != "eth12" {
		t.Fatalf("non-standard route = %q, want eth12", got)
	}
	if got := selectWAN("eth8.4", ethernet, bridged, carrier, hints); got != "eth8" {
		t.Fatalf("vlan parent = %q, want eth8", got)
	}
	if got := selectWAN("", ethernet, bridged, carrier, hints); got != "eth9" {
		t.Fatalf("carrier among hints = %q, want eth9", got)
	}
	if got := selectWAN("br0", ethernet, bridged, carrier, nil); got != "eth9" {
		t.Fatalf("bridge route ignored = %q, want eth9", got)
	}
	if got := walkToEthernet("eth8.4", nil); got != "eth8" {
		t.Fatalf("vlan parent = %q, want eth8", got)
	}
	if got := walkToEthernet("eth8.6", nil); got != "eth8" {
		t.Fatalf("pppoe vlan parent = %q, want eth8", got)
	}
	if got := walkToEthernet("br0", nil); got != "" {
		t.Fatalf("bridge parent = %q", got)
	}
	lower := func(name string) string {
		return map[string]string{"ppp0": "eth8.6"}[name]
	}
	if got := walkToEthernet("ppp0", lower); got != "eth8" {
		t.Fatalf("pppoe parent = %q, want eth8", got)
	}
}

func TestSelectWANIgnoresSwitchPortsWhenInternetIsPPPoE(t *testing.T) {
	t.Parallel()
	ethernet := []string{"eth0", "eth1", "eth2", "eth3", "eth4", "eth5", "eth6", "eth7", "eth8", "eth9", "eth10"}
	skip := map[string]bool{"eth0": true, "eth1": true, "eth2": true, "eth3": true, "eth4": true, "eth5": true, "eth6": true, "eth7": true}
	carrier := map[string]bool{"eth0": true, "eth1": true, "eth2": true, "eth3": true, "eth4": true, "eth5": true, "eth6": true, "eth7": true, "eth8": true}
	if got := selectWAN("eth8", ethernet, skip, carrier, nil); got != "eth8" {
		t.Fatalf("udm-pro pppoe uplink = %q, want eth8", got)
	}
	if got := selectWAN("", ethernet, map[string]bool{}, carrier, nil); got != "eth0" {
		t.Fatalf("unfiltered switch ports = %q, want eth0", got)
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

func TestDefaultRouteInterfaceIgnoresZeroBasedPrefixes(t *testing.T) {
	t.Parallel()
	routes := `Iface Destination Gateway Flags RefCnt Use Metric Mask
tun0 00000000 0100000A 0003 0 0 50 00000080
tun1 00000000 0100000A 0003 0 0 60 000000FF
eth8 00000000 0100000A 0003 0 0 100 00000000
`
	if got := defaultRouteInterface(strings.NewReader(routes)); got != "eth8" {
		t.Fatalf("split-default route selected %q, want eth8", got)
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
	got := rewriteWAN(value, "eth4")
	if got.WAN.Interface != "eth4.35" {
		t.Fatalf("WAN interface = %q, want eth4.35", got.WAN.Interface)
	}
}

// The proxy exposes multicast on every interface it is given, so detection
// offers the bridges and the seed enables one.
func TestPrimaryDownstreamSeedsOneInterface(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		detected []string
		want     string
	}{
		"home lan among container bridges": {detected: []string{"br0", "br100", "brdocker"}, want: "br0"},
		"no br0":                           {detected: []string{"br100", "br200"}, want: "br100"},
		"uxg subinterfaces":                {detected: []string{"br0", "eth0.10"}, want: "br0"},
		"nothing detected":                 {detected: nil, want: ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := primaryDownstream(test.detected); got != test.want {
				t.Fatalf("primaryDownstream(%q) = %q, want %q", test.detected, got, test.want)
			}
		})
	}
}
