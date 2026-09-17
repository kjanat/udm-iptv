package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/filemode"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state", "config.json")
	// A provider config exercises every omitempty slice on the way out.
	want := DefaultKPN()
	want.WAN.DHCPRoutes = RoutesAllowDefault
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
	if info.Mode().Perm() != filemode.PrivateFile {
		t.Fatalf("config mode = %o, want %o", info.Mode().Perm(), filemode.PrivateFile)
	}
	directory, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if directory.Mode().Perm() != filemode.PrivateDir {
		t.Fatalf("configuration directory mode = %o, want %o", directory.Mode().Perm(), filemode.PrivateDir)
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
	if err := atomicfile.Write(legacy, []byte(content), 0o600); err != nil {
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
		err := profile.Config.Validate()
		if err != nil {
			t.Errorf("profile %s: %v", profile.ID, err)
		}
	}
}

func TestValidateWANAddressing(t *testing.T) {
	t.Parallel()
	base := Default()
	base.WAN.VLANMAC = "00:11:22:33:44:55"
	if err := base.Validate(); err != nil {
		t.Fatalf("six-byte MAC rejected: %v", err)
	}
	eui64 := base
	eui64.WAN.VLANMAC = "00:11:22:33:44:55:66:77"
	if err := eui64.Validate(); !errors.Is(err, errVLANMAC) {
		t.Fatalf("eight-byte MAC accepted: %v", err)
	}
	both := Default()
	both.WAN.DHCP = true
	both.WAN.StaticAddress = "192.0.2.10/24"
	if err := both.Validate(); !errors.Is(err, errDHCPWithStatic) {
		t.Fatalf("DHCP with a static address accepted: %v", err)
	}
	NormalizeAddressing(&both)
	if both.WAN.StaticAddress != "" {
		t.Fatalf("static address survived normalization: %q", both.WAN.StaticAddress)
	}
	if err := both.Validate(); err != nil {
		t.Fatalf("normalized configuration rejected: %v", err)
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
	if err := atomicfile.Write(legacy, []byte(content), 0o600); err != nil {
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
	// A provider config starts with both lists populated, so this proves they
	// are separate storage rather than comparing against an empty slice.
	value := DefaultKPN()
	if len(value.WAN.NATDestinations) == 0 || len(value.Proxy.SourceRanges) == 0 {
		t.Fatal("provider profile lost its prefixes")
	}
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
	if err := atomicfile.Write(legacy, []byte(content), 0o600); err != nil {
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
			if err := atomicfile.Write(legacy, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			value, err := ImportLegacy(legacy)
			if err != nil {
				t.Fatal(err)
			}
			want := RoutesAllowDefault
			if noGateway != "" {
				want = RoutesNone
			}
			if value.WAN.DHCPRoutes != want {
				t.Fatalf("dhcpRoutes = %q for NO_GATEWAY=%q, want %q", value.WAN.DHCPRoutes, noGateway, want)
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
	if err := atomicfile.Write(legacy, []byte(content), 0o600); err != nil {
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

func TestImportLegacyAcceptsSingleQuotedAssignments(t *testing.T) {
	t.Parallel()
	legacy := filepath.Join(t.TempDir(), "udm-iptv.conf")
	content := `IPTV_WAN_INTERFACE='eth8'
IPTV_WAN_VLAN='4'
IPTV_WAN_VLAN_INTERFACE='iptv'
IPTV_WAN_RANGES='213.75.0.0/16 217.166.0.0/16 195.121.0.0/16'
IPTV_WAN_DHCP_OPTIONS='-O staticroutes -V IPTV_RG'
IPTV_LAN_INTERFACES='br0'
IPTV_IGMPPROXY_PROGRAM='improxy'
IPTV_IGMPPROXY_IGMP_VERSION='3'
`
	if err := atomicfile.Write(legacy, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := ImportLegacy(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if value.WAN.Interface != "eth8" || value.WAN.VLANInterface != "iptv" || value.WAN.VLAN != 4 {
		t.Fatalf("single-quoted import: %#v", value.WAN)
	}
	if !slices.Equal(value.WAN.DHCPOptions, []string{"-O", "staticroutes", "-V", "IPTV_RG"}) {
		t.Fatalf("DHCP options = %q", value.WAN.DHCPOptions)
	}
	if value.Profile != "kpn" {
		t.Fatalf("profile = %q, want kpn", value.Profile)
	}
}

func TestImportLegacyHonorsVLANGatewayOptOut(t *testing.T) {
	t.Parallel()
	for noGateway, want := range map[string]RoutePolicy{
		"iptv": RoutesNone, "eth8": RoutesNone, "": RoutesAllowDefault, "br0": RoutesAllowDefault,
	} {
		t.Run("NO_GATEWAY="+noGateway, func(t *testing.T) {
			legacy := filepath.Join(t.TempDir(), "udm-iptv.conf")
			content := `IPTV_WAN_INTERFACE="eth8"
IPTV_WAN_VLAN="4"
IPTV_WAN_VLAN_INTERFACE="iptv"
IPTV_WAN_DHCP="true"
IPTV_WAN_RANGES="213.75.0.0/16"
IPTV_STATIC_ROUTES="203.0.113.7 198.51.100.0/24"
IPTV_LAN_INTERFACES="br0"
IPTV_IGMPPROXY_PROGRAM="improxy"
IPTV_IGMPPROXY_IGMP_VERSION="3"
NO_GATEWAY="` + noGateway + `"
`
			if err := atomicfile.Write(legacy, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			value, err := ImportLegacy(legacy)
			if err != nil {
				t.Fatal(err)
			}
			if value.WAN.DHCPRoutes != want {
				t.Fatalf("dhcpRoutes = %q, want %q", value.WAN.DHCPRoutes, want)
			}
			if !slices.Equal(value.WAN.StaticRoutes, []string{"203.0.113.7/32", "198.51.100.0/24"}) {
				t.Fatalf("static routes = %q", value.WAN.StaticRoutes)
			}
		})
	}
}

func TestDecodeAcceptsLegacyAllowDefaultRoute(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, wan string
		want      RoutePolicy
	}{
		{"fallback allowed", `"allowDefaultRoute": true`, RoutesAllowDefault},
		{"fallback refused", `"allowDefaultRoute": false`, RoutesNoDefault},
		{"policy wins over the old key", `"allowDefaultRoute": true, "dhcpRoutes": "none"`, RoutesNone},
		{"neither key", `"vlan": 4`, RoutesNoDefault},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			data := []byte(`{"profile":"custom","wan":{"interface":"eth8","vlanInterface":"iptv",` + test.wan +
				`},"lan":{"interfaces":["br0"]},"proxy":{"program":"improxy","igmpVersion":3}}`)
			value, err := decodeConfig(data)
			if err != nil {
				t.Fatal(err)
			}
			if value.WAN.DHCPRoutes != test.want {
				t.Fatalf("dhcpRoutes = %q, want %q", value.WAN.DHCPRoutes, test.want)
			}
			if err := value.Validate(); err != nil {
				t.Fatalf("decoded configuration rejected: %v", err)
			}
		})
	}
}
