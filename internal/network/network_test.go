package network

import (
	"net"
	"net/netip"
	"testing"

	"github.com/vishvananda/netlink"

	"github.com/kjanat/udm-iptv/internal/config"
)

func TestStaticAddressPreservesHost(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"10.20.30.1/24", "10.20.30.1/32", "10.20.30.1/0"} {
		addressing, err := (config.WAN{StaticAddress: raw}).Addressing()
		if err != nil {
			t.Fatal(err)
		}
		address := staticAddress(addressing.Static())
		if address.String() != raw || !address.IP.Equal(netip.MustParseAddr("10.20.30.1").AsSlice()) {
			t.Errorf("static address = %s, want %s", address, raw)
		}
	}
}

func TestExistingAddressingDoesNotReplaceAddress(t *testing.T) {
	t.Parallel()
	// No link exists at this index. Existing addressing without routes must not
	// make any netlink mutation, even though no address was provided.
	link := &netlink.Dummy{Index: -1}
	if err := ApplyStatic(config.Config{}, config.Addressing{}, link); err != nil {
		t.Fatal(err)
	}
}

func TestMaskBits(t *testing.T) {
	t.Parallel()
	for input, want := range map[string]int{"24": 24, "255.255.255.0": 24, "255.255.255.255": 32} {
		got, err := maskBits(input)
		if err != nil {
			t.Errorf("maskBits(%q): %v", input, err)

			continue
		}
		if got != want {
			t.Errorf("maskBits(%q) = %d, want %d", input, got, want)
		}
	}
}

func TestInvalidMasks(t *testing.T) {
	t.Parallel()
	for _, input := range []string{"33", "255.0.255.0", "nope"} {
		if _, err := maskBits(input); err == nil {
			t.Errorf("maskBits(%q) unexpectedly succeeded", input)
		}
	}
}

func TestSameAddressComparesHostAndPrefix(t *testing.T) {
	t.Parallel()
	current := netlink.Addr{IPNet: &net.IPNet{IP: net.ParseIP("10.0.0.2"), Mask: net.CIDRMask(24, 32)}}
	same := netlink.Addr{IPNet: &net.IPNet{IP: net.ParseIP("10.0.0.2"), Mask: net.CIDRMask(24, 32)}}
	differentHost := netlink.Addr{IPNet: &net.IPNet{IP: net.ParseIP("10.0.0.3"), Mask: net.CIDRMask(24, 32)}}
	differentPrefix := netlink.Addr{IPNet: &net.IPNet{IP: net.ParseIP("10.0.0.2"), Mask: net.CIDRMask(32, 32)}}
	if !sameAddress(current, same) {
		t.Fatal("identical lease addresses did not match")
	}
	if sameAddress(current, differentHost) {
		t.Fatal("different host addresses in the same subnet matched")
	}
	if sameAddress(current, differentPrefix) {
		t.Fatal("different prefix lengths matched")
	}
}
