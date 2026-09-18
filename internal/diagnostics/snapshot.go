package diagnostics

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-iptables/iptables"
	systemd "github.com/coreos/go-systemd/v22/dbus"
	"github.com/vishvananda/netlink"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/device"
	"github.com/kjanat/udm-iptv/internal/mroute"
	"github.com/kjanat/udm-iptv/internal/network"
	"github.com/kjanat/udm-iptv/internal/service"
)

// Collector reads router state for snapshots and bounded captures.
type Collector struct{ ConfigPath, Version string }

// Snapshot represents a point-in-time summary of the router's
// configuration and status.
type Snapshot struct {
	Timestamp   time.Time           `json:"timestamp"`
	Version     string              `json:"version"`
	Config      configSummary       `json:"config"`
	Service     serviceStatus       `json:"service"`
	Network     networkStatus       `json:"network"`
	Multicast   *MulticastInfo      `json:"multicast"`
	Memberships *[]Membership       `json:"memberships"`
	Lease       *service.LeaseState `json:"lease"`
	NAT         *[]string           `json:"natRules"`
	Downstream  []downstreamStatus  `json:"downstream"`
	Switches    string              `json:"switches"`
	NativeProxy string              `json:"nativeProxy"`
	Playback    string              `json:"playback"`
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
	Addresses    []string `json:"addresses"`
	Routes       []string `json:"routes"`
	DefaultRoute bool     `json:"defaultRoute"`
}

// MulticastInfo is the kernel forwarding cache: Routes counts the entries
// that forward, Unresolved the ones the kernel is still deciding on.
type MulticastInfo struct {
	Routes     int            `json:"routes"`
	Unresolved int            `json:"unresolved"`
	Packets    uint64         `json:"packets"`
	Bytes      uint64         `json:"bytes"`
	Entries    []mroute.Route `json:"entries"`
}

// Membership is one group a bridge port has joined, from the bridge MDB.
type Membership struct {
	Bridge string `json:"bridge"`
	Port   string `json:"port"`
	Group  string `json:"group"`
}

// mdbTimeout bounds the bridge MDB query.
const mdbTimeout = 5 * time.Second

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
	if memberships, err := bridgeMemberships(ctx); err == nil {
		result.Memberships = &memberships
	}
	if lease, err := service.ReadLeaseState(); err == nil {
		result.Lease = &lease
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

func inspectReceivers(usage *MulticastInfo, lan []string) string {
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

func multicastSummary(usage *MulticastInfo) string {
	if usage == nil {
		return counterUnavailable
	}

	return fmt.Sprintf("%d forwarding (%d packets, %s), %d unresolved", usage.Routes, usage.Packets, formatBytes(usage.Bytes), usage.Unresolved)
}

func formatBytes(value uint64) string {
	const unit = 1000
	switch {
	case value >= unit*unit*unit:
		return fmt.Sprintf("%.1f GB", float64(value)/(unit*unit*unit))
	case value >= unit*unit:
		return fmt.Sprintf("%.1f MB", float64(value)/(unit*unit))
	case value >= unit:
		return fmt.Sprintf("%.1f kB", float64(value)/unit)
	}

	return fmt.Sprintf("%d B", value)
}

func renderMulticast(usage *MulticastInfo) string {
	if usage == nil {
		return ""
	}
	var output strings.Builder
	for _, route := range usage.Entries {
		fmt.Fprintf(&output, "  %s from %s: %s -> %s, %d packets, %s", route.Group, route.Source, route.Input, strings.Join(route.Outputs, ","), route.Packets, formatBytes(route.Bytes))
		if route.Wrong > 0 {
			fmt.Fprintf(&output, ", %d on the wrong interface", route.Wrong)
		}
		output.WriteByte('\n')
	}

	return output.String()
}

func renderMemberships(memberships *[]Membership) string {
	if memberships == nil {
		return "Bridge memberships: unavailable\n"
	}
	var output strings.Builder
	fmt.Fprintf(&output, "Bridge memberships: %d\n", len(*memberships))
	for _, entry := range *memberships {
		fmt.Fprintf(&output, "  %s %s %s\n", entry.Bridge, entry.Port, entry.Group)
	}

	return output.String()
}

func renderLease(lease *service.LeaseState) string {
	if lease == nil {
		return "DHCP lease: none recorded\n"
	}
	var output strings.Builder
	fmt.Fprintf(&output, "DHCP lease: %s at %s, address %s/%s, routers %s, static routes %s\n",
		lease.Lease.Action, lease.Received.Format(time.RFC3339), lease.Lease.Address, lease.Lease.Mask,
		strings.Join(lease.Lease.Routers, " "), strings.Join(lease.Lease.StaticRoutes, " "))
	keys := make([]string, 0, len(lease.Lease.Options))
	for key := range lease.Lease.Options {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(&output, "  %s=%s\n", key, lease.Lease.Options[key])
	}

	return output.String()
}

func multicastRouteCount(usage *MulticastInfo) string {
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
		DHCPRoutes: string(value.WAN.DHCPRoutes), NATDestinations: value.WAN.NATDestinations,
		LANInterfaces: value.LAN.Interfaces, Proxy: value.Proxy.Program, IGMPVersion: value.Proxy.IGMPVersion,
		QuickLeave: value.Proxy.QuickLeave, Debug: value.Proxy.Debug, ProxySourceRanges: value.Proxy.SourceRanges,
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

func multicastUsage() (MulticastInfo, error) {
	table, err := mroute.Read()
	if err != nil {
		return MulticastInfo{}, fmt.Errorf("read multicast routes: %w", err)
	}

	return MulticastInfo{Routes: len(table.Routes), Unresolved: table.Unresolved, Packets: table.Packets(), Bytes: table.Bytes(), Entries: table.Routes}, nil
}

// bridgeMemberships asks the bridge MDB which groups each port joined.
func bridgeMemberships(ctx context.Context) ([]Membership, error) {
	ctx, cancel := context.WithTimeout(ctx, mdbTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, "bridge", "-j", "mdb", "show").Output()
	if err != nil {
		return nil, fmt.Errorf("read bridge mdb: %w", err)
	}

	return parseMemberships(output)
}

func parseMemberships(data []byte) ([]Membership, error) {
	var bridges []struct {
		MDB []struct {
			Dev   string `json:"dev"`
			Port  string `json:"port"`
			Group string `json:"grp"`
		} `json:"mdb"`
	}
	if err := json.Unmarshal(data, &bridges); err != nil {
		return nil, fmt.Errorf("parse bridge mdb: %w", err)
	}
	memberships := []Membership{}
	for _, bridge := range bridges {
		for _, entry := range bridge.MDB {
			memberships = append(memberships, Membership{Bridge: entry.Dev, Port: entry.Port, Group: entry.Group})
		}
	}

	return memberships, nil
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
			managed = append(managed, rule)
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
		for _, address := range addresses {
			result.Addresses = append(result.Addresses, address.IPNet.String())
		}
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
		if route.Gw != nil {
			destination += " via " + route.Gw.String()
		}
		result.Routes = append(result.Routes, destination)
	}
	sort.Strings(result.Routes)

	return result
}

// RenderSnapshot returns a human-readable string representation of a snapshot.
// Configured settings and observed state are labelled apart.
func RenderSnapshot(value Snapshot) string {
	return fmt.Sprintf(`udm-iptv %s
Profile: %s
WAN: %s, VLAN %d (%s), DHCP: %t
Custom VLAN MAC: %t, static address: %t, DHCP options: %t
DHCP route policy: %s
NAT destinations: %s
Active NAT rules: %s
Proxy source ranges: %s
LAN interfaces: %s
Service: %s/%s (%s, restarts: %d)
Proxy: %s (PID %d)
IGMP version: %d, quickleave enabled: %t, proxy debug logging: %t
IPTV interface: %s (%s, %d IPv4 addresses)
Addresses: %s
Routes: %s
Default route observed on %s: %s
Multicast routes: %s
`, value.Version, value.Config.Profile, value.Config.WANInterface, value.Config.VLAN, value.Config.IPTVInterface, value.Config.DHCP,
		value.Config.CustomMAC, value.Config.StaticAddress, value.Config.DHCPOptions, fallbackText(value.Config.DHCPRoutes),
		strings.Join(value.Config.NATDestinations, ", "), natRuleCount(value.NAT), renderSourceRanges(value.Config), strings.Join(value.Config.LANInterfaces, ", "),
		fallbackText(value.Service.ActiveState), fallbackText(value.Service.SubState), fallbackText(value.Service.UnitFile), value.Service.Restarts,
		fallbackText(value.Service.Proxy), value.Service.ProxyPID, value.Config.IGMPVersion, value.Config.QuickLeave, value.Config.Debug,
		value.Network.Target, fallbackText(value.Network.LinkState), value.Network.AddressCount, strings.Join(value.Network.Addresses, ", "),
		strings.Join(value.Network.Routes, ", "), value.Network.Target, presence(value.Network.DefaultRoute), multicastSummary(value.Multicast)) +
		renderMulticast(value.Multicast) + renderNAT(value.NAT) + renderMemberships(value.Memberships) + renderLease(value.Lease) + renderDownstream(value)
}

func presence(observed bool) string {
	if observed {
		return "yes"
	}

	return "none"
}

// renderSourceRanges says whether the configured ranges reach the proxy:
// igmpproxy takes them as altnet entries, improxy has no source filter.
func renderSourceRanges(summary configSummary) string {
	if len(summary.ProxySourceRanges) == 0 {
		return "none configured"
	}
	ranges := strings.Join(summary.ProxySourceRanges, ", ")
	if summary.Proxy == config.ProxyImproxy {
		return ranges + " (configured; improxy has no source filter, so nothing is applied)"
	}

	return ranges + " (applied as igmpproxy altnet)"
}

func renderNAT(rules *[]string) string {
	if rules == nil {
		return ""
	}
	var output strings.Builder
	for _, rule := range *rules {
		fmt.Fprintf(&output, "  %s\n", rule)
	}

	return output.String()
}
