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
	"github.com/kjanat/udm-iptv/internal/network"
	"github.com/kjanat/udm-iptv/internal/service"
)

// Collector reads router state for snapshots and bounded captures.
type Collector struct{ ConfigPath, Version string }

type Snapshot struct {
	Timestamp   time.Time          `json:"timestamp"`
	Version     string             `json:"version"`
	Config      configSummary      `json:"config"`
	Service     serviceStatus      `json:"service"`
	Network     networkStatus      `json:"network"`
	Multicast   multicastInfo      `json:"multicast"`
	NAT         []string           `json:"natRules"`
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

func (application *Collector) Snapshot(ctx context.Context) (Snapshot, error) {
	value, err := config.Load(application.ConfigPath)
	if err != nil {
		return Snapshot{}, err
	}
	result := Snapshot{Timestamp: time.Now().UTC(), Version: application.Version, Config: configSummary{
		Profile: value.Profile, WANInterface: value.WAN.Interface, VLAN: value.WAN.VLAN, IPTVInterface: value.WAN.VLANInterface,
		CustomMAC: value.WAN.VLANMAC != "", DHCP: value.WAN.DHCP, DHCPOptions: len(value.WAN.DHCPOptions) > 0, StaticAddress: value.WAN.StaticAddress != "",
		AllowDefaultRoute: value.WAN.AllowDefaultRoute, NATDestinations: sanitizePrefixes(value.WAN.NATDestinations),
		LANInterfaces: value.LAN.Interfaces, Proxy: value.Proxy.Program, IGMPVersion: value.Proxy.IGMPVersion,
		QuickLeave: value.Proxy.QuickLeave, Debug: value.Proxy.Debug, ProxySourceRanges: sanitizePrefixes(value.Proxy.SourceRanges),
	}}
	result.Network.Target = network.Target(value)
	result.Downstream = inspectDownstream(os.DirFS("/sys"), value.LAN.Interfaces)
	result.Switches, result.NativeProxy, result.Playback = "not checked", "not checked", "not checked"
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
	if state, stateErr := service.ReadRuntimeState(); stateErr == nil {
		result.Service.Proxy = state.Proxy
		result.Service.ProxyPID = state.ProxyPID
	}
	if connection, connectionErr := systemd.NewSystemConnectionContext(ctx); connectionErr == nil {
		defer connection.Close()
		if properties, propertyErr := connection.GetAllPropertiesContext(ctx, "udm-iptv.service"); propertyErr == nil {
			result.Service.LoadState, _ = properties["LoadState"].(string)
			result.Service.ActiveState, _ = properties["ActiveState"].(string)
			result.Service.SubState, _ = properties["SubState"].(string)
			result.Service.UnitFile, _ = properties["UnitFileState"].(string)
			result.Service.Restarts = service.ParseCounter(properties["NRestarts"])
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

func RenderSnapshot(value Snapshot) string {
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
IGMP version: %d, quickleave enabled: %t, proxy debug logging: %t
IPTV interface: %s (%s, %d IPv4 addresses)
Routes: %s
IPTV default route: %t
Multicast routes: %d (%d packets)
`, value.Version, value.Config.Profile, value.Config.WANInterface, value.Config.VLAN, value.Config.IPTVInterface, value.Config.DHCP,
		value.Config.CustomMAC, value.Config.StaticAddress, value.Config.DHCPOptions, strings.Join(value.Config.NATDestinations, ", "),
		len(value.NAT), strings.Join(value.Config.ProxySourceRanges, ", "), strings.Join(value.Config.LANInterfaces, ", "),
		fallbackText(value.Service.ActiveState), fallbackText(value.Service.SubState), fallbackText(value.Service.UnitFile), value.Service.Restarts,
		fallbackText(value.Service.Proxy), value.Service.ProxyPID, value.Config.IGMPVersion, value.Config.QuickLeave, value.Config.Debug,
		value.Network.Target, fallbackText(value.Network.LinkState), value.Network.AddressCount,
		strings.Join(value.Network.Routes, ", "), value.Network.DefaultRoute, value.Multicast.Routes, value.Multicast.Packets) + renderDownstream(value.Downstream)
}
