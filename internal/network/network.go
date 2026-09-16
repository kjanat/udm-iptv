package network

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"

	"github.com/coreos/go-iptables/iptables"
	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const routeProtocolDHCP = 16

func Target(value config.Config) string {
	if value.WAN.VLAN > 0 {
		return value.WAN.VLANInterface
	}

	return value.WAN.Interface
}

func EnsureLink(value config.Config) (netlink.Link, error) {
	if value.WAN.VLAN == 0 {
		link, err := netlink.LinkByName(value.WAN.Interface)
		if err != nil {
			return nil, fmt.Errorf("find WAN interface %s: %w", value.WAN.Interface, err)
		}

		return link, netlink.LinkSetUp(link)
	}
	parent, err := netlink.LinkByName(value.WAN.Interface)
	if err != nil {
		return nil, fmt.Errorf("find WAN interface %s: %w", value.WAN.Interface, err)
	}
	existing, lookupErr := netlink.LinkByName(value.WAN.VLANInterface)
	if lookupErr == nil {
		if _, ok := existing.(*netlink.Vlan); !ok {
			return nil, fmt.Errorf("interface %s already exists and is not a VLAN", value.WAN.VLANInterface)
		}
		err := netlink.LinkDel(existing)
		if err != nil {
			return nil, fmt.Errorf("replace managed VLAN interface %s: %w", value.WAN.VLANInterface, err)
		}
	} else {
		if _, ok := errors.AsType[netlink.LinkNotFoundError](lookupErr); !ok {
			return nil, fmt.Errorf("inspect VLAN interface %s: %w", value.WAN.VLANInterface, lookupErr)
		}
	}
	attributes := netlink.NewLinkAttrs()
	attributes.Name = value.WAN.VLANInterface
	attributes.ParentIndex = parent.Attrs().Index
	vlan := &netlink.Vlan{LinkAttrs: attributes, VlanId: value.WAN.VLAN}
	if err := netlink.LinkAdd(vlan); err != nil {
		return nil, fmt.Errorf("create VLAN interface: %w", err)
	}
	if err := applyMAC(vlan, value.WAN.VLANMAC); err != nil {
		_ = netlink.LinkDel(vlan)

		return nil, err
	}
	if err := netlink.LinkSetUp(vlan); err != nil {
		_ = netlink.LinkDel(vlan)

		return nil, err
	}

	return vlan, nil
}

func applyMAC(link netlink.Link, address string) error {
	if address == "" {
		return nil
	}
	mac, err := net.ParseMAC(address)
	if err != nil {
		return fmt.Errorf("parse VLAN MAC: %w", err)
	}

	return netlink.LinkSetHardwareAddr(link, mac)
}

func EnsureNAT(value config.Config) error {
	table, err := iptables.NewWithProtocol(iptables.ProtocolIPv4)
	if err != nil {
		return err
	}
	for _, destination := range value.WAN.NATDestinations {
		rule := []string{"-d", destination, "-o", Target(value), "-j", "MASQUERADE", "-m", "comment", "--comment", "udm-iptv"}
		err := table.AppendUnique("nat", "POSTROUTING", rule...)
		if err != nil {
			return fmt.Errorf("add NAT rule for %s: %w", destination, err)
		}
	}

	return nil
}

func RemoveNAT(value config.Config) error {
	table, err := iptables.NewWithProtocol(iptables.ProtocolIPv4)
	if err != nil {
		return err
	}
	var joined error
	for _, destination := range value.WAN.NATDestinations {
		rule := []string{"-d", destination, "-o", Target(value), "-j", "MASQUERADE", "-m", "comment", "--comment", "udm-iptv"}
		if exists, checkErr := table.Exists("nat", "POSTROUTING", rule...); checkErr != nil {
			joined = errors.Join(joined, checkErr)
		} else if exists {
			joined = errors.Join(joined, table.Delete("nat", "POSTROUTING", rule...))
		}
	}

	return joined
}

func ApplyStatic(value config.Config, link netlink.Link) error {
	if value.WAN.StaticAddress != "" {
		address, err := netlink.ParseAddr(value.WAN.StaticAddress)
		if err != nil {
			return err
		}
		if err := netlink.AddrReplace(link, address); err != nil {
			return err
		}
	}
	for _, raw := range value.WAN.StaticRoutes {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return err
		}
		destination := &net.IPNet{IP: prefix.Addr().AsSlice(), Mask: net.CIDRMask(prefix.Bits(), 32)}
		if err := netlink.RouteReplace(&netlink.Route{LinkIndex: link.Attrs().Index, Dst: destination, Protocol: unix.RTPROT_STATIC}); err != nil {
			return err
		}
	}

	return nil
}

func ResetLease(link netlink.Link) error {
	err := flushDHCPRoutes(link.Attrs().Index)
	if err != nil {
		return err
	}

	return flushAddresses(link)
}

type Lease struct {
	Action       string
	Interface    string
	Address      string
	Mask         string
	Broadcast    string
	Routers      []string
	StaticRoutes []string
	Metric       int
}

func LeaseFromEnvironment(action string) (Lease, error) {
	metric := 0
	if raw := first(os.Getenv("IF_METRIC"), os.Getenv("metric")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return Lease{}, fmt.Errorf("invalid route metric %q", raw)
		}
		metric = parsed
	}
	lease := Lease{
		Action: action, Interface: os.Getenv("interface"), Address: os.Getenv("ip"),
		Mask: os.Getenv("mask"), Broadcast: os.Getenv("broadcast"),
		Routers: strings.Fields(os.Getenv("router")), StaticRoutes: strings.Fields(os.Getenv("staticroutes")), Metric: metric,
	}
	if lease.Interface == "" {
		return Lease{}, errors.New("udhcpc did not provide an interface")
	}

	return lease, nil
}

func ApplyLease(lease Lease, allowDefaultRoute bool) error {
	return applyLease(lease, allowDefaultRoute, leaseOperations{
		link: netlink.LinkByName, addresses: netlink.AddrList,
		replaceAddress: netlink.AddrReplace, deleteAddress: netlink.AddrDel,
		up: netlink.LinkSetUp, routes: netlink.RouteListFiltered,
		replaceRoute: netlink.RouteReplace, deleteRoute: netlink.RouteDel,
	})
}

type leaseOperations struct {
	link           func(string) (netlink.Link, error)
	addresses      func(netlink.Link, int) ([]netlink.Addr, error)
	replaceAddress func(netlink.Link, *netlink.Addr) error
	deleteAddress  func(netlink.Link, *netlink.Addr) error
	up             func(netlink.Link) error
	routes         func(int, *netlink.Route, uint64) ([]netlink.Route, error)
	replaceRoute   func(*netlink.Route) error
	deleteRoute    func(*netlink.Route) error
}

func applyLease(lease Lease, allowDefaultRoute bool, ops leaseOperations) error {
	if lease.Action != "deconfig" && lease.Action != "bound" && lease.Action != "renew" {
		return nil
	}
	link, err := ops.link(lease.Interface)
	if err != nil {
		return fmt.Errorf("find DHCP interface: %w", err)
	}
	if lease.Action == "deconfig" {
		return clearLease(link, ops)
	}
	address, prefixLength, err := leaseAddress(lease)
	if err != nil {
		return err
	}
	// Validate every route before changing the working lease.
	routes, err := leaseRoutes(lease, link.Attrs().Index, prefixLength, allowDefaultRoute)
	if err != nil {
		return fmt.Errorf("validate DHCP routes: %w", err)
	}
	return installLease(link, address, routes, ops)
}

func clearLease(link netlink.Link, ops leaseOperations) error {
	if err := reconcileLeaseRoutes(link.Attrs().Index, nil, ops); err != nil {
		return fmt.Errorf("clear DHCP routes: %w", err)
	}
	if _, err := removeOldLeaseAddresses(link, nil, ops); err != nil {
		return fmt.Errorf("clear DHCP addresses: %w", err)
	}
	return nil
}

func leaseAddress(lease Lease) (*netlink.Addr, int, error) {
	prefixLength, err := maskBits(lease.Mask)
	if err != nil {
		return nil, 0, fmt.Errorf("parse DHCP subnet mask: %w", err)
	}
	address, err := netlink.ParseAddr(fmt.Sprintf("%s/%d", lease.Address, prefixLength))
	if err != nil {
		return nil, 0, fmt.Errorf("parse DHCP address: %w", err)
	}
	if address.IP.To4() == nil {
		return nil, 0, errors.New("DHCP lease address must be IPv4")
	}
	if lease.Broadcast != "" {
		address.Broadcast = net.ParseIP(lease.Broadcast).To4()
		if address.Broadcast == nil {
			return nil, 0, errors.New("DHCP broadcast address must be IPv4")
		}
	}
	return address, prefixLength, nil
}

func installLease(link netlink.Link, address *netlink.Addr, routes []netlink.Route, ops leaseOperations) error {
	if err := ops.replaceAddress(link, address); err != nil {
		return fmt.Errorf("apply DHCP address: %w", err)
	}
	if err := ops.up(link); err != nil {
		return fmt.Errorf("bring DHCP interface up: %w", err)
	}
	if err := reconcileLeaseRoutes(link.Attrs().Index, routes, ops); err != nil {
		return fmt.Errorf("apply DHCP routes: %w", err)
	}
	removed, err := removeOldLeaseAddresses(link, address, ops)
	if err != nil {
		return fmt.Errorf("retire previous DHCP address: %w", err)
	}
	if !removed {
		return nil
	}
	// Removing a primary address can remove secondary addresses and routes.
	if err := ops.replaceAddress(link, address); err != nil {
		return fmt.Errorf("restore DHCP address after cleanup: %w", err)
	}
	if err := reconcileLeaseRoutes(link.Attrs().Index, routes, ops); err != nil {
		return fmt.Errorf("restore DHCP routes after cleanup: %w", err)
	}
	return nil
}

func leaseRoutes(lease Lease, linkIndex, prefixLength int, allowDefaultRoute bool) ([]netlink.Route, error) {
	metric := lease.Metric
	if metric < 0 {
		return nil, errors.New("DHCP route metric must not be negative")
	}
	if metric == 0 {
		metric = 200 + linkIndex
	}
	var routes []netlink.Route
	add := func(destination, gateway string, priority int) error {
		route, err := dhcpRoute(linkIndex, destination, gateway, priority)
		if err == nil {
			routes = append(routes, route)
		}

		return err
	}
	if len(lease.StaticRoutes) > 0 {
		if len(lease.StaticRoutes)%2 != 0 {
			return nil, errors.New("invalid RFC3442 classless route option")
		}
		for index := 0; index < len(lease.StaticRoutes); index += 2 {
			if prefixLength == 32 && lease.StaticRoutes[index+1] != "0.0.0.0" {
				err := add(lease.StaticRoutes[index+1]+"/32", "0.0.0.0", metric)
				if err != nil {
					return nil, err
				}
			}
			err := add(lease.StaticRoutes[index], lease.StaticRoutes[index+1], metric+index/2)
			if err != nil {
				return nil, err
			}
		}

		return routes, nil
	}
	if allowDefaultRoute {
		for index, gateway := range lease.Routers {
			if prefixLength == 32 {
				err := add(gateway+"/32", "0.0.0.0", metric)
				if err != nil {
					return nil, err
				}
			}
			err := add("0.0.0.0/0", gateway, metric+index)
			if err != nil {
				return nil, err
			}
		}
	}

	return routes, nil
}

func sameAddress(left, right netlink.Addr) bool {
	if left.IP == nil || right.IP == nil || !left.IP.Equal(right.IP) {
		return false
	}
	leftBits, leftSize := left.Mask.Size()
	rightBits, rightSize := right.Mask.Size()

	return leftBits == rightBits && leftSize == rightSize
}

func flushDHCPRoutes(linkIndex int) error {
	routes, err := netlink.RouteListFiltered(netlink.FAMILY_V4, &netlink.Route{LinkIndex: linkIndex}, netlink.RT_FILTER_OIF)
	if err != nil {
		return err
	}
	for _, route := range routes {
		if int(route.Protocol) == routeProtocolDHCP {
			err := netlink.RouteDel(&route)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func flushAddresses(link netlink.Link) error {
	addresses, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		return err
	}
	for _, address := range addresses {
		err := netlink.AddrDel(link, &address)
		if err != nil {
			return err
		}
	}

	return nil
}

func dhcpRoute(linkIndex int, destination, gateway string, metric int) (netlink.Route, error) {
	prefix, err := netip.ParsePrefix(destination)
	if err != nil {
		return netlink.Route{}, fmt.Errorf("invalid route destination %q: %w", destination, err)
	}
	if !prefix.Addr().Is4() {
		return netlink.Route{}, errors.New("DHCP route destination must be IPv4")
	}
	prefix = prefix.Masked()
	route := netlink.Route{
		LinkIndex: linkIndex,
		Dst:       &net.IPNet{IP: prefix.Addr().AsSlice(), Mask: net.CIDRMask(prefix.Bits(), 32)},
		Protocol:  routeProtocolDHCP,
		Priority:  metric,
		Table:     unix.RT_TABLE_MAIN,
		Scope:     netlink.SCOPE_LINK,
	}
	if gateway != "" && gateway != "0.0.0.0" {
		route.Gw = net.ParseIP(gateway).To4()
		if route.Gw == nil {
			return netlink.Route{}, fmt.Errorf("invalid IPv4 route gateway %q", gateway)
		}
		route.Scope = netlink.SCOPE_UNIVERSE
	}

	return route, nil
}

func maskBits(mask string) (int, error) {
	if bits, err := strconv.Atoi(mask); err == nil && bits >= 0 && bits <= 32 {
		return bits, nil
	}
	parsed := net.ParseIP(mask)
	if parsed == nil {
		return 0, fmt.Errorf("invalid subnet mask %q", mask)
	}
	ones, bits := net.IPMask(parsed.To4()).Size()
	if bits != 32 || ones < 0 {
		return 0, fmt.Errorf("invalid subnet mask %q", mask)
	}

	return ones, nil
}

func first(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}

	return ""
}
