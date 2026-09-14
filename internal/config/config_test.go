package config

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state", "config.json")
	want := Default()
	want.WAN.AllowDefaultRoute = true
	want.Proxy.SourceRanges = []string{"195.121.0.0/16"}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch:\n got: %#v\nwant: %#v", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %o, want 600", info.Mode().Perm())
	}
}

func TestImportLegacyDoesNotExecuteShell(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	marker := filepath.Join(directory, "executed")
	legacy := filepath.Join(directory, "udm-iptv.conf")
	content := `IPTV_WAN_INTERFACE="eth8"
IPTV_WAN_VLAN="4"
IPTV_WAN_RANGES="213.75.0.0/16 195.121.0.0/16"
IPTV_LAN_INTERFACES="br0"
IPTV_IGMPPROXY_PROGRAM="improxy"
IPTV_IGMPPROXY_IGMP_VERSION="3"
MALICIOUS="$(touch ` + marker + `)"
`
	if err := os.WriteFile(legacy, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := ImportLegacy(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if value.Profile != "legacy" || value.WAN.Interface != "eth8" {
		t.Fatalf("unexpected import: %#v", value)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("legacy import executed shell content")
	}
}

func TestProfilesValidate(t *testing.T) {
	t.Parallel()
	for _, profile := range Profiles() {
		if profile.ID == "custom" {
			continue
		}
		if err := profile.Config.Validate(); err != nil {
			t.Errorf("profile %s: %v", profile.ID, err)
		}
	}
}

func TestImportLegacyInfersExactProviderProfile(t *testing.T) {
	t.Parallel()
	legacy := filepath.Join(t.TempDir(), "udm-iptv.conf")
	content := `IPTV_WAN_INTERFACE="eth8"
IPTV_WAN_VLAN="4"
IPTV_WAN_VLAN_INTERFACE="iptv"
IPTV_WAN_RANGES="213.75.0.0/16 217.166.0.0/16 195.121.0.0/16"
IPTV_WAN_DHCP_OPTIONS="-O staticroutes -V IPTV_RG"
IPTV_LAN_INTERFACES="br0"
IPTV_IGMPPROXY_PROGRAM="improxy"
IPTV_IGMPPROXY_IGMP_VERSION="3"
`
	if err := os.WriteFile(legacy, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := ImportLegacy(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if value.Profile != "kpn" {
		t.Fatalf("profile = %q, want kpn", value.Profile)
	}
}

func TestEveryLegacyProviderSignatureIsUnique(t *testing.T) {
	t.Parallel()
	for _, profile := range Profiles() {
		if profile.ID == "custom" {
			continue
		}
		legacy := profile.Config
		legacy.WAN.NATDestinations = append([]string(nil), profile.Config.Proxy.SourceRanges...)
		got, found := InferLegacyProfile(legacy)
		if !found || got != profile.ID {
			t.Errorf("profile %q inferred as %q (found %v)", profile.ID, got, found)
		}
	}
}

func TestProfilesPreserveLegacyProxySourcesWithoutNATingMulticast(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"bt", "init7", "magentatv", "meo", "swisscom", "telenor"} {
		profile, found := ProfileByID(id)
		if !found {
			t.Fatalf("profile %q is missing", id)
		}
		if len(profile.Config.Proxy.SourceRanges) == 0 {
			t.Errorf("profile %q has no proxy source ranges", id)
		}
		for _, destination := range profile.Config.WAN.NATDestinations {
			if destination == "224.0.0.0/4" || destination == "224.0.0.0/8" || destination == "233.50.230.0/24" || destination == "239.77.0.0/16" {
				t.Errorf("profile %q NATs multicast destination %q", id, destination)
			}
		}
	}
}

func TestMagentaTVDoesNotRunDHCPOnPPPInterface(t *testing.T) {
	t.Parallel()
	profile, found := ProfileByID("magentatv")
	if !found {
		t.Fatal("magentatv profile is missing")
	}
	if profile.Config.WAN.Interface != "ppp0" || profile.Config.WAN.DHCP {
		t.Fatalf("unexpected MagentaTV WAN configuration: %#v", profile.Config.WAN)
	}
}

func TestNATAndProxyRangesAreIndependent(t *testing.T) {
	t.Parallel()
	value := Default()
	value.Proxy.SourceRanges = []string{"198.51.100.0/24"}
	if reflect.DeepEqual(value.WAN.NATDestinations, value.Proxy.SourceRanges) {
		t.Fatal("NAT destinations and proxy sources must be independent")
	}
}

func TestImportLegacyNormalizesHostDestinations(t *testing.T) {
	t.Parallel()
	legacy := filepath.Join(t.TempDir(), "udm-iptv.conf")
	content := `IPTV_WAN_INTERFACE="eth8"
IPTV_WAN_VLAN="0"
IPTV_WAN_DHCP="false"
IPTV_WAN_DHCP_OPTIONS=""
IPTV_WAN_RANGES="224.0.0.0/4 93.91.111.0/24 148.122.7.125"
IPTV_LAN_INTERFACES="br0"
IPTV_IGMPPROXY_PROGRAM="improxy"
IPTV_IGMPPROXY_IGMP_VERSION="3"
`
	if err := os.WriteFile(legacy, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := ImportLegacy(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if got := value.WAN.NATDestinations[1]; got != "148.122.7.125/32" {
		t.Fatalf("normalized destination = %q", got)
	}
	if value.Profile != "telenor" {
		t.Fatalf("profile = %q, want telenor", value.Profile)
	}
	if reflect.DeepEqual(value.WAN.NATDestinations, value.Proxy.SourceRanges) {
		t.Fatal("known legacy profile kept proxy sources in NAT destinations")
	}
	if !slices.Contains(value.Proxy.SourceRanges, "224.0.0.0/4") {
		t.Fatalf("Telenor proxy sources = %q", value.Proxy.SourceRanges)
	}
}

func TestImportLegacyPreservesDefaultRouteFallback(t *testing.T) {
	t.Parallel()
	for name, noGateway := range map[string]string{"default": "", "explicit opt-out": "eth8"} {
		t.Run(name, func(t *testing.T) {
			legacy := filepath.Join(t.TempDir(), "udm-iptv.conf")
			content := `IPTV_WAN_INTERFACE="eth8"
IPTV_WAN_VLAN="4"
IPTV_WAN_DHCP="true"
IPTV_WAN_RANGES="213.75.0.0/16"
IPTV_LAN_INTERFACES="br0"
IPTV_IGMPPROXY_PROGRAM="improxy"
IPTV_IGMPPROXY_IGMP_VERSION="3"
NO_GATEWAY="` + noGateway + `"
`
			if err := os.WriteFile(legacy, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			value, err := ImportLegacy(legacy)
			if err != nil {
				t.Fatal(err)
			}
			if value.WAN.AllowDefaultRoute != (noGateway == "") {
				t.Fatalf("allowDefaultRoute = %t for NO_GATEWAY=%q", value.WAN.AllowDefaultRoute, noGateway)
			}
		})
	}
}

func TestImportLegacyPreservesLANSourceRanges(t *testing.T) {
	t.Parallel()
	legacy := filepath.Join(t.TempDir(), "udm-iptv.conf")
	content := `IPTV_WAN_INTERFACE="eth8"
IPTV_WAN_VLAN="4"
IPTV_WAN_RANGES="213.75.0.0/16 217.166.0.0/16 195.121.0.0/16"
IPTV_LAN_RANGES="192.0.2.10 198.51.100.0/24 192.0.2.10/32"
IPTV_LAN_INTERFACES="br0"
IPTV_IGMPPROXY_PROGRAM="igmpproxy"
IPTV_IGMPPROXY_IGMP_VERSION="3"
`
	if err := os.WriteFile(legacy, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := ImportLegacy(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if value.Profile != "kpn" {
		t.Fatalf("profile = %q, want kpn", value.Profile)
	}
	for _, source := range []string{"213.75.0.0/16", "192.0.2.10/32", "198.51.100.0/24"} {
		if !slices.Contains(value.Proxy.SourceRanges, source) {
			t.Errorf("proxy sources %q do not contain %q", value.Proxy.SourceRanges, source)
		}
	}
	if count := len(value.Proxy.SourceRanges); count != 5 {
		t.Fatalf("proxy sources contain duplicates: %q", value.Proxy.SourceRanges)
	}
}
