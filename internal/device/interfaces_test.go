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
