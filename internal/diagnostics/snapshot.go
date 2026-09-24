package diagnostics

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	systemd "github.com/coreos/go-systemd/v22/dbus"
	"github.com/vishvananda/netlink"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/device"
	"github.com/kjanat/udm-iptv/internal/installer"
	"github.com/kjanat/udm-iptv/internal/mroute"
	"github.com/kjanat/udm-iptv/internal/network"
	"github.com/kjanat/udm-iptv/internal/proxyinventory"
	"github.com/kjanat/udm-iptv/internal/service"
)

// Collector reads router state for snapshots and bounded captures.
type Collector struct{ ConfigPath, StateDir, Version string }

// Snapshot represents a point-in-time summary of the router's
// configuration and status.
type Snapshot struct {
	Proxies     proxyinventory.Inventory `json:"proxies,omitempty"`
	Errors      map[string]string        `json:"errors,omitempty"`
	Timestamp   time.Time                `json:"timestamp"`
	Version     string                   `json:"version"`
	System      systemInfo               `json:"system"`
	ProxyConfig *string                  `json:"proxyConfig"`
	RecentLogs  *recentJournal           `json:"recentLogs,omitempty"`
	Config      configSummary            `json:"config"`
	Service     serviceStatus            `json:"service"`
	Network     networkStatus            `json:"network"`
	Multicast   *MulticastInfo           `json:"multicast"`
	Memberships *[]Membership            `json:"memberships"`
	Lease       *service.LeaseState      `json:"lease"`
	NAT         *[]network.NATRule       `json:"natRules"`
	NATEvidence *[]NATEvidence           `json:"natEvidence"`
	Downstream  []downstreamStatus       `json:"downstream"`
	Switches    string                   `json:"switches"`
	NativeProxy string                   `json:"nativeProxy"`
	Playback    string                   `json:"playback"`
}

type configSummary struct {
	MACAddress        string   `json:"vlanMAC,omitempty"`
	StaticCIDR        string   `json:"staticAddress,omitempty"`
	DHCPOptionValues  []string `json:"dhcpOptions,omitempty"`
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
	MLDVersion        int      `json:"mldVersion"`
	QuickLeave        bool     `json:"quickLeave"`
	Debug             bool     `json:"debug"`
	ProxySourceRanges []string `json:"proxySourceRanges"`
}

type serviceStatus struct {
	Errors      map[string]string `json:"errors,omitempty"`
	SystemState string            `json:"systemState"`
	Units       []unitEvidence    `json:"units,omitempty"`
	ResumeAt    *time.Time        `json:"resumeAt,omitempty"`
	ResumeError string            `json:"resumeError,omitempty"`
	Package     string            `json:"package,omitempty"`
	LoadState   string            `json:"loadState"`
	ActiveState string            `json:"activeState"`
	SubState    string            `json:"subState"`
	UnitFile    string            `json:"unitFileState"`
	Restarts    uint64            `json:"restarts"`
	Proxy       string            `json:"proxy,omitempty"`
	ProxyPID    int               `json:"proxyPID,omitempty"`
}

type networkStatus struct {
	Errors       map[string]string `json:"errors,omitempty"`
	VLAN         *vlanCheck        `json:"vlanCheck,omitempty"`
	Target       string            `json:"target"`
	LinkState    string            `json:"linkState"`
	AddressCount int               `json:"addressCount"`
	Addresses    []string          `json:"addresses"`
	AddressesV6  []string          `json:"addressesV6,omitempty"`
	IPv6Knobs    map[string]string `json:"ipv6Knobs,omitempty"`
	Routes       []string          `json:"routes"`
	DefaultRoute bool              `json:"defaultRoute"`
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

// NATEvidence is what the router shows for one configured NAT destination:
// the routes on the IPTV interface that can carry traffic to it, and the
// traffic the rules for it matched, managed and unmanaged apart.
type NATEvidence struct {
	Destination      string   `json:"destination"`
	Routes           []string `json:"routes"`
	Packets          uint64   `json:"packets"`
	Bytes            uint64   `json:"bytes"`
	UnmanagedRules   int      `json:"unmanagedRules"`
	UnmanagedPackets uint64   `json:"unmanagedPackets"`
	UnmanagedBytes   uint64   `json:"unmanagedBytes"`
}

// Membership is one group a bridge port has joined, from the bridge MDB.
type Membership struct {
	Bridge string `json:"bridge"`
	Port   string `json:"port"`
	Group  string `json:"group"`
}

// mdbTimeout bounds the bridge MDB query.
const mdbTimeout = 5 * time.Second

// defaultRouteText is how a route without a destination is listed.
const defaultRouteText = "default"

// Snapshot returns a snapshot of the current state of the system.
func (application *Collector) Snapshot(ctx context.Context) (Snapshot, error) {
	value, err := config.Load(application.ConfigPath)
	if err != nil {
		return Snapshot{}, fmt.Errorf("load configuration for snapshot: %w", err)
	}
	stateDir := application.StateDir
	if stateDir == "" {
		stateDir = filepath.Dir(application.ConfigPath)
	}
	hardware := device.Inspect(ctx)
	result := Snapshot{
		Timestamp:  time.Now().UTC(),
		Proxies:    proxyinventory.Inspect(ctx, stateDir),
		Version:    application.Version,
		System:     inspectSystem(os.DirFS("/"), hardware),
		Config:     summarizeConfig(value),
		Service:    inspectService(ctx),
		Network:    inspectLink(network.Target(value)),
		Downstream: inspectDownstream(os.DirFS("/sys"), value.LAN.Interfaces),
	}
	result.Network.VLAN = inspectVLAN(value, netlink.LinkByName, netlink.LinkByIndex)
	if value.Proxy.MLDVersion != 0 {
		result.Network.IPv6Knobs = network.IPv6MulticastState(value)
	}
	collectSnapshotTables(ctx, value, &result)
	if generated, err := service.ReadProxyConfig(); err == nil {
		result.ProxyConfig = &generated
	} else {
		recordCollectionError(&result.Errors, "proxyConfig", err)
	}
	result.Switches = inspectSwitch(os.DirFS("/sys"), hardware.Firmware)
	result.NativeProxy = inspectNativeProxy(ctx, result.Service.ProxyPID)
	result.Playback = inspectReceivers(result.Multicast, value.LAN.Interfaces)

	return result, nil
}

func collectSnapshotTables(ctx context.Context, value config.Config, result *Snapshot) {
	if usage, err := multicastUsage(); err == nil {
		result.Multicast = &usage
	} else {
		recordCollectionError(&result.Errors, "multicast", err)
	}
	if rules, err := network.ListNAT(value); err == nil {
		result.NAT = &rules
		result.NATEvidence = observedNATEvidence(value.WAN.NATDestinations, result.Network, rules)
	} else {
		recordCollectionError(&result.Errors, "nat", err)
	}
	if memberships, err := bridgeMemberships(ctx); err == nil {
		result.Memberships = &memberships
	} else {
		recordCollectionError(&result.Errors, "memberships", err)
	}
	if lease, err := service.ReadLeaseState(); err == nil {
		result.Lease = &lease
	} else {
		recordCollectionError(&result.Errors, "lease", err)
	}
}

func observedNATEvidence(destinations []string, observed networkStatus, rules []network.NATRule) *[]NATEvidence {
	if observed.Errors["link"] != "" || observed.Errors["routes"] != "" {
		return nil
	}
	evidence := natEvidence(destinations, observed.Routes, rules)
	return &evidence
}

func recordCollectionError(target *map[string]string, collector string, err error) {
	if err == nil {
		return
	}
	if *target == nil {
		*target = make(map[string]string)
	}
	(*target)[collector] = err.Error()
}

func inspectNativeProxy(ctx context.Context, ourPID int) string {
	loaded, active := false, ""
	connection, err := systemd.NewSystemConnectionContext(ctx)
	unitErr := err
	if err == nil {
		defer connection.Close()
		properties, err := connection.GetAllPropertiesContext(ctx, "igmpproxy.service")
		unitErr = err
		if err == nil {
			loaded = true
			active, _ = properties["ActiveState"].(string)
		}
	}

	extra, scanned := extraProxyPIDs(ourPID)

	text := formatNativeProxy(loaded, active, extra, scanned)
	if unitErr != nil {
		text = strings.Replace(text, "igmpproxy.service not loaded", "igmpproxy.service unavailable: "+unitErr.Error(), 1)
	}
	return text
}

func inspectReceivers(usage *MulticastInfo, lan []string) string {
	data, err := os.ReadFile("/proc/net/igmp")
	if err != nil {
		return formatReceivers(usage, nil) + ": " + err.Error()
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
		return fmt.Sprintf("%.1f GB (%d B)", float64(value)/(unit*unit*unit), value)
	case value >= unit*unit:
		return fmt.Sprintf("%.1f MB (%d B)", float64(value)/(unit*unit), value)
	case value >= unit:
		return fmt.Sprintf("%.1f kB (%d B)", float64(value)/unit, value)
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

// renderIPv6 reports the IPv6 addresses the IPTV interface carries and the
// forwarding knobs MLD needs. A forwarding interface with accept_ra below 2
// never takes the provider's Router Advertisement, so it carries no global
// address and the proxy has no upstream.
func renderIPv6(network networkStatus) string {
	var output strings.Builder
	if len(network.AddressesV6) > 0 {
		fmt.Fprintf(&output, "IPv6 addresses on %s: %s\n", network.Target, strings.Join(network.AddressesV6, ", "))
	}
	if len(network.IPv6Knobs) > 0 {
		names := slices.Sorted(maps.Keys(network.IPv6Knobs))
		settings := make([]string, 0, len(names))
		for _, name := range names {
			settings = append(settings, name+"="+network.IPv6Knobs[name])
		}
		fmt.Fprintf(&output, "IPv6 multicast knobs: %s\n", strings.Join(settings, ", "))
	}

	return output.String()
}

func (r reportRenderer) lease(lease *service.LeaseState) string {
	if lease == nil {
		return "DHCP lease: none recorded\n"
	}
	var output strings.Builder
	var outcome string
	if lease.Applied {
		outcome = r.text(ReportGood, "applied")
	} else {
		outcome = r.text(ReportBad, "not applied") + ": " + lease.Failure
	}
	fmt.Fprintf(&output, "DHCP lease: %s at %s, address %s/%s, routers %s, static routes %s, %s\n",
		lease.Lease.Action, lease.Received.Format(time.RFC3339Nano), lease.Lease.Address, lease.Lease.Mask,
		strings.Join(lease.Lease.Routers, " "), strings.Join(lease.Lease.StaticRoutes, " "), outcome)
	fmt.Fprintf(&output, "Lease interface: %s, broadcast: %s, route metric: %d\n", lease.Lease.Interface, lease.Lease.Broadcast, lease.Lease.Metric)
	if lease.Failure != "" {
		fmt.Fprintf(&output, "Recorded lease failure: %s\n", lease.Failure)
	}
	managed, err := json.Marshal(struct {
		Routes    []netlink.Route `json:"routes"`
		Addresses []netlink.Addr  `json:"addresses"`
	}{lease.Lease.ManagedRoutes, lease.Lease.ManagedAddresses})
	if err != nil {
		fmt.Fprintf(&output, "Lease managed state unavailable: %s\n", err)
	} else {
		fmt.Fprintf(&output, "Lease managed state: %s\n", managed)
	}
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

func natRuleCount(rules *[]network.NATRule) string {
	if rules == nil {
		return counterUnavailable
	}
	managed, unmanaged := 0, 0
	for _, rule := range *rules {
		if rule.Managed {
			managed++
		} else {
			unmanaged++
		}
	}
	if unmanaged == 0 {
		return strconv.Itoa(managed)
	}

	return fmt.Sprintf("%d managed, %d unmanaged", managed, unmanaged)
}

func natEvidence(destinations, routes []string, rules []network.NATRule) []NATEvidence {
	evidence := make([]NATEvidence, 0, len(destinations))
	for _, destination := range destinations {
		entry := NATEvidence{Destination: destination, Routes: []string{}}
		if prefix, err := netip.ParsePrefix(destination); err == nil {
			entry.Routes = overlappingRoutes(prefix, routes)
		}
		for _, rule := range rules {
			if !samePrefix(rule.Destination, destination) {
				continue
			}
			if rule.Managed {
				entry.Packets += rule.Packets
				entry.Bytes += rule.Bytes

				continue
			}
			entry.UnmanagedRules++
			entry.UnmanagedPackets += rule.Packets
			entry.UnmanagedBytes += rule.Bytes
		}
		evidence = append(evidence, entry)
	}

	return evidence
}

// overlappingRoutes keeps the routes that can carry traffic to prefix, in
// the form inspectLink lists them: the destination first, "default" for a
// default route.
func overlappingRoutes(prefix netip.Prefix, routes []string) []string {
	matches := []string{}
	for _, route := range routes {
		destination, _, _ := strings.Cut(route, " ")
		if destination == defaultRouteText {
			destination = "0.0.0.0/0"
		}
		parsed, err := netip.ParsePrefix(destination)
		if err == nil && parsed.Overlaps(prefix) {
			matches = append(matches, route)
		}
	}

	return matches
}

func samePrefix(left, right string) bool {
	first, err := netip.ParsePrefix(left)
	if err != nil {
		return left == right
	}
	second, err := netip.ParsePrefix(right)
	if err != nil {
		return false
	}

	return first.Masked() == second.Masked()
}

func summarizeConfig(value config.Config) configSummary {
	return configSummary{
		MACAddress: value.WAN.VLANMAC, StaticCIDR: value.WAN.StaticAddress, DHCPOptionValues: slices.Clone(value.WAN.DHCPOptions),
		Profile: value.Profile, WANInterface: value.WAN.Interface, VLAN: value.WAN.VLAN, IPTVInterface: value.WAN.VLANInterface,
		CustomMAC: value.WAN.VLANMAC != "", DHCP: value.WAN.DHCP, DHCPOptions: len(value.WAN.DHCPOptions) > 0, StaticAddress: value.WAN.StaticAddress != "",
		DHCPRoutes: string(value.WAN.DHCPRoutes), NATDestinations: value.WAN.NATDestinations,
		LANInterfaces: value.LAN.Interfaces, Proxy: value.Proxy.Program, IGMPVersion: value.Proxy.IGMPVersion, MLDVersion: value.Proxy.MLDVersion,
		QuickLeave: value.Proxy.QuickLeave, Debug: value.Proxy.Debug, ProxySourceRanges: value.Proxy.SourceRanges,
	}
}

func inspectService(ctx context.Context) serviceStatus {
	var status serviceStatus
	record, err := installer.QueryPackage(ctx)
	recordCollectionError(&status.Errors, "package", err)
	if err == nil && record.Owned() {
		status.Package = record.ReleaseVersion()
	}
	if state, err := service.ReadRuntimeState(); err == nil {
		status.Proxy = state.Proxy
		status.ProxyPID = state.ProxyPID
	} else {
		recordCollectionError(&status.Errors, "runtime", err)
	}
	connection, err := systemd.NewSystemConnectionContext(ctx)
	if err != nil {
		recordCollectionError(&status.Errors, "systemd", err)
		return status
	}
	defer connection.Close()
	lifecycle := service.Lifecycle{Connection: connection, Unit: service.Unit}
	collectSystemdEvidence(ctx, connection, &status)
	if resumeAt, err := lifecycle.ResumeAt(ctx); err != nil {
		status.ResumeError = err.Error()
	} else if !resumeAt.IsZero() {
		status.ResumeAt = &resumeAt
	}
	properties, err := connection.GetAllPropertiesContext(ctx, "udm-iptv.service")
	if err != nil {
		recordCollectionError(&status.Errors, "systemd", err)
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

func linkAddresses(link netlink.Link, family int) ([]string, error) {
	addresses, err := netlink.AddrList(link, family)
	if err != nil {
		return nil, fmt.Errorf("read interface addresses: %w", err)
	}
	result := make([]string, 0, len(addresses))
	for _, address := range addresses {
		result = append(result, address.IPNet.String())
	}

	return result, nil
}

func inspectLink(target string) networkStatus {
	result := networkStatus{Target: target}
	link, err := netlink.LinkByName(target)
	if err != nil {
		recordCollectionError(&result.Errors, "link", err)
		return result
	}
	result.LinkState = link.Attrs().OperState.String()
	result.Addresses, err = linkAddresses(link, netlink.FAMILY_V4)
	recordCollectionError(&result.Errors, "addresses4", err)
	result.AddressCount = len(result.Addresses)
	// A provider that carries IPTV over IPv6 shows up here first, so the
	// addresses are collected even though the lease path is IPv4 only.
	result.AddressesV6, err = linkAddresses(link, netlink.FAMILY_V6)
	recordCollectionError(&result.Errors, "addresses6", err)
	routes, err := netlink.RouteList(link, netlink.FAMILY_V4)
	if err != nil {
		recordCollectionError(&result.Errors, "routes", err)
		return result
	}
	for _, route := range routes {
		destination := defaultRouteText
		if route.Dst != nil {
			destination = route.Dst.String()
		}
		if destination == defaultRouteText || destination == "0.0.0.0/0" {
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
	return RenderSnapshotStyled(value, nil)
}

func (r reportRenderer) snapshotLease(value Snapshot) string {
	if value.Lease == nil {
		if value.Errors["lease"] != "" {
			return r.field("DHCP lease", r.text(ReportWarning, "unavailable"))
		}
		return r.field("DHCP lease", r.available(!value.Config.DHCP, "none recorded"))
	}
	return r.lease(value.Lease)
}

func observedText(failures map[string]string, value string, dependencies ...string) string {
	for _, name := range dependencies {
		if failures[name] != "" {
			return counterUnavailable
		}
	}
	return value
}

func (r reportRenderer) collectionErrors(section string, failures map[string]string) string {
	var output strings.Builder
	for _, name := range slices.Sorted(maps.Keys(failures)) {
		output.WriteString(r.text(ReportBad, "Collection error ("+section+"."+name+")") + ": " + failures[name] + "\n")
	}
	return output.String()
}

func renderResume(status serviceStatus) string {
	if status.ResumeError != "" {
		return "\nScheduled start: unavailable (" + status.ResumeError + ")"
	}
	if status.ResumeAt == nil {
		return ""
	}
	label := "Scheduled start: "
	if status.ActiveState == "inactive" {
		label = "Paused until: "
	}
	return "\n" + label + status.ResumeAt.UTC().Format(time.RFC3339)
}

// renderInstallation says who installed the executable and whether dpkg's
// record still matches it.
func renderInstallation(version, packageVersion string) string {
	switch packageVersion {
	case "":
		return "standalone"
	case version:
		return "package " + packageVersion
	default:
		return "package " + packageVersion + " recorded by dpkg while " + version + " runs; udm-iptv upgrade reinstalls the package"
	}
}

func presence(observed bool) string {
	if observed {
		return "yes"
	}

	return "none"
}

// sourceRanges says whether the configured ranges reach the proxy:
// igmpproxy takes them as altnet entries, improxy has no source filter.
func (r reportRenderer) sourceRanges(summary configSummary) string {
	if len(summary.ProxySourceRanges) == 0 {
		return "none configured"
	}
	ranges := strings.Join(summary.ProxySourceRanges, ", ")
	if summary.Proxy == config.ProxyImproxy {
		return ranges + " (" + r.text(ReportWarning, "configured; improxy has no source filter, so nothing is applied") + ")"
	}

	return ranges + " (applied as igmpproxy altnet)"
}

func renderNAT(rules *[]network.NATRule) string {
	if rules == nil || len(*rules) == 0 {
		return ""
	}
	var output strings.Builder
	output.WriteString("NAT rules on the IPTV interface:\n")
	for _, rule := range *rules {
		fmt.Fprintf(&output, "  %s %s: %d packets, %s\n", rule.Owner(), rule.Destination, rule.Packets, formatBytes(rule.Bytes))
	}

	return output.String()
}

func (r reportRenderer) natEvidence(evidence *[]NATEvidence, target string) string {
	if evidence == nil || len(*evidence) == 0 {
		return ""
	}
	var output strings.Builder
	output.WriteString(r.text(ReportHeading, "NAT evidence per destination:") + "\n")
	for _, entry := range *evidence {
		var reach string
		if len(entry.Routes) > 0 {
			reach = r.text(ReportGood, "routed") + " (" + strings.Join(entry.Routes, ", ") + ")"
		} else {
			reach = r.text(ReportWarning, "no route via "+target)
		}
		fmt.Fprintf(&output, "  %s: %s, %d packets, %s", entry.Destination, reach, entry.Packets, formatBytes(entry.Bytes))
		if entry.UnmanagedRules > 0 {
			fmt.Fprintf(&output, "; unmanaged rules for it: %d, %d packets, %s", entry.UnmanagedRules, entry.UnmanagedPackets, formatBytes(entry.UnmanagedBytes))
		}
		output.WriteByte('\n')
	}

	return output.String()
}
