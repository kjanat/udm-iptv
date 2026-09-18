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

// linkAlias marks the links this program creates.
const linkAlias = "udm-iptv"

var (
	errInterfaceNotVLAN        = errors.New("interface already exists and is not a VLAN")
	errForeignVLAN             = errors.New("interface already exists, was not created by udm-iptv and is not the configured VLAN")
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
	return value.Target()
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
	if err := removeManagedVLAN(value, parent.Attrs().Index); err != nil {
		return nil, err
	}
	attributes := netlink.NewLinkAttrs()
	attributes.Name = value.WAN.VLANInterface
	attributes.ParentIndex = parent.Attrs().Index
	attributes.Alias = linkAlias
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

func removeManagedVLAN(value config.Config, parentIndex int) error {
	name := value.WAN.VLANInterface
	existing, err := netlink.LinkByName(name)
	if err != nil {
		if _, ok := errors.AsType[netlink.LinkNotFoundError](err); !ok {
			return fmt.Errorf("inspect VLAN interface %s: %w", name, err)
		}

		return nil
	}
	vlan, ok := existing.(*netlink.Vlan)
	if !ok {
		return fmt.Errorf("%w: %s", errInterfaceNotVLAN, name)
	}
	if !managedVLAN(vlan, parentIndex, value.WAN.VLAN) {
		return fmt.Errorf("%w: %s is VLAN %d on link index %d; configured VLAN %d on %s", errForeignVLAN, name, vlan.VlanId, vlan.Attrs().ParentIndex, value.WAN.VLAN, value.WAN.Interface)
	}
	if err := netlink.LinkDel(existing); err != nil {
		return fmt.Errorf("replace managed VLAN interface %s: %w", name, err)
	}

	return nil
}

// managedVLAN accepts a link carrying this program's alias, and an unmarked
// link that already is the configured VLAN on the configured parent.
// Releases before 5.0.0 created the VLAN without an alias.
func managedVLAN(vlan *netlink.Vlan, parentIndex, vlanID int) bool {
	return owned(vlan) || (vlan.Attrs().ParentIndex == parentIndex && vlan.VlanId == vlanID)
}

// owned reports whether this program created link. Addresses on a borrowed
// interface belong to whoever put them there, so cleanup is limited to
// owned links.
func owned(link netlink.Link) bool {
	return link.Attrs().Alias == linkAlias
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

// natTable is the iptables NAT table, narrowed to what NAT reconciliation uses.
type natTable interface {
	List(table, chain string) ([]string, error)
	Delete(table, chain string, rulespec ...string) error
	AppendUnique(table, chain string, rulespec ...string) error
}

const (
	natTableName = "nat"
	natChain     = "POSTROUTING"
	natComment   = "udm-iptv"
)

func openNATTable() (natTable, error) {
	table, err := iptables.NewWithProtocol(iptables.ProtocolIPv4)
	if err != nil {
		return nil, fmt.Errorf("open the iptables NAT table: %w", err)
	}

	return table, nil
}

func natRule(destination, target string) []string {
	return []string{"-d", destination, "-o", target, "-m", "comment", "--comment", natComment, "-j", "MASQUERADE"}
}

// EnsureNAT makes the MASQUERADE rules on the IPTV interface exactly value's
// NAT destinations, removing any other MASQUERADE rule bound to that interface.
func EnsureNAT(value config.Config) error {
	table, err := openNATTable()
	if err != nil {
		return err
	}

	return reconcileNAT(table, value)
}

func reconcileNAT(table natTable, value config.Config) error {
	target := Target(value)
	wanted := map[string]bool{}
	for _, destination := range value.WAN.NATDestinations {
		wanted[destination] = true
	}
	rules, err := masqueradeRules(table, target)
	if err != nil {
		return err
	}
	for _, rule := range rules {
		if rule.managed && wanted[rule.destination] {
			continue
		}
		if err := table.Delete(natTableName, natChain, rule.spec...); err != nil {
			return fmt.Errorf("remove NAT rule for %s: %w", rule.destination, err)
		}
	}
	for _, destination := range value.WAN.NATDestinations {
		if err := table.AppendUnique(natTableName, natChain, natRule(destination, target)...); err != nil {
			return fmt.Errorf("add NAT rule for %s: %w", destination, err)
		}
	}

	return nil
}

// RemoveNAT removes every MASQUERADE rule bound to the IPTV interface.
func RemoveNAT(value config.Config) error {
	table, err := openNATTable()
	if err != nil {
		return err
	}

	return removeNAT(table, value)
}

func removeNAT(table natTable, value config.Config) error {
	rules, err := masqueradeRules(table, Target(value))
	if err != nil {
		return err
	}
	var joined error
	for _, rule := range rules {
		if err := table.Delete(natTableName, natChain, rule.spec...); err != nil {
			joined = errors.Join(joined, fmt.Errorf("remove NAT rule for %s: %w", rule.destination, err))
		}
	}

	return joined
}

type masqueradeRule struct {
	spec        []string
	destination string
	managed     bool
}

// masqueradeRules lists the POSTROUTING MASQUERADE rules that leave through
// target, as iptables -S prints them, so each can be deleted by its own spec.
func masqueradeRules(table natTable, target string) ([]masqueradeRule, error) {
	listed, err := table.List(natTableName, natChain)
	if err != nil {
		return nil, fmt.Errorf("list NAT rules: %w", err)
	}
	var rules []masqueradeRule
	for _, line := range listed {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "-A" || fields[1] != natChain {
			continue
		}
		spec := fields[2:]
		if !hasOption(spec, "-j", "MASQUERADE") || !hasOption(spec, "-o", target) {
			continue
		}
		rules = append(rules, masqueradeRule{
			spec:        spec,
			destination: optionValue(spec, "-d"),
			managed:     hasOption(spec, "--comment", natComment),
		})
	}

	return rules, nil
}

func hasOption(spec []string, flag, value string) bool {
	return optionValue(spec, flag) == value
}

func optionValue(spec []string, flag string) string {
	for i := 0; i+1 < len(spec); i++ {
		if spec[i] == flag {
			return spec[i+1]
		}
	}

	return ""
}

// ApplyStatic sets the configured static address and static routes on link.
// On an owned link it also retires any IPv4 address a previous configuration
// left behind.
func ApplyStatic(value config.Config, link netlink.Link) error {
	if value.WAN.StaticAddress != "" {
		address, err := netlink.ParseAddr(value.WAN.StaticAddress)
		if err != nil {
			return fmt.Errorf("parse static address %s: %w", value.WAN.StaticAddress, err)
		}
		if err := netlink.AddrReplace(link, address); err != nil {
			return fmt.Errorf("apply static address %s: %w", value.WAN.StaticAddress, err)
		}
		if _, err := removeOtherAddresses(link, address, systemOperations()); err != nil {
			return fmt.Errorf("retire the previous static address: %w", err)
		}
	}
	return ApplyStaticRoutes(value, link)
}

// ApplyStaticRoutes installs the configured unicast routes on link.
func ApplyStaticRoutes(value config.Config, link netlink.Link) error {
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

// ResetLease clears any DHCP-derived routes from link, and every IPv4
// address when this program owns the link.
func ResetLease(link netlink.Link) error {
	err := flushDHCPRoutes(link.Attrs().Index)
	if err != nil || !owned(link) {
		return err
	}

	return flushAddresses(link)
}

// Lease is a udhcpc lease event, as udhcpc reports it through environment variables.
type Lease struct {
	Action       string            `json:"action"`
	Interface    string            `json:"interface"`
	Address      string            `json:"address"`
	Mask         string            `json:"mask"`
	Broadcast    string            `json:"broadcast"`
	Routers      []string          `json:"routers"`
	StaticRoutes []string          `json:"staticRoutes"`
	Metric       int               `json:"metric"`
	Options      map[string]string `json:"options"`
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
		Options: dhcpOptions(os.Environ()),
	}
	if lease.Interface == "" {
		return Lease{}, errMissingDHCPInterface
	}

	return lease, nil
}

// udhcpc exports every option it received as a lower-case variable named
// after the option, so those are the lease as the server sent it.
func dhcpOptions(environment []string) map[string]string {
	options := map[string]string{}
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" || strings.ToLower(key) != key || key == "interface" {
			continue
		}
		options[key] = value
	}

	return options
}

// ApplyLease reconciles the interface's address and routes with a DHCP lease event.
func ApplyLease(lease Lease, policy config.RoutePolicy) error {
	return applyLease(lease, policy, systemOperations())
}

func systemOperations() leaseOperations {
	return leaseOperations{
		link: netlink.LinkByName, addresses: netlink.AddrList,
		replaceAddress: netlink.AddrReplace, deleteAddress: netlink.AddrDel,
		up: netlink.LinkSetUp, routes: netlink.RouteListFiltered,
		replaceRoute: netlink.RouteReplace, deleteRoute: netlink.RouteDel,
	}
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

func applyLease(lease Lease, policy config.RoutePolicy, ops leaseOperations) error {
	if lease.Action != "deconfig" && lease.Action != "bound" && lease.Action != "renew" {
		return nil
	}
	link, err := ops.link(lease.Interface)
	if err != nil {
		return fmt.Errorf("find DHCP interface: %w", err)
	}
	if lease.Action == "deconfig" {
		return clearLease(link, lease, ops)
	}
	address, prefixLength, err := leaseAddress(lease)
	if err != nil {
		return err
	}
	// Validate every route before changing the working lease.
	routes, err := leaseRoutes(lease, link.Attrs().Index, prefixLength, policy)
	if err != nil {
		return fmt.Errorf("validate DHCP routes: %w", err)
	}
	return installLease(link, address, routes, ops)
}

// clearLease removes the lease's routes, and its addresses: every IPv4
// address on an owned link, and on a borrowed link only the address the
// previous lease assigned, when the caller knows it.
func clearLease(link netlink.Link, lease Lease, ops leaseOperations) error {
	if err := reconcileLeaseRoutes(link.Attrs().Index, nil, ops); err != nil {
		return fmt.Errorf("clear DHCP routes: %w", err)
	}
	if owned(link) {
		if _, err := removeOtherAddresses(link, nil, ops); err != nil {
			return fmt.Errorf("clear DHCP addresses: %w", err)
		}

		return nil
	}
	if lease.Address == "" {
		return nil
	}
	address, _, err := leaseAddress(lease)
	if err != nil {
		return err
	}
	if err := ops.deleteAddress(link, address); err != nil && !errors.Is(err, unix.EADDRNOTAVAIL) {
		return fmt.Errorf("clear DHCP address: %w", err)
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
	removed, err := removeOtherAddresses(link, address, ops)
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
func addStaticRoutes(add func(destination, gateway string, priority int) error, staticRoutes []string, prefixLength, metric int, allowDefault bool) error {
	if len(staticRoutes)%2 != 0 {
		return errInvalidClasslessRoutes
	}
	for index := 0; index < len(staticRoutes); index += 2 {
		if !allowDefault && defaultDestination(staticRoutes[index]) {
			continue
		}
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

func leaseRoutes(lease Lease, linkIndex, prefixLength int, policy config.RoutePolicy) ([]netlink.Route, error) {
	if policy == config.RoutesNone {
		return nil, nil
	}
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
		if err := addStaticRoutes(add, lease.StaticRoutes, prefixLength, metric, policy.AllowsDefault()); err != nil {
			return nil, err
		}

		return routes, nil
	}
	if policy.AllowsDefault() {
		if err := addRouterRoutes(add, lease.Routers, prefixLength, metric); err != nil {
			return nil, err
		}
	}

	return routes, nil
}

// A lease can carry a default route in RFC3442 option 121 just as it can in
// the Router option, so the policy has to recognise it in both.
func defaultDestination(destination string) bool {
	prefix, err := netip.ParsePrefix(destination)

	return err == nil && prefix.Bits() == 0
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
