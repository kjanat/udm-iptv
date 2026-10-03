package config

import (
	"errors"
	"net/netip"
	"testing"
)

func TestWANAddressing(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		wan    WAN
		dhcp   bool
		prefix netip.Prefix
		err    error
	}{
		{name: "existing interface"},
		{name: "DHCP", wan: WAN{DHCP: true}, dhcp: true},
		{name: "static host", wan: WAN{StaticAddress: "10.20.30.1/24"}, prefix: netip.MustParsePrefix("10.20.30.1/24")},
		{name: "host route", wan: WAN{StaticAddress: "10.20.30.1/32"}, prefix: netip.MustParsePrefix("10.20.30.1/32")},
		{name: "conflicting modes", wan: WAN{DHCP: true, StaticAddress: "10.20.30.1/24"}, err: errDHCPWithStatic},
		{name: "invalid address", wan: WAN{StaticAddress: "broken"}, err: errStaticAddress},
		{name: "missing prefix", wan: WAN{StaticAddress: "10.20.30.1"}, err: errStaticAddress},
		{name: "IPv6", wan: WAN{StaticAddress: "2001:db8::1/64"}, err: errStaticAddress},
		{name: "mapped IPv6", wan: WAN{StaticAddress: "::ffff:10.20.30.1/120"}, err: errStaticAddress},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.wan.Addressing()
			if !errors.Is(err, test.err) {
				t.Fatalf("Addressing: %v, want %v", err, test.err)
			}
			if got.DHCP() != test.dhcp || got.Static() != test.prefix {
				t.Fatalf("got DHCP=%v static=%v, want DHCP=%v static=%v", got.DHCP(), got.Static(), test.dhcp, test.prefix)
			}
		})
	}
}

func TestResolvedAddressSurvivesConfigurationEdits(t *testing.T) {
	t.Parallel()
	wan := WAN{StaticAddress: "10.20.30.1/24"}
	addressing, err := wan.Addressing()
	if err != nil {
		t.Fatal(err)
	}
	wan.StaticAddress = ""
	wan.DHCP = true
	updated, err := wan.Addressing()
	if err != nil {
		t.Fatal(err)
	}
	if addressing.DHCP() || addressing.Static().String() != "10.20.30.1/24" || !updated.DHCP() || updated.Static().IsValid() {
		t.Fatalf("configuration edit changed resolved mode: original=%v updated=%v", addressing, updated)
	}
}
