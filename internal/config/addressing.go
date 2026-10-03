package config

import "net/netip"

// Addressing is a validated WAN address choice. Its zero value preserves the
// interface's existing addresses. Private fields prevent combining DHCP with a
// static address; the JSON configuration remains the editable input format.
type Addressing struct {
	dhcp   bool
	static netip.Prefix
}

// Addressing resolves the saved settings without changing or masking the host
// address. DHCP, static IPv4, and existing interface addressing are all valid.
func (value WAN) Addressing() (Addressing, error) {
	var prefix netip.Prefix
	if value.StaticAddress != "" {
		var err error
		prefix, err = netip.ParsePrefix(value.StaticAddress)
		if err != nil || !prefix.Addr().Is4() {
			return Addressing{}, errStaticAddress
		}
		if value.DHCP {
			return Addressing{}, errDHCPWithStatic
		}
	}
	return Addressing{dhcp: value.DHCP, static: prefix}, nil
}

// DHCP reports whether a DHCP client owns the WAN address.
func (value Addressing) DHCP() bool { return value.dhcp }

// Static returns the configured host prefix, or an invalid prefix for DHCP and
// existing interface addressing. The returned value is independent of Config.
func (value Addressing) Static() netip.Prefix { return value.static }
