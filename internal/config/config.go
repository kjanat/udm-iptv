package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const DefaultPath = "/data/udm-iptv/config.json"

type Config struct {
	Profile   string    `json:"profile"`
	WAN       WAN       `json:"wan"`
	LAN       LAN       `json:"lan"`
	Proxy     Proxy     `json:"proxy"`
	Telemetry Telemetry `json:"telemetry"`
}

type Telemetry struct {
	Presets         bool    `json:"presets"`
	NetworkIdentity bool    `json:"networkIdentity"`
	Enabled         bool    `json:"enabled"`
	Errors          bool    `json:"errors"`
	Logs            bool    `json:"logs"`
	Metrics         bool    `json:"metrics"`
	Tracing         bool    `json:"tracing"`
	TraceRate       float64 `json:"traceRate"`
}

type WAN struct {
	Interface         string   `json:"interface"`
	VLAN              int      `json:"vlan"`
	VLANInterface     string   `json:"vlanInterface"`
	VLANMAC           string   `json:"vlanMAC,omitempty"`
	DHCP              bool     `json:"dhcp"`
	DHCPOptions       []string `json:"dhcpOptions,omitempty"`
	AllowDefaultRoute bool     `json:"allowDefaultRoute"`
	StaticAddress     string   `json:"staticAddress,omitempty"`
	NATDestinations   []string `json:"natDestinations,omitempty"`
	StaticRoutes      []string `json:"staticRoutes,omitempty"`
}

type LAN struct {
	Interfaces []string `json:"interfaces"`
}

type Proxy struct {
	Program      string   `json:"program"`
	IGMPVersion  int      `json:"igmpVersion"`
	QuickLeave   bool     `json:"quickLeave"`
	Debug        bool     `json:"debug"`
	SourceRanges []string `json:"sourceRanges,omitempty"`
}

func Default() Config {
	return Config{
		Profile: "kpn",
		WAN: WAN{
			Interface: "eth8", VLAN: 4, VLANInterface: "iptv", DHCP: true,
			DHCPOptions:     []string{"-O", "staticroutes", "-V", "IPTV_RG"},
			NATDestinations: []string{"213.75.0.0/16", "217.166.0.0/16", "195.121.0.0/16"},
		},
		LAN:       LAN{Interfaces: []string{"br0"}},
		Proxy:     Proxy{Program: "improxy", IGMPVersion: 3},
		Telemetry: Telemetry{Enabled: true, Errors: true, Logs: true, Metrics: true, Tracing: true, TraceRate: 0.1, Presets: true, NetworkIdentity: true},
	}
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var value Config
	if err := json.Unmarshal(data, &value); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := value.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate %s: %w", path, err)
	}

	return value, nil
}

func Save(path string, value Config) error {
	if err := value.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".config-*.json")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()

		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()

		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()

		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}

	return os.Rename(name, path)
}

func (value Config) Validate() error {
	if !(value.Telemetry.TraceRate >= 0 && value.Telemetry.TraceRate <= 1) {
		return errors.New("telemetry trace rate must be between 0 and 1")
	}
	if !validInterface(value.WAN.Interface) {
		return errors.New("WAN interface must be a valid Linux interface name")
	}
	if value.WAN.VLAN < 0 || value.WAN.VLAN > 4094 {
		return errors.New("WAN VLAN must be between 0 and 4094")
	}
	if value.WAN.VLAN > 0 && !validInterface(value.WAN.VLANInterface) {
		return errors.New("VLAN interface must be a valid Linux interface name")
	}
	if len(value.LAN.Interfaces) == 0 {
		return errors.New("at least one LAN interface is required")
	}
	if value.Proxy.Program != "improxy" && value.Proxy.Program != "igmpproxy" {
		return errors.New("proxy must be improxy or igmpproxy")
	}
	if value.Proxy.Program == "igmpproxy" && len(value.Proxy.SourceRanges) == 0 {
		return errors.New("igmpproxy requires at least one proxy source range")
	}
	if value.Proxy.IGMPVersion != 2 && value.Proxy.IGMPVersion != 3 {
		return errors.New("IGMP version must be 2 or 3")
	}
	if value.WAN.VLANMAC != "" {
		if _, err := net.ParseMAC(value.WAN.VLANMAC); err != nil {
			return errors.New("VLAN MAC address is invalid")
		}
	}
	if value.WAN.StaticAddress != "" {
		prefix, err := netip.ParsePrefix(value.WAN.StaticAddress)
		if err != nil || !prefix.Addr().Is4() {
			return errors.New("static address must be an IPv4 CIDR address")
		}
	}
	for _, name := range value.LAN.Interfaces {
		if !validInterface(name) {
			return fmt.Errorf("invalid LAN interface %q", name)
		}
	}
	for _, values := range [][]string{value.WAN.NATDestinations, value.Proxy.SourceRanges, value.WAN.StaticRoutes} {
		for _, prefix := range values {
			parsed, err := netip.ParsePrefix(prefix)
			if err != nil || !parsed.Addr().Is4() {
				return fmt.Errorf("invalid network prefix %q", prefix)
			}
		}
	}

	return nil
}

var interfacePattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,15}$`)

func validInterface(name string) bool {
	return name != "." && name != ".." && interfacePattern.MatchString(name)
}

// ImportLegacy converts the former shell configuration without executing it.
func ImportLegacy(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	values := map[string]string{}
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, raw, ok := strings.Cut(line, "=")
		if !ok || (!strings.HasPrefix(key, "IPTV_") && key != "NO_GATEWAY") {
			continue
		}
		raw = strings.TrimSpace(raw)
		if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
			decoded, decodeErr := strconv.Unquote(raw)
			if decodeErr != nil {
				return Config{}, fmt.Errorf("parse %s: %w", key, decodeErr)
			}
			raw = decoded
		}
		values[key] = raw
	}
	value := Default()
	// Legacy installations never selected telemetry; do not opt them in on migration.
	value.Telemetry.Enabled = false
	value.Profile = "legacy"
	value.WAN.Interface = fallback(values["IPTV_WAN_INTERFACE"], value.WAN.Interface)
	if vlan, parseErr := strconv.Atoi(fallback(values["IPTV_WAN_VLAN"], "4")); parseErr == nil {
		value.WAN.VLAN = vlan
	}
	value.WAN.VLANInterface = fallback(values["IPTV_WAN_VLAN_INTERFACE"], value.WAN.VLANInterface)
	value.WAN.VLANMAC = values["IPTV_WAN_VLAN_MAC"]
	value.WAN.DHCP = values["IPTV_WAN_DHCP"] != "false"
	if values["IPTV_WAN_DHCP"] == "" && value.WAN.VLAN == 0 {
		value.WAN.DHCP = false
	}
	if options, found := values["IPTV_WAN_DHCP_OPTIONS"]; found {
		value.WAN.DHCPOptions = strings.Fields(options)
	}
	value.WAN.AllowDefaultRoute = value.WAN.DHCP && !slicesContains(strings.Fields(values["NO_GATEWAY"]), value.WAN.Interface)
	value.WAN.StaticAddress = values["IPTV_WAN_STATIC_IP"]
	if destinations, found := values["IPTV_WAN_RANGES"]; found {
		value.WAN.NATDestinations = normalizeLegacyPrefixes(strings.Fields(destinations))
	}
	value.WAN.StaticRoutes = strings.Fields(values["IPTV_STATIC_ROUTES"])
	value.LAN.Interfaces = strings.Fields(fallback(values["IPTV_LAN_INTERFACES"], "br0"))
	value.Proxy.Program = fallback(values["IPTV_IGMPPROXY_PROGRAM"], "igmpproxy")
	if version, parseErr := strconv.Atoi(fallback(values["IPTV_IGMPPROXY_IGMP_VERSION"], "3")); parseErr == nil {
		value.Proxy.IGMPVersion = version
	}
	value.Proxy.QuickLeave = values["IPTV_IGMPPROXY_DISABLE_QUICKLEAVE"] == "false"
	value.Proxy.Debug = values["IPTV_IGMPPROXY_DEBUG"] == "true"
	legacyLANSources := normalizeLegacyPrefixes(strings.Fields(values["IPTV_LAN_RANGES"]))
	// Preserve old igmpproxy semantics during migration; new configurations keep
	// source allowlists separate from NAT destinations.
	if value.Proxy.Program == "igmpproxy" {
		value.Proxy.SourceRanges = mergePrefixes(value.WAN.NATDestinations, legacyLANSources)
	}
	if profile, found := InferLegacyProfile(value); found {
		value.Profile = profile
		known := profiles[profile].Config
		value.WAN.NATDestinations = append([]string(nil), known.WAN.NATDestinations...)
		value.Proxy.SourceRanges = mergePrefixes(known.Proxy.SourceRanges, legacyLANSources)
	}

	return value, value.Validate()
}

func fallback(value, defaultValue string) string {
	if value != "" {
		return value
	}

	return defaultValue
}

func normalizeLegacyPrefixes(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if address, err := netip.ParseAddr(value); err == nil && address.Is4() {
			value = netip.PrefixFrom(address, 32).String()
		}
		result = append(result, value)
	}

	return result
}

func mergePrefixes(groups ...[]string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, values := range groups {
		for _, value := range values {
			if !seen[value] {
				seen[value] = true
				result = append(result, value)
			}
		}
	}

	return result
}

func slicesContains(values []string, wanted string) bool {
	return slices.Contains(values, wanted)
}
