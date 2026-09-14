package network

import (
	"net"
	"testing"

	"github.com/vishvananda/netlink"
)

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
