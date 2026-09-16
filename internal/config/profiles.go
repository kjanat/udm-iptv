package config

import (
	"fmt"
	"slices"
	"sort"
)

// Profile represents a provider profile with its ID, name, note,
// and configuration.
type Profile struct {
	ID     string
	Name   string
	Note   string
	Config Config
}

// profileDefinition holds the definition of a provider profile,
// including its name, WAN configuration, source ranges,
// and an optional note.
type profileDefinition struct {
	Name    string
	WAN     WAN
	Sources []string
	Note    string
}

const (
	// vlanVivoSP is the IPTV VLAN Vivo assigns in São Paulo, Brazil.
	vlanVivoSP = 20
	// vlanVivoGVT is the IPTV VLAN Vivo assigns on its former GVT network.
	vlanVivoGVT = 4000
)

var profileDefinitions = map[string]profileDefinition{
	"bt": {
		Name: "BT (GB)",
		WAN: WAN{
			VLAN: 0, DHCP: false, StaticAddress: "10.20.30.1/24",
			NATDestinations: []string{"109.159.247.0/24"},
		},
		Sources: []string{"224.0.0.0/4", "109.159.247.0/24"},
	},
	"init7": {
		Name: "Init7 (CH)",
		WAN: WAN{
			VLAN: 0, DHCP: false, NATDestinations: []string{"77.109.128.0/19"},
		},
		Sources: []string{"224.0.0.0/8", "239.77.0.0/16", "77.109.128.0/19", "233.50.230.0/24"},
	},
	"kpn": {
		Name: "KPN / XS4ALL / Freedom (NL)",
		WAN: WAN{
			VLAN: DefaultKPNVLAN, DHCP: true, DHCPOptions: kpnDHCPOptions,
			NATDestinations: kpnNATDestinations,
		},
		Sources: kpnNATDestinations,
	},
	"magentatv": {
		Name: "MagentaTV (DE)",
		WAN: WAN{
			Interface: "ppp0", VLAN: 0, DHCP: false,
			NATDestinations: []string{"87.141.0.0/16", "193.158.0.0/15"},
		},
		Sources: []string{"224.0.0.0/4", "87.141.0.0/16", "193.158.0.0/15"},
	},
	"meo": {
		Name: "MEO (PT)",
		WAN: WAN{
			VLAN: 0, DHCP: false,
			NATDestinations: []string{"10.159.0.0/16", "10.173.0.0/16", "194.65.46.0/23", "213.13.16.0/20"},
		},
		Sources: []string{"10.159.0.0/16", "10.173.0.0/16", "194.65.46.0/23", "213.13.16.0/20", "224.0.0.0/4"},
	},
	"posttv": {
		Name: "PostTV (LU)",
		WAN: WAN{
			Interface: "eth8.35", VLAN: 0, DHCP: false, StaticAddress: "10.10.10.10/32",
			NATDestinations: []string{"172.19.9.0/24"},
		},
		Sources: []string{"172.19.9.0/24"},
	},
	"solcon": {
		Name: "Solcon (NL)",
		WAN: WAN{
			VLAN: DefaultKPNVLAN, DHCP: true, DHCPOptions: kpnDHCPOptions,
			NATDestinations: []string{"10.0.0.0/8", "10.252.0.0/16", "10.253.0.0/16", "217.166.0.0/16"},
		},
		Sources: []string{"10.0.0.0/8", "10.252.0.0/16", "10.253.0.0/16", "217.166.0.0/16"},
	},
	"swisscom": {
		Name: "Swisscom (CH)",
		WAN: WAN{
			VLAN: 0, DHCP: false, NATDestinations: []string{"195.186.0.0/16", "213.3.72.0/24"},
		},
		Sources: []string{"195.186.0.0/16", "213.3.72.0/24", "224.0.0.0/4"},
	},
	"telenor": {
		Name: "Telenor (NO)",
		WAN: WAN{
			VLAN: 0, DHCP: false, NATDestinations: []string{"93.91.111.0/24", "148.122.7.125/32"},
		},
		Sources: []string{"224.0.0.0/4", "93.91.111.0/24", "148.122.7.125/32"},
	},
	"tweak": {
		Name: "Tweak (NL)",
		WAN: WAN{
			VLAN: DefaultKPNVLAN, DHCP: true, DHCPOptions: []string{"-O", "staticroutes"}, NATDestinations: []string{"0.0.0.0/0"},
		},
		Sources: []string{"0.0.0.0/0"},
	},
	"vivo": {
		Name: "Vivo SP (BR)",
		WAN: WAN{
			VLAN: vlanVivoSP, DHCP: true,
			NATDestinations: []string{"172.28.0.0/14", "201.0.52.0/23", "200.161.71.0/24", "177.16.0.0/16"},
		},
		Sources: []string{"172.28.0.0/14", "201.0.52.0/23", "200.161.71.0/24", "177.16.0.0/16"},
		Note:    "IPTV DNS servers: 177.16.30.67 and 177.16.30.7.",
	},
	"vivogvt": {
		Name: "Vivo GVT (BR)",
		WAN: WAN{
			VLAN: vlanVivoGVT, DHCP: false, StaticAddress: "10.0.0.1/32", NATDestinations: []string{"0.0.0.0/0"},
		},
		Sources: []string{"0.0.0.0/0"},
	},
}

var profiles = buildProfiles()

func buildProfiles() map[string]Profile {
	result := make(map[string]Profile, len(profileDefinitions))
	for id, definition := range profileDefinitions {
		result[id] = definition.resolve(id)
	}

	return result
}

func (definition profileDefinition) resolve(id string) Profile {
	base := genericBase()
	base.Profile = id
	wan := definition.WAN
	if wan.Interface == "" {
		wan.Interface = base.WAN.Interface
	}
	if wan.VLANInterface == "" {
		wan.VLANInterface = base.WAN.VLANInterface
	}
	base.WAN = wan
	base.Proxy.SourceRanges = definition.Sources

	return Profile{ID: id, Name: definition.Name, Note: definition.Note, Config: base}
}

// Profiles returns a sorted list of all available provider profiles, including
// the "Custom" profile.
func Profiles() []Profile {
	result := make([]Profile, 0, 1+len(profiles))
	result = append(result, Profile{ID: "custom", Name: "Custom", Config: Default()})
	for _, value := range profiles {
		result = append(result, value)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Name < result[right].Name })

	return result
}

// ProfileByID returns the provider profile corresponding to the specified ID.
// If the ID is "custom" or "legacy", it returns a profile with the name "Custom".
// If the ID corresponds to a known profile, it returns that profile.
// If the ID is unknown, it returns false.
func ProfileByID(id string) (Profile, bool) {
	if id == "custom" || id == "legacy" {
		return Profile{ID: id, Name: "Custom"}, true
	}
	value, found := profiles[id]

	return value, found
}

// FromProfile returns a configuration based on the specified provider profile ID.
// If the ID is "custom" or "legacy", it returns the provided current configuration.
// If the ID corresponds to a known profile, it returns the configuration for that profile,
// preserving the telemetry setting from the current configuration.
// If the ID is unknown, it returns an error.
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
