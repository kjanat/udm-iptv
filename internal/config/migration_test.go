package config

import (
	"fmt"
	"path/filepath"
	"slices"
	"testing"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/filemode"
)

func TestLegacyMissingDefaults(t *testing.T) {
	for _, test := range []struct {
		vlan, dhcp, program string
		wantDHCP            bool
		wantProxy           string
	}{
		{"0", "", "", false, "igmpproxy"},
		{"4", "", "", true, "igmpproxy"},
		// udm-iptvd ran udhcpc only inside its VLAN branch, so an untagged
		// installation performed no DHCP even when the variable enabled it.
		{"0", "true", "improxy", false, "improxy"},
		{"4", "false", "igmpproxy", false, "igmpproxy"},
	} {
		file := filepath.Join(t.TempDir(), "legacy.conf")
		data := fmt.Sprintf("IPTV_WAN_VLAN=%s\nIPTV_WAN_RANGES=1.2.3.0/24\n", test.vlan)
		if test.dhcp != "" {
			data += "IPTV_WAN_DHCP=" + test.dhcp + "\n"
		}
		if test.program != "" {
			data += "IPTV_IGMPPROXY_PROGRAM=" + test.program + "\n"
		}
		if err := atomicfile.Write(file, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		value, err := ImportLegacy(file)
		if err != nil {
			t.Fatal(err)
		}
		if value.WAN.DHCP != test.wantDHCP || value.Proxy.Program != test.wantProxy {
			t.Fatalf("migration defaults: %+v", test)
		}
		if test.wantProxy == "igmpproxy" && len(value.Proxy.SourceRanges) == 0 {
			t.Fatal("missing legacy sources")
		}
	}
}

// importLegacyString writes content as a legacy configuration file and
// imports it.
func importLegacyString(t *testing.T, content string) Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "udm-iptv.conf")
	if err := atomicfile.Write(path, []byte(content), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	value, err := ImportLegacy(path)
	if err != nil {
		t.Fatal(err)
	}

	return value
}

// A variable the shell daemon defaulted to empty must not pick up whichever
// provider profile the rewrite happens to start from.
func TestLegacyAbsenceDoesNotInheritProviderDefaults(t *testing.T) {
	t.Parallel()
	value := importLegacyString(t, "IPTV_WAN_INTERFACE=eth8\nIPTV_IGMPPROXY_PROGRAM=improxy\n")
	if len(value.WAN.NATDestinations) != 0 {
		t.Errorf("absent IPTV_WAN_RANGES produced %v", value.WAN.NATDestinations)
	}
	if value.WAN.VLAN != legacyVLAN {
		t.Errorf("absent IPTV_WAN_VLAN produced %d", value.WAN.VLAN)
	}
	if value.Proxy.IGMPVersion != legacyIGMPVersion {
		t.Errorf("absent IPTV_IGMPPROXY_IGMP_VERSION produced IGMPv%d", value.Proxy.IGMPVersion)
	}
	if value.Profile != profileLegacy {
		t.Errorf("profile = %q, want %q", value.Profile, profileLegacy)
	}
}

// udm-iptvd passed its hardcoded udhcpc options only inside the VLAN branch.
func TestLegacyDHCPOptionsFollowTheVLANBranch(t *testing.T) {
	t.Parallel()
	tagged := importLegacyString(t, "IPTV_WAN_VLAN=4\nIPTV_WAN_RANGES=213.75.0.0/16\n")
	if !slices.Equal(tagged.WAN.DHCPOptions, legacyDHCPOptions) {
		t.Errorf("tagged uplink options = %v, want %v", tagged.WAN.DHCPOptions, legacyDHCPOptions)
	}
	if !tagged.WAN.DHCP {
		t.Error("tagged uplink lost DHCP")
	}
	untagged := importLegacyString(t, "IPTV_WAN_VLAN=0\nIPTV_WAN_DHCP=true\nIPTV_WAN_RANGES=213.75.0.0/16\n")
	if untagged.WAN.DHCP || len(untagged.WAN.DHCPOptions) != 0 {
		t.Errorf("untagged uplink ran DHCP: %t %v", untagged.WAN.DHCP, untagged.WAN.DHCPOptions)
	}
}

// The shell applied the static address only when DHCP was disabled by name.
func TestLegacyStaticAddressNeedsDisabledDHCP(t *testing.T) {
	t.Parallel()
	const address = "10.20.30.1/24"
	disabled := importLegacyString(t, "IPTV_WAN_DHCP=false\nIPTV_WAN_STATIC_IP="+address+"\nIPTV_WAN_RANGES=213.75.0.0/16\n")
	if disabled.WAN.StaticAddress != address {
		t.Errorf("static address = %q, want %q", disabled.WAN.StaticAddress, address)
	}
	dead := importLegacyString(t, "IPTV_WAN_STATIC_IP="+address+"\nIPTV_WAN_RANGES=213.75.0.0/16\n")
	if dead.WAN.StaticAddress != "" {
		t.Errorf("address the shell never applied survived as %q", dead.WAN.StaticAddress)
	}
}

// Quickleave defaulted on for igmpproxy and off for improxy.
func TestLegacyQuickleaveAsymmetry(t *testing.T) {
	t.Parallel()
	const ranges = "IPTV_WAN_RANGES=213.75.0.0/16\n"
	if value := importLegacyString(t, ranges); !value.Proxy.QuickLeave {
		t.Error("igmpproxy lost its default quickleave")
	}
	if value := importLegacyString(t, ranges+"IPTV_IGMPPROXY_PROGRAM=improxy\n"); value.Proxy.QuickLeave {
		t.Error("improxy gained quickleave it never had")
	}
	if value := importLegacyString(t, ranges+"IPTV_IGMPPROXY_DISABLE_QUICKLEAVE=true\n"); value.Proxy.QuickLeave {
		t.Error("disabled quickleave survived")
	}
}

// Only the literal strings selected the non-default proxy and IGMP version.
func TestLegacyProxySelectionIsExactMatch(t *testing.T) {
	t.Parallel()
	const ranges = "IPTV_WAN_RANGES=213.75.0.0/16\n"
	for _, program := range []string{"", "igmpproxy", "IMProxy", "nonsense"} {
		value := importLegacyString(t, ranges+"IPTV_IGMPPROXY_PROGRAM="+program+"\n")
		if value.Proxy.Program != legacyProxyProgram {
			t.Errorf("program %q selected %q", program, value.Proxy.Program)
		}
	}
	if value := importLegacyString(t, ranges+"IPTV_IGMPPROXY_PROGRAM=improxy\n"); value.Proxy.Program != "improxy" {
		t.Errorf("improxy not selected: %q", value.Proxy.Program)
	}
	for _, version := range []string{"", "2", "4", "nonsense"} {
		value := importLegacyString(t, ranges+"IPTV_IGMPPROXY_IGMP_VERSION="+version+"\n")
		if value.Proxy.IGMPVersion != legacyIGMPVersion {
			t.Errorf("version %q selected IGMPv%d", version, value.Proxy.IGMPVersion)
		}
	}
	if value := importLegacyString(t, ranges+"IPTV_IGMPPROXY_IGMP_VERSION=3\n"); value.Proxy.IGMPVersion != DefaultIGMPVersion {
		t.Errorf("IGMPv3 not selected: %d", value.Proxy.IGMPVersion)
	}
}
