package diagnostics

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-iptables/iptables"
	systemd "github.com/coreos/go-systemd/v22/dbus"
	"github.com/vishvananda/netlink"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/device"
	"github.com/kjanat/udm-iptv/internal/network"
	"github.com/kjanat/udm-iptv/internal/service"
)

// Collector reads router state for snapshots and bounded captures.
type Collector struct{ ConfigPath, Version string }

// Snapshot represents a point-in-time summary of the router's
// configuration and status.
type Snapshot struct {
	Timestamp   time.Time          `json:"timestamp"`
	Version     string             `json:"version"`
	Config      configSummary      `json:"config"`
	Service     serviceStatus      `json:"service"`
	Network     networkStatus      `json:"network"`
	Multicast   *multicastInfo     `json:"multicast"`
	NAT         *[]string          `json:"natRules"`
	Downstream  []downstreamStatus `json:"downstream"`
	Switches    string             `json:"switches"`
	NativeProxy string             `json:"nativeProxy"`
	Playback    string             `json:"playback"`
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
	DHCPRoutes        string   `json:"dhcpRoutes"`
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

// ipMRCachePacketsColumn is the packet-count field in /proc/net/ip_mr_cache.
const ipMRCachePacketsColumn = 3

// Snapshot returns a snapshot of the current state of the system.
func (application *Collector) Snapshot(ctx context.Context) (Snapshot, error) {
	value, err := config.Load(application.ConfigPath)
	if err != nil {
		return Snapshot{}, fmt.Errorf("load configuration for snapshot: %w", err)
	}
	result := Snapshot{
		Timestamp:  time.Now().UTC(),
		Version:    application.Version,
		Config:     summarizeConfig(value),
		Service:    inspectService(ctx),
		Network:    inspectLink(network.Target(value)),
		Downstream: inspectDownstream(os.DirFS("/sys"), value.LAN.Interfaces),
	}
	if usage, err := multicastUsage(); err == nil {
		result.Multicast = &usage
	}
	if rules, err := managedNATRules(); err == nil {
		result.NAT = &rules
	}
	result.Switches = inspectSwitch(os.DirFS("/sys"), device.Inspect(ctx).Firmware)
	result.NativeProxy = inspectNativeProxy(ctx, result.Service.ProxyPID)
	result.Playback = inspectReceivers(result.Multicast, value.LAN.Interfaces)

	return result, nil
}

func inspectNativeProxy(ctx context.Context, ourPID int) string {
	loaded, active := false, ""
	connection, err := systemd.NewSystemConnectionContext(ctx)
	if err == nil {
		defer connection.Close()
		properties, err := connection.GetAllPropertiesContext(ctx, "igmpproxy.service")
		if err == nil {
			loaded = true
			active, _ = properties["ActiveState"].(string)
		}
	}

	extra, scanned := extraProxyPIDs(ourPID)

	return formatNativeProxy(loaded, active, extra, scanned)
}

func inspectReceivers(usage *multicastInfo, lan []string) string {
	data, err := os.ReadFile("/proc/net/igmp")
	if err != nil {
		return formatReceivers(usage, nil)
	}
	groups := countLANIGMPGroups(string(data), lan)

	return formatReceivers(usage, &groups)
}

// counterUnavailable keeps a failed read out of the counts, so an unreadable
// kernel table never renders as an idle router.
const counterUnavailable = "unavailable"

func multicastSummary(usage *multicastInfo) string {
	if usage == nil {
		return counterUnavailable
	}

	return fmt.Sprintf("%d (%d packets)", usage.Routes, usage.Packets)
}

func multicastRouteCount(usage *multicastInfo) string {
	if usage == nil {
		return counterUnavailable
	}

	return strconv.Itoa(usage.Routes)
}

func natRuleCount(rules *[]string) string {
	if rules == nil {
		return counterUnavailable
	}

	return strconv.Itoa(len(*rules))
}

func summarizeConfig(value config.Config) configSummary {
	return configSummary{
		Profile: value.Profile, WANInterface: value.WAN.Interface, VLAN: value.WAN.VLAN, IPTVInterface: value.WAN.VLANInterface,
		CustomMAC: value.WAN.VLANMAC != "", DHCP: value.WAN.DHCP, DHCPOptions: len(value.WAN.DHCPOptions) > 0, StaticAddress: value.WAN.StaticAddress != "",
		DHCPRoutes: string(value.WAN.DHCPRoutes), NATDestinations: sanitizePrefixes(value.WAN.NATDestinations),
		LANInterfaces: value.LAN.Interfaces, Proxy: value.Proxy.Program, IGMPVersion: value.Proxy.IGMPVersion,
		QuickLeave: value.Proxy.QuickLeave, Debug: value.Proxy.Debug, ProxySourceRanges: sanitizePrefixes(value.Proxy.SourceRanges),
	}
}

func inspectService(ctx context.Context) serviceStatus {
	var status serviceStatus
	if state, err := service.ReadRuntimeState(); err == nil {
		status.Proxy = state.Proxy
		status.ProxyPID = state.ProxyPID
	}
	connection, err := systemd.NewSystemConnectionContext(ctx)
	if err != nil {
		return status
	}
	defer connection.Close()
	properties, err := connection.GetAllPropertiesContext(ctx, "udm-iptv.service")
	if err != nil {
		return status
	}
	status.LoadState, _ = properties["LoadState"].(string)
	status.ActiveState, _ = properties["ActiveState"].(string)
	status.SubState, _ = properties["SubState"].(string)
	status.UnitFile, _ = properties["UnitFileState"].(string)
	status.Restarts = service.ParseCounter(properties["NRestarts"])

	return status
}

func multicastUsage() (multicastInfo, error) {
	data, err := os.ReadFile("/proc/net/ip_mr_cache")
	if err != nil {
		return multicastInfo{}, fmt.Errorf("read ip_mr_cache: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) <= 1 {
		return multicastInfo{}, nil
	}
	usage := multicastInfo{Routes: len(lines) - 1}
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) > ipMRCachePacketsColumn {
			packets, _ := strconv.ParseUint(fields[ipMRCachePacketsColumn], 0, 64)
			usage.Packets += packets
		}
	}

	return usage, nil
}

func managedNATRules() ([]string, error) {
	table, err := iptables.NewWithProtocol(iptables.ProtocolIPv4)
	if err != nil {
		return nil, fmt.Errorf("open iptables: %w", err)
	}
	rules, err := table.ListWithCounters("nat", "POSTROUTING")
	if err != nil {
		return nil, fmt.Errorf("list nat POSTROUTING: %w", err)
	}
	managed := []string{}
	for _, rule := range rules {
		if strings.Contains(rule, "udm-iptv") {
			managed = append(managed, sanitize(rule))
		}
	}

	return managed, nil
}

func inspectLink(target string) networkStatus {
	result := networkStatus{Target: target}
	link, err := netlink.LinkByName(target)
	if err != nil {
		return result
	}
	result.LinkState = link.Attrs().OperState.String()
	if addresses, err := netlink.AddrList(link, netlink.FAMILY_V4); err == nil {
		result.AddressCount = len(addresses)
	}
	routes, err := netlink.RouteList(link, netlink.FAMILY_V4)
	if err != nil {
		return result
	}
	for _, route := range routes {
		destination := "default"
		if route.Dst != nil {
			destination = route.Dst.String()
		}
		if destination == "default" || destination == "0.0.0.0/0" {
			result.DefaultRoute = true
		}
		result.Routes = append(result.Routes, sanitize(destination))
	}
	sort.Strings(result.Routes)

	return result
}

// RenderSnapshot returns a human-readable string representation of a snapshot.
func RenderSnapshot(value Snapshot) string {
	return fmt.Sprintf(`udm-iptv %s
Profile: %s
WAN: %s, VLAN %d (%s), DHCP: %t
Custom VLAN MAC: %t, static address: %t, DHCP options: %t
NAT destinations: %s
Active NAT rules: %s
Proxy sources: %s
LAN interfaces: %s
Service: %s/%s (%s, restarts: %d)
Proxy: %s (PID %d)
IGMP version: %d, quickleave enabled: %t, proxy debug logging: %t
IPTV interface: %s (%s, %d IPv4 addresses)
Routes: %s
IPTV default route: %t
Multicast routes: %s
`, value.Version, value.Config.Profile, value.Config.WANInterface, value.Config.VLAN, value.Config.IPTVInterface, value.Config.DHCP,
		value.Config.CustomMAC, value.Config.StaticAddress, value.Config.DHCPOptions, strings.Join(value.Config.NATDestinations, ", "),
		natRuleCount(value.NAT), strings.Join(value.Config.ProxySourceRanges, ", "), strings.Join(value.Config.LANInterfaces, ", "),
		fallbackText(value.Service.ActiveState), fallbackText(value.Service.SubState), fallbackText(value.Service.UnitFile), value.Service.Restarts,
		fallbackText(value.Service.Proxy), value.Service.ProxyPID, value.Config.IGMPVersion, value.Config.QuickLeave, value.Config.Debug,
		value.Network.Target, fallbackText(value.Network.LinkState), value.Network.AddressCount,
		strings.Join(value.Network.Routes, ", "), value.Network.DefaultRoute, multicastSummary(value.Multicast)) + renderDownstream(value)
}
