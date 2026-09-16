package service

import (
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/vishvananda/netlink"

	"github.com/kjanat/udm-iptv/internal/config"
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
	if got := ParseCounter(uint32(7)); got != 7 {
		t.Fatalf("ParseCounter(uint32(7)) = %d", got)
	}
}

func TestNoSuchUnitRecognition(t *testing.T) {
	t.Parallel()
	err := dbus.NewError("org.freedesktop.systemd1.NoSuchUnit", []any{"missing"})
	if !NoSuchUnit(err) {
		t.Fatal("systemd NoSuchUnit error was not recognized")
	}
	if NoSuchUnit(errors.New("stop failed")) {
		t.Fatal("ordinary stop failure was treated as a missing unit")
	}
}
