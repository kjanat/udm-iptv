package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-iptables/iptables"
	systemd "github.com/coreos/go-systemd/v22/dbus"
	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/network"
	"github.com/spf13/cobra"
	"github.com/vishvananda/netlink"
)

type snapshot struct {
	Timestamp time.Time     `json:"timestamp"`
	Version   string        `json:"version"`
	Config    configSummary `json:"config"`
	Service   serviceStatus `json:"service"`
	Network   networkStatus `json:"network"`
	Multicast multicastInfo `json:"multicast"`
	NAT       []string      `json:"natRules"`
}

type configSummary struct {
	Profile           string   `json:"profile"`
	WANInterface      string   `json:"wanInterface"`
	VLAN              int      `json:"vlan"`
	IPTVInterface     string   `json:"iptvInterface"`
	CustomMAC         bool     `json:"customMAC"`
	DHCP              bool     `json:"dhcp"`
	DHCPOptions       bool     `json:"dhcpOptionsConfigured"`
	StaticAddress     bool     `json:"staticAddressConfigured"`
	AllowDefaultRoute bool     `json:"allowDefaultRoute"`
	NATDestinations   []string `json:"natDestinations"`
	LANInterfaces     []string `json:"lanInterfaces"`
	Proxy             string   `json:"proxy"`
	IGMPVersion       int      `json:"igmpVersion"`
	QuickLeave        bool     `json:"quickLeave"`
	Debug             bool     `json:"debug"`
	ProxySourceRanges []string `json:"proxySourceRanges"`
}

type serviceStatus struct {
	LoadState   string `json:"loadState"`
	ActiveState string `json:"activeState"`
	SubState    string `json:"subState"`
	UnitFile    string `json:"unitFileState"`
	Restarts    uint64 `json:"restarts"`
	Proxy       string `json:"proxy,omitempty"`
	ProxyPID    int    `json:"proxyPID,omitempty"`
}

type networkStatus struct {
	Target       string   `json:"target"`
	LinkState    string   `json:"linkState"`
	AddressCount int      `json:"addressCount"`
	Routes       []string `json:"routes"`
	DefaultRoute bool     `json:"defaultRoute"`
}

type multicastInfo struct {
	Routes  int    `json:"routes"`
	Packets uint64 `json:"packets"`
}

func (application *Application) statusCommand() *cobra.Command {
	var outputJSON bool
	command := &cobra.Command{
		Use: "status", Short: "Show the current IPTV state", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			value, err := application.snapshot(command.Context())
			if err != nil {
				return err
			}
			if outputJSON {
				encoder := json.NewEncoder(application.Out)
				encoder.SetIndent("", "  ")
				return encoder.Encode(value)
			}
			return writeString(application.Out, renderSnapshot(value))
		},
	}
	command.Flags().BoolVar(&outputJSON, "json", false, "write structured JSON")
	return command
}

func (application *Application) snapshot(ctx context.Context) (snapshot, error) {
	value, err := config.Load(application.ConfigPath)
	if err != nil {
		return snapshot{}, err
	}
	result := snapshot{Timestamp: time.Now().UTC(), Version: application.Version, Config: configSummary{
		Profile: value.Profile, WANInterface: value.WAN.Interface, VLAN: value.WAN.VLAN, IPTVInterface: value.WAN.VLANInterface,
		CustomMAC: value.WAN.VLANMAC != "", DHCP: value.WAN.DHCP, DHCPOptions: len(value.WAN.DHCPOptions) > 0, StaticAddress: value.WAN.StaticAddress != "",
		AllowDefaultRoute: value.WAN.AllowDefaultRoute, NATDestinations: sanitizePrefixes(value.WAN.NATDestinations),
		LANInterfaces: value.LAN.Interfaces, Proxy: value.Proxy.Program, IGMPVersion: value.Proxy.IGMPVersion,
		QuickLeave: value.Proxy.QuickLeave, Debug: value.Proxy.Debug, ProxySourceRanges: sanitizePrefixes(value.Proxy.SourceRanges),
	}}
	result.Network.Target = network.Target(value)
	if link, linkErr := netlink.LinkByName(result.Network.Target); linkErr == nil {
		result.Network.LinkState = link.Attrs().OperState.String()
		if addresses, addressErr := netlink.AddrList(link, netlink.FAMILY_V4); addressErr == nil {
			result.Network.AddressCount = len(addresses)
		}
		if routes, routeErr := netlink.RouteList(link, netlink.FAMILY_V4); routeErr == nil {
			for _, route := range routes {
				destination := "default"
				if route.Dst != nil {
					destination = route.Dst.String()
				}
				if destination == "default" || destination == "0.0.0.0/0" {
					result.Network.DefaultRoute = true
				}
				result.Network.Routes = append(result.Network.Routes, sanitize(destination))
			}
			sort.Strings(result.Network.Routes)
		}
	}
	if state, stateErr := readRuntimeState(); stateErr == nil {
		result.Service.Proxy = state.Proxy
		result.Service.ProxyPID = state.ProxyPID
	}
	if connection, connectionErr := systemd.NewSystemConnectionContext(ctx); connectionErr == nil {
		defer connection.Close()
		if properties, propertyErr := connection.GetUnitPropertiesContext(ctx, "udm-iptv.service"); propertyErr == nil {
			result.Service.LoadState, _ = properties["LoadState"].(string)
			result.Service.ActiveState, _ = properties["ActiveState"].(string)
			result.Service.SubState, _ = properties["SubState"].(string)
			result.Service.UnitFile, _ = properties["UnitFileState"].(string)
			result.Service.Restarts = parseUint(properties["NRestarts"])
		}
	}
	if data, readErr := os.ReadFile("/proc/net/ip_mr_cache"); readErr == nil {
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(lines) > 1 {
			result.Multicast.Routes = len(lines) - 1
			for _, line := range lines[1:] {
				fields := strings.Fields(line)
				if len(fields) > 3 {
					packets, _ := strconv.ParseUint(fields[3], 0, 64)
					result.Multicast.Packets += packets
				}
			}
		}
	}
	if table, tableErr := iptables.NewWithProtocol(iptables.ProtocolIPv4); tableErr == nil {
		if rules, ruleErr := table.ListWithCounters("nat", "POSTROUTING"); ruleErr == nil {
			for _, rule := range rules {
				if strings.Contains(rule, "udm-iptv") {
					result.NAT = append(result.NAT, sanitize(rule))
				}
			}
		}
	}
	return result, nil
}

func renderSnapshot(value snapshot) string {
	return fmt.Sprintf(`udm-iptv %s
Profile: %s
WAN: %s, VLAN %d (%s), DHCP: %t
Custom VLAN MAC: %t, static address: %t, DHCP options: %t
NAT destinations: %s
Active NAT rules: %d
Proxy sources: %s
LAN interfaces: %s
Service: %s/%s (%s, restarts: %d)
Proxy: %s (PID %d)
IPTV interface: %s (%s, %d IPv4 addresses)
Routes: %s
IPTV default route: %t
Multicast routes: %d (%d packets)
`, value.Version, value.Config.Profile, value.Config.WANInterface, value.Config.VLAN, value.Config.IPTVInterface, value.Config.DHCP,
		value.Config.CustomMAC, value.Config.StaticAddress, value.Config.DHCPOptions, strings.Join(value.Config.NATDestinations, ", "),
		len(value.NAT), strings.Join(value.Config.ProxySourceRanges, ", "), strings.Join(value.Config.LANInterfaces, ", "),
		fallbackText(value.Service.ActiveState), fallbackText(value.Service.SubState), fallbackText(value.Service.UnitFile), value.Service.Restarts,
		fallbackText(value.Service.Proxy), value.Service.ProxyPID, value.Network.Target, fallbackText(value.Network.LinkState), value.Network.AddressCount,
		strings.Join(value.Network.Routes, ", "), value.Network.DefaultRoute, value.Multicast.Routes, value.Multicast.Packets)
}

var (
	macPattern = regexp.MustCompile(`(?i)(?:\b[0-9a-f]{2}[:-]){5}[0-9a-f]{2}\b|\b[0-9a-f]{4}\.[0-9a-f]{4}\.[0-9a-f]{4}\b`)
	ipPattern  = regexp.MustCompile(`\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b|\b[0-9a-fA-F:]{2,}%?[0-9A-Za-z_.-]*\b`)
)

func sanitize(text string) string {
	return sanitizeWithAddresses(text, assignedAddresses())
}

func sanitizeWithAddresses(text string, addresses []string) string {
	text = macPattern.ReplaceAllString(text, "<mac>")
	if hostname, err := os.Hostname(); err == nil && hostname != "" {
		pattern := regexp.MustCompile(`(?i)(^|[^0-9A-Za-z_-])(` + regexp.QuoteMeta(hostname) + `)([^0-9A-Za-z_-]|$)`)
		text = pattern.ReplaceAllString(text, "${1}<router-hostname>${3}")
	}
	for _, address := range addresses {
		pattern := regexp.MustCompile(`(^|[^0-9A-Fa-f:.])(` + regexp.QuoteMeta(address) + `)([^0-9A-Fa-f:.]|$)`)
		text = pattern.ReplaceAllString(text, "${1}<device-address>${3}")
	}
	return ipPattern.ReplaceAllStringFunc(text, func(candidate string) string {
		address := strings.Trim(candidate, "[](),")
		if zone := strings.LastIndexByte(address, '%'); zone >= 0 {
			address = address[:zone]
		}
		parsed, err := netip.ParseAddr(address)
		if err != nil {
			return candidate
		}
		if parsed.IsPrivate() || parsed.IsLoopback() || parsed.IsLinkLocalUnicast() || parsed.IsLinkLocalMulticast() || parsed.IsMulticast() || parsed.IsUnspecified() || parsed.Is6() || sharedAddress(parsed) {
			return "<redacted-address>"
		}
		return candidate
	})
}

type diagnosticSanitizer struct {
	configPath string
	addresses  map[string]bool
	mutex      sync.RWMutex
}

func newDiagnosticSanitizer(configPath string) *diagnosticSanitizer {
	value := &diagnosticSanitizer{configPath: configPath, addresses: make(map[string]bool)}
	value.refresh()
	return value
}

func (value *diagnosticSanitizer) refresh() {
	value.observe(assignedAddresses())
	configured, err := config.Load(value.configPath)
	if err != nil || configured.WAN.StaticAddress == "" {
		return
	}
	if prefix, parseErr := netip.ParsePrefix(configured.WAN.StaticAddress); parseErr == nil {
		value.observe([]string{prefix.Addr().String()})
	}
}

func (value *diagnosticSanitizer) observe(addresses []string) {
	value.mutex.Lock()
	defer value.mutex.Unlock()
	for _, address := range addresses {
		if net.ParseIP(address) != nil {
			value.addresses[address] = true
		}
	}
}

func (value *diagnosticSanitizer) sanitize(text string) string {
	value.mutex.RLock()
	defer value.mutex.RUnlock()
	addresses := make([]string, 0, len(value.addresses))
	for address := range value.addresses {
		addresses = append(addresses, address)
	}
	return sanitizeWithAddresses(text, addresses)
}

func (value *diagnosticSanitizer) watch(ctx context.Context) (<-chan error, error) {
	failures := make(chan error, 1)
	updates := make(chan netlink.AddrUpdate, 16)
	options := netlink.AddrSubscribeOptions{
		ListExisting: true,
		ErrorCallback: func(err error) {
			select {
			case failures <- err:
			default:
			}
		},
	}
	if err := netlink.AddrSubscribeWithOptions(updates, ctx.Done(), options); err != nil {
		return nil, err
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case update, ok := <-updates:
				if !ok {
					if ctx.Err() == nil {
						select {
						case failures <- errors.New("address observation stopped"):
						default:
						}
					}
					return
				}
				if update.LinkAddress.IP != nil {
					value.observe([]string{update.LinkAddress.IP.String()})
				}
			}
		}
	}()
	return failures, nil
}

func assignedAddresses() []string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var result []string
	for _, iface := range interfaces {
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			raw, _, found := strings.Cut(address.String(), "/")
			if found {
				result = append(result, raw)
			}
		}
	}
	return result
}

func sharedAddress(address netip.Addr) bool {
	prefix := netip.MustParsePrefix("100.64.0.0/10")
	return address.Is4() && prefix.Contains(address)
}

func sanitizePrefixes(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		prefix, err := netip.ParsePrefix(value)
		if err == nil && (prefix.Addr().IsPrivate() || prefix.Addr().IsLoopback() || prefix.Addr().IsLinkLocalUnicast() || prefix.Addr().IsMulticast() || prefix.Addr().IsUnspecified() || sharedAddress(prefix.Addr())) {
			result = append(result, "<redacted-prefix>")
		} else {
			result = append(result, value)
		}
	}
	return result
}

func fallbackText(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

func (application *Application) reportHealthFailure(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	_ = writeString(application.Err, "\n=== udm-iptv failure diagnostics ===\n")
	sanitizer := newDiagnosticSanitizer(application.ConfigPath)
	if value, err := application.snapshot(ctx); err == nil {
		_ = writeString(application.Err, renderSnapshot(value))
	} else {
		_ = writef(application.Err, "Snapshot unavailable: %s\n", sanitizer.sanitize(err.Error()))
	}
	command := exec.CommandContext(ctx, "journalctl", "-n", "100", "--no-pager", "-o", "cat", "-u", "udm-iptv.service")
	if output, err := command.Output(); err == nil {
		_ = writeString(application.Err, "--- recent service logs ---\n")
		_ = writef(application.Err, "%s\n", sanitizer.sanitize(strings.TrimSpace(string(output))))
	}
}
