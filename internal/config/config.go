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

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/filemode"
)

// DefaultPath is the default location of the persisted configuration file.
const DefaultPath = "/data/udm-iptv/config.json"

const (
	// DefaultKPNVLAN is the IPTV VLAN ID KPN assigns in the Netherlands.
	DefaultKPNVLAN = 4
	// DefaultIGMPVersion picks IGMPv3, which works for most current receivers.
	DefaultIGMPVersion = 3
	// DefaultTraceRate samples one in ten operations for tracing.
	DefaultTraceRate = 0.1
	// hostPrefixBits is the /32 prefix length for a single IPv4 host address.
	hostPrefixBits = 32
)

var (
	errTraceRateRange       = errors.New("telemetry trace rate must be between 0 and 1")
	errWANInterfaceName     = errors.New("WAN interface must be a valid Linux interface name")
	errWANVLANRange         = errors.New("WAN VLAN must be between 0 and 4094")
	errVLANInterfaceName    = errors.New("VLAN interface must be a valid Linux interface name")
	errNoLANInterface       = errors.New("at least one LAN interface is required")
	errProxyProgram         = errors.New("proxy must be improxy or igmpproxy")
	errMissingProxySources  = errors.New("igmpproxy requires at least one proxy source range")
	errIGMPVersion          = errors.New("IGMP version must be 2 or 3")
	errVLANMAC              = errors.New("VLAN MAC address must be a six-byte Ethernet address")
	errStaticAddress        = errors.New("static address must be an IPv4 CIDR address")
	errDHCPWithStatic       = errors.New("DHCP and a static address are mutually exclusive")
	errRoutePolicy          = errors.New("DHCP route policy must be no-default, allow-default or none")
	errInvalidLANInterface  = errors.New("invalid LAN interface")
	errInvalidNetworkPrefix = errors.New("invalid network prefix")
	errNotAnInterfaceName   = errors.New("is not a valid Linux interface name")
)

// Config is the persisted configuration file format. It is a superset of the
// legacy shell configuration, which is imported and converted to this format.
type Config struct {
	Profile   string    `json:"profile"`
	WAN       WAN       `json:"wan"`
	LAN       LAN       `json:"lan"`
	Proxy     Proxy     `json:"proxy"`
	Telemetry Telemetry `json:"telemetry"`
}

type configJSON struct {
	Profile   string          `json:"profile"`
	WAN       WAN             `json:"wan"`
	LAN       LAN             `json:"lan"`
	Proxy     Proxy           `json:"proxy"`
	Telemetry json.RawMessage `json:"telemetry"`
}

// UnmarshalJSON accepts the allowDefaultRoute boolean that installations
// written before dhcpRoutes still carry on disk.
func (value *WAN) UnmarshalJSON(data []byte) error {
	type plain WAN
	legacy := struct {
		*plain

		AllowDefaultRoute *bool `json:"allowDefaultRoute"`
	}{plain: (*plain)(value)}
	if err := json.Unmarshal(data, &legacy); err != nil {
		return fmt.Errorf("parse WAN configuration: %w", err)
	}
	if value.DHCPRoutes == "" {
		value.DHCPRoutes = RoutesNoDefault
		if legacy.AllowDefaultRoute != nil && *legacy.AllowDefaultRoute {
			value.DHCPRoutes = RoutesAllowDefault
		}
	}

	return nil
}

func decodeConfig(data []byte) (Config, error) {
	var parsed configJSON
	if err := json.Unmarshal(data, &parsed); err != nil {
		return Config{}, fmt.Errorf("parse configuration: %w", err)
	}
	value := Config{Profile: parsed.Profile, WAN: parsed.WAN, LAN: parsed.LAN, Proxy: parsed.Proxy}
	if len(parsed.Telemetry) == 0 || string(parsed.Telemetry) == "null" {
		value.Telemetry = defaultTelemetry()

		return value, nil
	}
	telemetry, err := decodeTelemetry(parsed.Telemetry)
	if err != nil {
		return Config{}, err
	}
	value.Telemetry = telemetry

	return value, nil
}

func decodeTelemetry(data []byte) (Telemetry, error) {
	value := defaultTelemetry()
	type raw Telemetry
	if err := json.Unmarshal(data, (*raw)(&value)); err != nil {
		return Telemetry{}, fmt.Errorf("parse configuration: %w", err)
	}

	return value, nil
}

// Telemetry holds the configuration for telemetry collection and reporting.
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

// WAN holds the configuration for the WAN interface, including VLAN settings,
// DHCP options, static address, NAT destinations, and static routes.
type WAN struct {
	Interface       string      `json:"interface"`
	VLAN            int         `json:"vlan"`
	VLANInterface   string      `json:"vlanInterface"`
	VLANMAC         string      `json:"vlanMAC,omitempty"`
	DHCP            bool        `json:"dhcp"`
	DHCPOptions     []string    `json:"dhcpOptions,omitempty"`
	DHCPRoutes      RoutePolicy `json:"dhcpRoutes"`
	StaticAddress   string      `json:"staticAddress,omitempty"`
	NATDestinations []string    `json:"natDestinations,omitempty"`
	StaticRoutes    []string    `json:"staticRoutes,omitempty"`
}

// RoutePolicy selects which routes a DHCP lease may install on the IPTV path.
// Whether a default route is permitted is a policy about the route, not about
// the option that carried it: a server can advertise one through RFC3442
// option 121 or through the Router option, and the policy treats both alike.
type RoutePolicy string

const (
	// RoutesNoDefault installs the advertised RFC3442 routes except a default
	// route, and adds no Router-option default.
	RoutesNoDefault RoutePolicy = "no-default"
	// RoutesAllowDefault also installs a default route, whether the lease
	// advertises it through RFC3442 or through the Router option.
	RoutesAllowDefault RoutePolicy = "allow-default"
	// RoutesNone installs no route from a lease at all. udm-iptvd's NO_GATEWAY
	// returned before it read either option, so it suppressed the specific
	// routes too.
	RoutesNone RoutePolicy = "none"
)

// AllowsDefault reports whether a lease may install a default route.
func (policy RoutePolicy) AllowsDefault() bool { return policy == RoutesAllowDefault }

func (policy RoutePolicy) valid() bool {
	switch policy {
	case RoutesNoDefault, RoutesAllowDefault, RoutesNone:
		return true
	default:
		return false
	}
}

// LAN holds the configuration for the LAN interface, including the list of
// interfaces.
type LAN struct {
	Interfaces []string `json:"interfaces"`
}

// Proxy holds the configuration for the IGMP proxy, including the program
// choice, IGMP version, quick leave option, debug mode, and source ranges.
type Proxy struct {
	Program      string   `json:"program"`
	IGMPVersion  int      `json:"igmpVersion"`
	QuickLeave   bool     `json:"quickLeave"`
	Debug        bool     `json:"debug"`
	SourceRanges []string `json:"sourceRanges,omitempty"`
}

// genericBase holds the fallback values every profile inherits for fields it
// does not set itself: the physical interface, VLAN interface name, proxy
// choice and telemetry defaults.
func genericBase() Config {
	return Config{
		WAN:       WAN{Interface: "eth8", VLANInterface: "iptv", DHCPRoutes: RoutesNoDefault},
		LAN:       LAN{Interfaces: []string{"br0"}},
		Proxy:     Proxy{Program: "improxy", IGMPVersion: DefaultIGMPVersion},
		Telemetry: defaultTelemetry(),
	}
}

func defaultTelemetry() Telemetry {
	return Telemetry{Enabled: true, Errors: true, Logs: true, Metrics: true, Tracing: true, TraceRate: DefaultTraceRate, Presets: true, NetworkIdentity: true}
}

// Default is a configuration with no provider in it: the interface names and
// proxy settings every profile shares, and nothing a provider decides. Use it
// wherever the caller wants the shared defaults rather than a market.
func Default() Config {
	value := genericBase()
	value.Profile = profileCustom

	return value
}

// DefaultKPN is the kpn profile applied to the generic base. KPN is the
// primary market, so it seeds a fresh installation before the wizard runs.
// Callers that only want the shared defaults want Default instead.
func DefaultKPN() Config {
	kpn, _ := embedded().Profile("kpn")

	return kpn.Config
}

// Load reads and validates the configuration file at path.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read configuration: %w", err)
	}
	value, err := decodeConfig(data)
	if err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := value.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate %s: %w", path, err)
	}

	return value, nil
}

// Save validates value and atomically writes it to path. The directory is
// created owner-only, which atomicfile does not assume.
func Save(path string, value Config) error {
	if err := value.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode configuration: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), filemode.PrivateDir); err != nil {
		return fmt.Errorf("create configuration directory for %s: %w", path, err)
	}

	if err := atomicfile.Write(path, append(data, '\n'), filemode.PrivateFile); err != nil {
		return fmt.Errorf("write configuration: %w", err)
	}

	return nil
}

var configChecks = []func(Config) error{
	validateTelemetrySampling,
	validateWANLink,
	validateLANPresence,
	validateProxy,
	validateWANAddressing,
	validateLANNames,
	validatePrefixLists,
}

// Validate reports whether value is a consistent, applyable configuration.
func (value Config) Validate() error {
	for _, check := range configChecks {
		if err := check(value); err != nil {
			return err
		}
	}

	return nil
}

func validateTelemetrySampling(value Config) error {
	if !(value.Telemetry.TraceRate >= 0 && value.Telemetry.TraceRate <= 1) {
		return errTraceRateRange
	}

	return nil
}

func validateWANLink(value Config) error {
	if !validInterface(value.WAN.Interface) {
		return errWANInterfaceName
	}
	if value.WAN.VLAN < 0 || value.WAN.VLAN > 4094 {
		return errWANVLANRange
	}
	if value.WAN.VLAN > 0 && !validInterface(value.WAN.VLANInterface) {
		return errVLANInterfaceName
	}

	return nil
}

func validateLANPresence(value Config) error {
	if len(value.LAN.Interfaces) == 0 {
		return errNoLANInterface
	}

	return nil
}

func validateProxy(value Config) error {
	if value.Proxy.Program != "improxy" && value.Proxy.Program != "igmpproxy" {
		return errProxyProgram
	}
	if value.Proxy.Program == "igmpproxy" && len(value.Proxy.SourceRanges) == 0 {
		return errMissingProxySources
	}
	if value.Proxy.IGMPVersion != 2 && value.Proxy.IGMPVersion != 3 {
		return errIGMPVersion
	}

	return nil
}

func validateWANAddressing(value Config) error {
	if value.WAN.VLANMAC != "" {
		address, err := net.ParseMAC(value.WAN.VLANMAC)
		if err != nil || len(address) != 6 {
			return errVLANMAC
		}
	}
	if value.WAN.StaticAddress != "" {
		prefix, err := netip.ParsePrefix(value.WAN.StaticAddress)
		if err != nil || !prefix.Addr().Is4() {
			return errStaticAddress
		}
	}
	if value.WAN.DHCP && value.WAN.StaticAddress != "" {
		return errDHCPWithStatic
	}
	if !value.WAN.DHCPRoutes.valid() {
		return fmt.Errorf("%w %q", errRoutePolicy, value.WAN.DHCPRoutes)
	}

	return nil
}

// Target returns the interface that carries IPTV traffic: the VLAN when
// tagging is enabled, otherwise the physical uplink.
func (value Config) Target() string {
	if value.WAN.VLAN > 0 {
		return value.WAN.VLANInterface
	}

	return value.WAN.Interface
}

// NormalizeAddressing drops the static address once DHCP owns the interface,
// so switching the addressing mode stays valid.
func NormalizeAddressing(value *Config) {
	if value.WAN.DHCP {
		value.WAN.StaticAddress = ""
	}
}

func validateLANNames(value Config) error {
	for _, name := range value.LAN.Interfaces {
		if !validInterface(name) {
			return fmt.Errorf("%w %q", errInvalidLANInterface, name)
		}
	}

	return nil
}

func validatePrefixLists(value Config) error {
	for _, values := range [][]string{value.WAN.NATDestinations, value.Proxy.SourceRanges, value.WAN.StaticRoutes} {
		for _, prefix := range values {
			parsed, err := netip.ParsePrefix(prefix)
			if err != nil || !parsed.Addr().Is4() {
				return fmt.Errorf("%w %q", errInvalidNetworkPrefix, prefix)
			}
		}
	}

	return nil
}

var interfacePattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,15}$`)

func validInterface(name string) bool {
	return name != "." && name != ".." && interfacePattern.MatchString(name)
}

// Clone returns a copy whose slices no longer alias value's.
func (value Config) Clone() Config {
	value.WAN.DHCPOptions = slices.Clone(value.WAN.DHCPOptions)
	value.WAN.NATDestinations = slices.Clone(value.WAN.NATDestinations)
	value.WAN.StaticRoutes = slices.Clone(value.WAN.StaticRoutes)
	value.Proxy.SourceRanges = slices.Clone(value.Proxy.SourceRanges)
	value.LAN.Interfaces = slices.Clone(value.LAN.Interfaces)

	return value
}

// ValidateInterfaceName reports whether name is a usable Linux interface name.
func ValidateInterfaceName(name string) error {
	if !validInterface(name) {
		return fmt.Errorf("%q %w", name, errNotAnInterfaceName)
	}

	return nil
}

// Defaults the shell daemon applied to a variable the configuration file did
// not set. Its parameter expansions all use ${VAR:-…}, so an empty assignment
// and a missing line mean the same thing. Line numbers refer to udm-iptvd in
// the shell implementation.
const (
	// udm-iptvd:18 defaulted the VLAN to 0, meaning untagged IPTV.
	legacyVLAN = 0
	// udm-iptvd:12 defaulted the VLAN interface name.
	legacyVLANInterface = "iptv"
	// udm-iptvd:24 and :185 selected igmpproxy for anything but "improxy".
	legacyProxyProgram = "igmpproxy"
	// udm-iptvd:154 emitted IGMPv3 only for a literal 3, IGMPv2 otherwise.
	legacyIGMPVersion = 2
)

// legacyDHCPOptions is the udhcpc argument list udm-iptvd:13 hardcoded.
var legacyDHCPOptions = []string{"-O", "staticroutes", "-V", "IPTV_RG"}

// legacyBase is what the shell daemon ran with when the configuration file
// set nothing. Where the shell's own fallback was an empty string that could
// not run at all, this keeps the value the packaging offered: udm-iptvd:17
// left the WAN interface empty, which fails on ip link, and :22 left the
// downstream list empty, which disables every downstream.
func legacyBase() Config {
	value := genericBase()
	value.Profile = profileLegacy
	value.WAN.VLAN = legacyVLAN
	value.WAN.VLANInterface = legacyVLANInterface
	value.Proxy.Program = legacyProxyProgram
	value.Proxy.IGMPVersion = legacyIGMPVersion

	return value
}

// applyLegacyWAN reproduces udm-iptvd's uplink behaviour. udhcpc only ever
// ran inside the VLAN branch at :46, so an untagged installation performed no
// DHCP whatever the variable said, and :80 applied the static address only
// when DHCP was disabled by name.
func applyLegacyWAN(value *Config, values map[string]string) {
	value.WAN.Interface = fallback(values["IPTV_WAN_INTERFACE"], value.WAN.Interface)
	if vlan, err := strconv.Atoi(values["IPTV_WAN_VLAN"]); err == nil {
		value.WAN.VLAN = vlan
	}
	value.WAN.VLANInterface = fallback(values["IPTV_WAN_VLAN_INTERFACE"], value.WAN.VLANInterface)
	value.WAN.VLANMAC = values["IPTV_WAN_VLAN_MAC"]
	disabled := values["IPTV_WAN_DHCP"] == "false"
	value.WAN.DHCP = !disabled && value.WAN.VLAN > 0
	if value.WAN.DHCP {
		value.WAN.DHCPOptions = strings.Fields(fallback(values["IPTV_WAN_DHCP_OPTIONS"], strings.Join(legacyDHCPOptions, " ")))
	}
	if disabled {
		value.WAN.StaticAddress = values["IPTV_WAN_STATIC_IP"]
	}
	value.WAN.DHCPRoutes = legacyRoutePolicy(*value, values["NO_GATEWAY"])
	value.WAN.NATDestinations = normalizeLegacyPrefixes(strings.Fields(values["IPTV_WAN_RANGES"]))
	value.WAN.StaticRoutes = normalizeLegacyPrefixes(strings.Fields(values["IPTV_STATIC_ROUTES"]))
	value.LAN.Interfaces = strings.Fields(fallback(values["IPTV_LAN_INTERFACES"], "br0"))
}

// applyLegacyProxy reproduces udm-iptvd's proxy selection. Quickleave was
// asymmetric: :129 enabled it for igmpproxy unless the variable said false,
// while :162 enabled it for improxy only when the variable said false.
func applyLegacyProxy(value *Config, values map[string]string) {
	value.Proxy.Program = fallback(values["IPTV_IGMPPROXY_PROGRAM"], legacyProxyProgram)
	if value.Proxy.Program != "improxy" {
		value.Proxy.Program = legacyProxyProgram
	}
	value.Proxy.IGMPVersion = legacyIGMPVersion
	if values["IPTV_IGMPPROXY_IGMP_VERSION"] == "3" {
		value.Proxy.IGMPVersion = DefaultIGMPVersion
	}
	quickleave := values["IPTV_IGMPPROXY_DISABLE_QUICKLEAVE"]
	value.Proxy.QuickLeave = quickleave == "false" || (quickleave == "" && value.Proxy.Program == legacyProxyProgram)
	value.Proxy.Debug = values["IPTV_IGMPPROXY_DEBUG"] == "true"
}

// ImportLegacy converts the former shell configuration without executing it.
func ImportLegacy(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read legacy configuration: %w", err)
	}
	values, err := parseLegacyAssignments(string(data))
	if err != nil {
		return Config{}, err
	}
	value := legacyBase()
	applyLegacyWAN(&value, values)
	applyLegacyProxy(&value, values)
	legacyLANSources := normalizeLegacyPrefixes(strings.Fields(values["IPTV_LAN_RANGES"]))
	// Preserve old igmpproxy semantics during migration; new configurations keep
	// source allowlists separate from NAT destinations.
	if value.Proxy.Program == "igmpproxy" {
		value.Proxy.SourceRanges = mergePrefixes(value.WAN.NATDestinations, legacyLANSources)
	}
	if profile, found := InferLegacyProfile(value); found {
		value.Profile = profile
		known, _ := embedded().Profile(profile)
		value.WAN.NATDestinations = append([]string(nil), known.Config.WAN.NATDestinations...)
		value.Proxy.SourceRanges = mergePrefixes(known.Config.Proxy.SourceRanges, legacyLANSources)
	}

	return value, value.Validate()
}

func parseLegacyAssignments(text string) (map[string]string, error) {
	values := map[string]string{}
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, raw, ok := strings.Cut(line, "=")
		if !ok || !legacyKey(key) {
			continue
		}
		decoded, err := decodeLegacyValue(key, strings.TrimSpace(raw))
		if err != nil {
			return nil, err
		}
		values[key] = decoded
	}

	return values, nil
}

func legacyKey(key string) bool {
	return strings.HasPrefix(key, "IPTV_") || key == "NO_GATEWAY"
}

// udm-iptvd's hook returned before it read option 121 or the Router option, so
// a listed interface installed no route at all.
func legacyRoutePolicy(value Config, noGateway string) RoutePolicy {
	switch {
	case legacyGatewayOptOut(value, noGateway):
		return RoutesNone
	case value.WAN.DHCP:
		return RoutesAllowDefault
	default:
		return RoutesNoDefault
	}
}

// udhcpc ran on the VLAN when tagging was enabled, so the legacy hook saw that
// name rather than the physical uplink. Either spelling opts out.
func legacyGatewayOptOut(value Config, noGateway string) bool {
	names := strings.Fields(noGateway)

	return slices.Contains(names, value.WAN.Interface) || slices.Contains(names, value.Target())
}

// Legacy configuration was sourced by /bin/sh, where single quotes suppress
// every expansion and escape.
func decodeLegacyValue(key, raw string) (string, error) {
	if quotedLegacyValue(raw, '\'') {
		return raw[1 : len(raw)-1], nil
	}
	if !quotedLegacyValue(raw, '"') {
		return raw, nil
	}
	decoded, err := strconv.Unquote(raw)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", key, err)
	}

	return decoded, nil
}

func quotedLegacyValue(raw string, quote byte) bool {
	return len(raw) >= 2 && raw[0] == quote && raw[len(raw)-1] == quote
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
			value = netip.PrefixFrom(address, hostPrefixBits).String()
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
