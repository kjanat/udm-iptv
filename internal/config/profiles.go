package config

import (
	"fmt"
	"slices"
	"sort"
)

type Profile struct {
	ID     string
	Name   string
	Note   string
	Config Config
}

var profiles = map[string]Profile{
	"bt": profile("bt", "BT (GB)", WAN{
		VLAN: 0, DHCP: false, StaticAddress: "10.20.30.1/24",
		NATDestinations: []string{"109.159.247.0/24"},
	}, []string{"224.0.0.0/4", "109.159.247.0/24"}, ""),
	"init7": profile("init7", "Init7 (CH)", WAN{
		VLAN: 0, DHCP: false, NATDestinations: []string{"77.109.128.0/19"},
	}, []string{"224.0.0.0/8", "239.77.0.0/16", "77.109.128.0/19", "233.50.230.0/24"}, ""),
	"kpn": profile("kpn", "KPN / XS4ALL / Freedom (NL)", WAN{
		VLAN: 4, DHCP: true, DHCPOptions: []string{"-O", "staticroutes", "-V", "IPTV_RG"},
		NATDestinations: []string{"213.75.0.0/16", "217.166.0.0/16", "195.121.0.0/16"},
	}, []string{"213.75.0.0/16", "217.166.0.0/16", "195.121.0.0/16"}, ""),
	"magentatv": profile("magentatv", "MagentaTV (DE)", WAN{
		Interface: "ppp0", VLAN: 0, DHCP: false,
		NATDestinations: []string{"87.141.0.0/16", "193.158.0.0/15"},
	}, []string{"224.0.0.0/4", "87.141.0.0/16", "193.158.0.0/15"}, ""),
	"meo": profile("meo", "MEO (PT)", WAN{
		VLAN: 0, DHCP: false,
		NATDestinations: []string{"10.159.0.0/16", "10.173.0.0/16", "194.65.46.0/23", "213.13.16.0/20"},
	}, []string{"10.159.0.0/16", "10.173.0.0/16", "194.65.46.0/23", "213.13.16.0/20", "224.0.0.0/4"}, ""),
	"posttv": profile("posttv", "PostTV (LU)", WAN{
		Interface: "eth8.35", VLAN: 0, DHCP: false, StaticAddress: "10.10.10.10/32",
		NATDestinations: []string{"172.19.9.0/24"},
	}, []string{"172.19.9.0/24"}, ""),
	"solcon": profile("solcon", "Solcon (NL)", WAN{
		VLAN: 4, DHCP: true, DHCPOptions: []string{"-O", "staticroutes", "-V", "IPTV_RG"},
		NATDestinations: []string{"10.0.0.0/8", "10.252.0.0/16", "10.253.0.0/16", "217.166.0.0/16"},
	}, []string{"10.0.0.0/8", "10.252.0.0/16", "10.253.0.0/16", "217.166.0.0/16"}, ""),
	"swisscom": profile("swisscom", "Swisscom (CH)", WAN{
		VLAN: 0, DHCP: false, NATDestinations: []string{"195.186.0.0/16", "213.3.72.0/24"},
	}, []string{"195.186.0.0/16", "213.3.72.0/24", "224.0.0.0/4"}, ""),
	"telenor": profile("telenor", "Telenor (NO)", WAN{
		VLAN: 0, DHCP: false, NATDestinations: []string{"93.91.111.0/24", "148.122.7.125/32"},
	}, []string{"224.0.0.0/4", "93.91.111.0/24", "148.122.7.125/32"}, ""),
	"tweak": profile("tweak", "Tweak (NL)", WAN{
		VLAN: 4, DHCP: true, DHCPOptions: []string{"-O", "staticroutes"}, NATDestinations: []string{"0.0.0.0/0"},
	}, []string{"0.0.0.0/0"}, ""),
	"vivo": profile("vivo", "Vivo SP (BR)", WAN{
		VLAN: 20, DHCP: true,
		NATDestinations: []string{"172.28.0.0/14", "201.0.52.0/23", "200.161.71.0/24", "177.16.0.0/16"},
	}, []string{"172.28.0.0/14", "201.0.52.0/23", "200.161.71.0/24", "177.16.0.0/16"}, "Set DNS servers 177.16.30.67 and 177.16.30.7 for the internal IPTV network."),
	"vivogvt": profile("vivogvt", "Vivo GVT (BR)", WAN{
		VLAN: 4000, DHCP: false, StaticAddress: "10.0.0.1/32", NATDestinations: []string{"0.0.0.0/0"},
	}, []string{"0.0.0.0/0"}, ""),
}

func profile(id, name string, wan WAN, proxySources []string, note string) Profile {
	base := Default()
	base.Profile = id
	if wan.Interface == "" {
		wan.Interface = base.WAN.Interface
	}
	if wan.VLANInterface == "" {
		wan.VLANInterface = base.WAN.VLANInterface
	}
	base.WAN = wan
	base.Proxy.SourceRanges = proxySources
	return Profile{ID: id, Name: name, Note: note, Config: base}
}

func Profiles() []Profile {
	result := []Profile{{ID: "custom", Name: "Custom", Config: Default()}}
	for _, value := range profiles {
		result = append(result, value)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Name < result[right].Name })
	return result
}

func ProfileByID(id string) (Profile, bool) {
	if id == "custom" || id == "legacy" {
		return Profile{ID: id, Name: "Custom"}, true
	}
	value, found := profiles[id]
	return value, found
}

func FromProfile(id string, current Config) (Config, error) {
	if id == "custom" || id == "legacy" {
		current.Profile = id
		return current, nil
	}
	if value, found := profiles[id]; found {
		value.Config.Telemetry = current.Telemetry
		return value.Config, nil
	}
	return Config{}, fmt.Errorf("unknown provider profile %q", id)
}

// InferLegacyProfile recognizes a provider only when every provider-specific
// value matches. Hardware-dependent interface names and user-selectable proxy
// settings intentionally do not participate in the match.
func InferLegacyProfile(value Config) (string, bool) {
	match := ""
	for id, candidate := range profiles {
		if value.WAN.VLAN == candidate.Config.WAN.VLAN &&
			value.WAN.DHCP == candidate.Config.WAN.DHCP &&
			value.WAN.StaticAddress == candidate.Config.WAN.StaticAddress &&
			slices.Equal(value.WAN.DHCPOptions, candidate.Config.WAN.DHCPOptions) &&
			slices.Equal(value.WAN.NATDestinations, candidate.Config.Proxy.SourceRanges) {
			if match != "" {
				return "", false
			}
			match = id
		}
	}
	return match, match != ""
}
