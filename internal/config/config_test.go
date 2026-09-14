package config

import (
	"os"
	"path/filepath"
	"reflect"
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
