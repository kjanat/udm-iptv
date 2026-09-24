package service

import (
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/vishvananda/netlink"

	"github.com/kjanat/udm-iptv/internal/config"
)

var errStopFailed = errors.New("stop failed")

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

func TestStaticAddressDeletionRecognition(t *testing.T) {
	t.Parallel()
	deleted := netlink.AddrUpdate{
		LinkAddress: net.IPNet{IP: net.ParseIP("10.20.30.1"), Mask: net.CIDRMask(24, 32)},
		LinkIndex:   8,
	}
	prefix := netip.MustParsePrefix("10.20.30.1/24")
	if !staticAddressDeleted(prefix, 8, deleted) {
		t.Fatal("configured static address deletion was not recognized")
	}
	for name, update := range map[string]netlink.AddrUpdate{
		"addition":      {LinkAddress: deleted.LinkAddress, LinkIndex: 8, NewAddr: true},
		"other link":    {LinkAddress: deleted.LinkAddress, LinkIndex: 9},
		"other address": {LinkAddress: net.IPNet{IP: net.ParseIP("10.20.30.2"), Mask: net.CIDRMask(24, 32)}, LinkIndex: 8},
		"other prefix":  {LinkAddress: net.IPNet{IP: net.ParseIP("10.20.30.1"), Mask: net.CIDRMask(32, 32)}, LinkIndex: 8},
	} {
		if staticAddressDeleted(prefix, 8, update) {
			t.Errorf("%s was treated as the configured address deletion", name)
		}
	}
	if staticAddressDeleted(netip.Prefix{}, 8, deleted) {
		t.Fatal("existing addressing was treated as a static address deletion")
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
	if NoSuchUnit(errStopFailed) {
		t.Fatal("ordinary stop failure was treated as a missing unit")
	}
}

// improxy's MLD support is compiled in; the configuration decides whether it
// forwards IPv6 multicast.
func TestProxyConfigurationCarriesTheMLDChoice(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		version int
		want    string
	}{
		"off":   {version: 0, want: "mld disable"},
		"MLDv1": {version: 1, want: "mld enable version 1"},
		"MLDv2": {version: config.MaxMLDVersion, want: "mld enable version 2"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			value := config.DefaultKPN()
			value.WAN.Interface = "eth8"
			value.LAN.Interfaces = []string{"br0"}
			value.Proxy.MLDVersion = test.version
			rendered := renderIMProxyConfig(value, "iptv")
			if !strings.Contains(rendered, test.want+"\n") {
				t.Fatalf("configuration lacks %q:\n%s", test.want, rendered)
			}
		})
	}
}
