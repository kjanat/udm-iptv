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
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/kjanat/udm-iptv/internal/config"
)

const routeProtocolDHCP = 16

const (
	// ipv4HostBits is the /32 prefix length for a single IPv4 host address.
	ipv4HostBits = 32
	// dhcpBaseMetric keeps DHCP-derived routes below manually configured ones.
	dhcpBaseMetric = 200
)

var (
	errInterfaceNotVLAN        = errors.New("interface already exists and is not a VLAN")
	errInvalidRouteMetric      = errors.New("invalid route metric")
	errMissingDHCPInterface    = errors.New("udhcpc did not provide an interface")
	errLeaseAddressNotIPv4     = errors.New("DHCP lease address must be IPv4")
	errBroadcastNotIPv4        = errors.New("DHCP broadcast address must be IPv4")
	errInvalidClasslessRoutes  = errors.New("invalid RFC3442 classless route option")
	errNegativeRouteMetric     = errors.New("DHCP route metric must not be negative")
	errRouteDestinationNotIPv4 = errors.New("DHCP route destination must be IPv4")
	errInvalidRouteGateway     = errors.New("invalid IPv4 route gateway")
	errInvalidSubnetMask       = errors.New("invalid subnet mask")
)

// Target returns the interface IPTV traffic flows through: the VLAN
// sub-interface when tagged, otherwise the WAN interface itself.
func Target(value config.Config) string {
	if value.WAN.VLAN > 0 {
		return value.WAN.VLANInterface
	}

	return value.WAN.Interface
}

// EnsureLink brings up the WAN interface, creating the VLAN sub-interface when tagged.
func EnsureLink(value config.Config) (netlink.Link, error) {
	parent, err := netlink.LinkByName(value.WAN.Interface)
	if err != nil {
		return nil, fmt.Errorf("find WAN interface %s: %w", value.WAN.Interface, err)
	}
	if value.WAN.VLAN == 0 {
		if err := netlink.LinkSetUp(parent); err != nil {
			return nil, fmt.Errorf("bring up WAN interface %s: %w", value.WAN.Interface, err)
		}

		return parent, nil
	}

	return ensureVLAN(value, parent)
}

func ensureVLAN(value config.Config, parent netlink.Link) (netlink.Link, error) {
	if err := removeManagedVLAN(value.WAN.VLANInterface); err != nil {
		return nil, err
	}
	attributes := netlink.NewLinkAttrs()
	attributes.Name = value.WAN.VLANInterface
	attributes.ParentIndex = parent.Attrs().Index
	vlan := &netlink.Vlan{LinkAttrs: attributes, VlanId: value.WAN.VLAN}
	if err := netlink.LinkAdd(vlan); err != nil {
		return nil, fmt.Errorf("create VLAN interface: %w", err)
	}
	if err := startVLAN(vlan, value.WAN.VLANMAC); err != nil {
		_ = netlink.LinkDel(vlan)

		return nil, err
	}

	return vlan, nil
}

func removeManagedVLAN(name string) error {
	existing, err := netlink.LinkByName(name)
	if err != nil {
		if _, ok := errors.AsType[netlink.LinkNotFoundError](err); !ok {
			return fmt.Errorf("inspect VLAN interface %s: %w", name, err)
		}

		return nil
	}
	if _, ok := existing.(*netlink.Vlan); !ok {
		return fmt.Errorf("%w: %s", errInterfaceNotVLAN, name)
	}
	if err := netlink.LinkDel(existing); err != nil {
		return fmt.Errorf("replace managed VLAN interface %s: %w", name, err)
	}

	return nil
}

func startVLAN(vlan netlink.Link, address string) error {
	if err := applyMAC(vlan, address); err != nil {
		return err
	}
	if err := netlink.LinkSetUp(vlan); err != nil {
		return fmt.Errorf("bring up VLAN interface %s: %w", vlan.Attrs().Name, err)
	}

	return nil
}

func applyMAC(link netlink.Link, address string) error {
	if address == "" {
		return nil
	}
	mac, err := net.ParseMAC(address)
	if err != nil {
		return fmt.Errorf("parse VLAN MAC: %w", err)
	}
	if err := netlink.LinkSetHardwareAddr(link, mac); err != nil {
		return fmt.Errorf("set VLAN MAC %s: %w", address, err)
	}

	return nil
}

// EnsureNAT adds MASQUERADE rules for value's NAT destinations, if not already present.
func EnsureNAT(value config.Config) error {
	table, err := iptables.NewWithProtocol(iptables.ProtocolIPv4)
	if err != nil {
		return fmt.Errorf("open the iptables NAT table: %w", err)
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

// RemoveNAT removes the MASQUERADE rules EnsureNAT added.
func RemoveNAT(value config.Config) error {
	table, err := iptables.NewWithProtocol(iptables.ProtocolIPv4)
	if err != nil {
		return fmt.Errorf("open the iptables NAT table: %w", err)
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

// ApplyStatic sets the configured static address and static routes on link.
func ApplyStatic(value config.Config, link netlink.Link) error {
	if value.WAN.StaticAddress != "" {
		address, err := netlink.ParseAddr(value.WAN.StaticAddress)
		if err != nil {
			return fmt.Errorf("parse static address %s: %w", value.WAN.StaticAddress, err)
		}
		if err := netlink.AddrReplace(link, address); err != nil {
			return fmt.Errorf("apply static address %s: %w", value.WAN.StaticAddress, err)
		}
	}
	for _, raw := range value.WAN.StaticRoutes {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return fmt.Errorf("parse static route %s: %w", raw, err)
		}
		destination := &net.IPNet{IP: prefix.Addr().AsSlice(), Mask: net.CIDRMask(prefix.Bits(), ipv4HostBits)}
		if err := netlink.RouteReplace(&netlink.Route{LinkIndex: link.Attrs().Index, Dst: destination, Protocol: unix.RTPROT_STATIC}); err != nil {
			return fmt.Errorf("apply static route %s: %w", raw, err)
		}
	}

	return nil
}

// ResetLease clears any DHCP-derived routes and addresses from link.
func ResetLease(link netlink.Link) error {
	err := flushDHCPRoutes(link.Attrs().Index)
	if err != nil {
		return err
	}

	return flushAddresses(link)
}

// Lease is a udhcpc lease event, as udhcpc reports it through environment variables.
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

// LeaseFromEnvironment reads a Lease from the udhcpc hook's environment variables.
func LeaseFromEnvironment(action string) (Lease, error) {
	metric := 0
	if raw := first(os.Getenv("IF_METRIC"), os.Getenv("metric")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return Lease{}, fmt.Errorf("%w %q", errInvalidRouteMetric, raw)
		}
		metric = parsed
	}
	lease := Lease{
		Action: action, Interface: os.Getenv("interface"), Address: os.Getenv("ip"),
		Mask: os.Getenv("mask"), Broadcast: os.Getenv("broadcast"),
		Routers: strings.Fields(os.Getenv("router")), StaticRoutes: strings.Fields(os.Getenv("staticroutes")), Metric: metric,
	}
	if lease.Interface == "" {
		return Lease{}, errMissingDHCPInterface
	}

	return lease, nil
}

// ApplyLease reconciles the interface's address and routes with a DHCP lease event.
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
		return nil, 0, errLeaseAddressNotIPv4
	}
	if lease.Broadcast != "" {
		address.Broadcast = net.ParseIP(lease.Broadcast).To4()
		if address.Broadcast == nil {
			return nil, 0, errBroadcastNotIPv4
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

// addStaticRoutes adds the RFC3442 classless static routes, preferring a
// host route over the on-link gateway before adding the route itself.
func addStaticRoutes(add func(destination, gateway string, priority int) error, staticRoutes []string, prefixLength, metric int) error {
	if len(staticRoutes)%2 != 0 {
		return errInvalidClasslessRoutes
	}
	for index := 0; index < len(staticRoutes); index += 2 {
		if prefixLength == ipv4HostBits && staticRoutes[index+1] != "0.0.0.0" {
			if err := add(staticRoutes[index+1]+"/32", "0.0.0.0", metric); err != nil {
				return err
			}
		}
		if err := add(staticRoutes[index], staticRoutes[index+1], metric+index/2); err != nil {
			return err
		}
	}

	return nil
}

// addRouterRoutes adds a default route per advertised router, preferring a
// host route over an off-subnet router before the default route itself.
func addRouterRoutes(add func(destination, gateway string, priority int) error, routers []string, prefixLength, metric int) error {
	for index, gateway := range routers {
		if prefixLength == ipv4HostBits {
			if err := add(gateway+"/32", "0.0.0.0", metric); err != nil {
				return err
			}
		}
		if err := add("0.0.0.0/0", gateway, metric+index); err != nil {
			return err
		}
	}

	return nil
}

func leaseRoutes(lease Lease, linkIndex, prefixLength int, allowDefaultRoute bool) ([]netlink.Route, error) {
	metric, err := leaseMetric(lease.Metric, linkIndex)
	if err != nil {
		return nil, err
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
		if err := addStaticRoutes(add, lease.StaticRoutes, prefixLength, metric); err != nil {
			return nil, err
		}

		return routes, nil
	}
	if allowDefaultRoute {
		if err := addRouterRoutes(add, lease.Routers, prefixLength, metric); err != nil {
			return nil, err
		}
	}

	return routes, nil
}

func leaseMetric(metric, linkIndex int) (int, error) {
	if metric < 0 {
		return 0, errNegativeRouteMetric
	}
	if metric == 0 {
		return dhcpBaseMetric + linkIndex, nil
	}

	return metric, nil
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
		return fmt.Errorf("list DHCP routes: %w", err)
	}
	for _, route := range routes {
		if int(route.Protocol) == routeProtocolDHCP {
			err := netlink.RouteDel(&route)
			if err != nil {
				return fmt.Errorf("remove DHCP route %s: %w", route.Dst, err)
			}
		}
	}

	return nil
}

func flushAddresses(link netlink.Link) error {
	addresses, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		return fmt.Errorf("list addresses on %s: %w", link.Attrs().Name, err)
	}
	for _, address := range addresses {
		err := netlink.AddrDel(link, &address)
		if err != nil {
			return fmt.Errorf("remove address %s: %w", address.IPNet, err)
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
		return netlink.Route{}, errRouteDestinationNotIPv4
	}
	prefix = prefix.Masked()
	route := netlink.Route{
		LinkIndex: linkIndex,
		Dst:       &net.IPNet{IP: prefix.Addr().AsSlice(), Mask: net.CIDRMask(prefix.Bits(), ipv4HostBits)},
		Protocol:  routeProtocolDHCP,
		Priority:  metric,
		Table:     unix.RT_TABLE_MAIN,
		Scope:     netlink.SCOPE_LINK,
	}
	if gateway != "" && gateway != "0.0.0.0" {
		route.Gw = net.ParseIP(gateway).To4()
		if route.Gw == nil {
			return netlink.Route{}, fmt.Errorf("%w %q", errInvalidRouteGateway, gateway)
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
		return 0, fmt.Errorf("%w %q", errInvalidSubnetMask, mask)
	}
	ones, bits := net.IPMask(parsed.To4()).Size()
	if bits != 32 || ones < 0 {
		return 0, fmt.Errorf("%w %q", errInvalidSubnetMask, mask)
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
