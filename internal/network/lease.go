package network

import (
	"errors"
	"fmt"
	"net"
	"reflect"
	"slices"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// Apply the complete desired set before removing obsolete DHCP routes. A failed
// replacement skips cleanup; successful replacements are retained for a retry.
func reconcileLeaseRoutes(link netlink.Link, desired []netlink.Route, tracked *[]netlink.Route, ops leaseOperations) error {
	linkIndex := link.Attrs().Index
	current, err := ops.routes(netlink.FAMILY_V4, &netlink.Route{LinkIndex: linkIndex}, netlink.RT_FILTER_OIF)
	if err != nil {
		return fmt.Errorf("read DHCP routes: %w", err)
	}
	if !owned(link) {
		if err := validateBorrowedRoutes(current, desired, *tracked); err != nil {
			return err
		}
	}
	if err := applyDesiredLeaseRoutes(link, current, desired, tracked, ops); err != nil {
		return err
	}

	if !owned(link) {
		current = slices.DeleteFunc(current, func(route netlink.Route) bool { return !trackedRoute(route, *tracked) })
	}
	if err := removeObsoleteLeaseRoutes(linkIndex, current, desired, ops); err != nil {
		return err
	}
	*tracked = slices.Clone(desired)
	return nil
}

func applyDesiredLeaseRoutes(link netlink.Link, current, desired []netlink.Route, tracked *[]netlink.Route, ops leaseOperations) error {
	installed := make(map[string]bool, len(current))
	for _, route := range current {
		installed[leaseRouteIdentity(route)] = true
	}
	for _, route := range desired {
		if installed[leaseRouteIdentity(route)] {
			continue
		}
		write := ops.replaceRoute
		if !owned(link) && !slices.ContainsFunc(current, func(existing netlink.Route) bool { return leaseRouteKey(existing) == leaseRouteKey(route) }) {
			write = ops.addRoute
		}
		if err := write(&route); err != nil {
			return fmt.Errorf("apply DHCP route %s: %w", route.Dst, err)
		}
		*tracked = slices.DeleteFunc(*tracked, func(old netlink.Route) bool { return leaseRouteKey(old) == leaseRouteKey(route) })
		*tracked = append(*tracked, route)
	}
	return nil
}

var errBorrowedAddressInUse = errors.New("cannot retire the borrowed interface address while another owner depends on its subnet; resolve the remaining addresses or routes first")

var errForeignRoute = errors.New("DHCP route collides with an unowned route; choose a distinct metric or resolve the existing route owner")

var errForeignAddress = errors.New("DHCP address already exists without recorded ownership; use a dedicated interface or resolve the existing address owner")

func trackedRoute(route netlink.Route, tracked []netlink.Route) bool {
	return slices.ContainsFunc(tracked, func(known netlink.Route) bool {
		return reflect.DeepEqual(normalizedLeaseRoute(route), normalizedLeaseRoute(known))
	})
}

// Netlink fills these defaults when reading a route back from the kernel.
// Compare every remaining attribute so another owner's modified route is not
// mistaken for the recorded route just because its destination still matches.
func normalizedLeaseRoute(route netlink.Route) netlink.Route {
	if route.Table == 0 {
		route.Table = unix.RT_TABLE_MAIN
	}
	if route.Type == 0 {
		route.Type = unix.RTN_UNICAST
	}
	if route.Family == 0 {
		route.Family = netlink.FAMILY_V4
	}
	if route.Dst == nil {
		route.Dst = &net.IPNet{IP: net.IPv4zero.To4(), Mask: net.CIDRMask(0, ipv4HostBits)}
	} else {
		route.Dst = &net.IPNet{IP: route.Dst.IP.Mask(route.Dst.Mask).To4(), Mask: route.Dst.Mask}
	}
	route.Gw = route.Gw.To4()
	route.Src = route.Src.To4()
	if len(route.MultiPath) == 0 {
		route.MultiPath = nil
	}
	return route
}

func validateBorrowedRoutes(current, desired, tracked []netlink.Route) error {
	for _, route := range desired {
		for _, existing := range current {
			if leaseRouteKey(existing) == leaseRouteKey(route) && !trackedRoute(existing, tracked) {
				return fmt.Errorf("%w: %s metric %d", errForeignRoute, route.Dst, route.Priority)
			}
		}
	}
	return nil
}

func removeObsoleteLeaseRoutes(linkIndex int, current, desired []netlink.Route, ops leaseOperations) error {
	obsolete := obsoleteLeaseRoutes(linkIndex, current, desired)
	// Delete gateway-dependent routes before their on-link gateway routes.
	for _, viaGateway := range []bool{true, false} {
		for _, route := range obsolete {
			if (len(route.Gw) != 0) != viaGateway {
				continue
			}
			if err := ops.deleteRoute(&route); err != nil {
				return fmt.Errorf("remove obsolete DHCP route %s: %w", route.Dst, err)
			}
		}
	}

	return nil
}

func obsoleteLeaseRoutes(linkIndex int, current, desired []netlink.Route) []netlink.Route {
	keep := make(map[string]bool, len(desired))
	for _, route := range desired {
		keep[leaseRouteKey(route)] = true
	}
	var obsolete []netlink.Route
	for _, route := range current {
		managed := route.LinkIndex == linkIndex && int(route.Protocol) == routeProtocolDHCP
		if managed && !keep[leaseRouteKey(route)] {
			obsolete = append(obsolete, route)
		}
	}

	return obsolete
}

func leaseRouteKey(route netlink.Route) string {
	destination := "0.0.0.0/0"
	if route.Dst != nil {
		destination = (&net.IPNet{IP: route.Dst.IP.Mask(route.Dst.Mask), Mask: route.Dst.Mask}).String()
	}
	table := route.Table
	if table == 0 {
		table = unix.RT_TABLE_MAIN
	}

	return fmt.Sprintf("%d/%d/%s/%d", table, route.LinkIndex, destination, route.Priority)
}

// leaseRouteIdentity also covers the attributes a replacement would change,
// so an unchanged renewal touches nothing.
func leaseRouteIdentity(route netlink.Route) string {
	return fmt.Sprintf("%s/%d/%d/%s", leaseRouteKey(route), route.Protocol, route.Scope, route.Gw)
}

// removeOtherAddresses retires every IPv4 address but desired from an owned
// link. A borrowed link keeps its other addresses except the one the
// previous lease put there.
func removeOtherAddresses(link netlink.Link, desired *netlink.Addr, previous Lease, ops leaseOperations) (bool, error) {
	if !owned(link) {
		return removePreviousAddress(link, desired, previous, ops)
	}
	addresses, err := ops.addresses(link, netlink.FAMILY_V4)
	if err != nil {
		return false, fmt.Errorf("read assigned addresses: %w", err)
	}
	removed := false
	for _, address := range addresses {
		if desired != nil && sameAddress(address, *desired) {
			continue
		}
		err := ops.deleteAddress(link, &address)
		if err != nil && !errors.Is(err, unix.EADDRNOTAVAIL) {
			return removed, fmt.Errorf("remove obsolete address: %w", err)
		}
		removed = true
	}

	return removed, nil
}

func removePreviousAddress(link netlink.Link, desired *netlink.Addr, previous Lease, ops leaseOperations) (bool, error) {
	removed := false
	for _, address := range previous.ManagedAddresses {
		if desired != nil && sameAddress(address, *desired) {
			continue
		}
		if err := protectBorrowedAddress(link, &address, desired, ops); err != nil {
			return removed, err
		}
		err := ops.deleteAddress(link, &address)
		if errors.Is(err, unix.EADDRNOTAVAIL) {
			continue
		}
		if err != nil {
			return removed, fmt.Errorf("remove the previous lease address: %w", err)
		}
		removed = true
	}
	return removed, nil
}

func validateBorrowedLease(link netlink.Link, address *netlink.Addr, desired []netlink.Route, previous Lease, ops leaseOperations) error {
	if owned(link) {
		return nil
	}
	current, err := ops.routes(netlink.FAMILY_V4, &netlink.Route{LinkIndex: link.Attrs().Index}, netlink.RT_FILTER_OIF)
	if err != nil {
		return fmt.Errorf("read DHCP routes: %w", err)
	}
	if err := validateBorrowedRoutes(current, desired, previous.ManagedRoutes); err != nil {
		return err
	}
	addresses, err := ops.addresses(link, netlink.FAMILY_V4)
	if err != nil {
		return fmt.Errorf("read DHCP addresses: %w", err)
	}
	for _, existing := range addresses {
		if existing.IP.Equal(address.IP) && !slices.ContainsFunc(previous.ManagedAddresses, func(known netlink.Addr) bool { return sameAddress(known, existing) }) {
			return fmt.Errorf("%w: %s", errForeignAddress, address)
		}
	}
	return nil
}

// Linux removes dependent routes with the final IPv4 address, and can remove
// secondary addresses when their primary address disappears. Refuse cleanup
// when that would also retire state whose ownership we cannot establish.
func protectBorrowedAddress(link netlink.Link, previous, desired *netlink.Addr, ops leaseOperations) error {
	addresses, err := ops.addresses(link, netlink.FAMILY_V4)
	if err != nil {
		return fmt.Errorf("inspect borrowed interface addresses: %w", err)
	}
	found, remaining, err := borrowedAddressDependents(addresses, previous, desired)
	if err != nil {
		return err
	}
	if !found || remaining > 0 {
		return nil
	}
	routes, err := ops.routes(netlink.FAMILY_V4, &netlink.Route{LinkIndex: link.Attrs().Index}, netlink.RT_FILTER_OIF)
	if err != nil {
		return fmt.Errorf("inspect borrowed interface routes: %w", err)
	}
	for _, route := range routes {
		if route.Protocol != unix.RTPROT_KERNEL {
			return errBorrowedAddressInUse
		}
	}
	return nil
}

func borrowedAddressDependents(addresses []netlink.Addr, previous, desired *netlink.Addr) (bool, int, error) {
	found, remaining := false, 0
	for _, address := range addresses {
		if sameAddress(address, *previous) {
			found = true
			continue
		}
		remaining++
		if desired != nil && sameAddress(address, *desired) {
			continue
		}
		if previous.Contains(address.IP) && previous.Flags&unix.IFA_F_SECONDARY == 0 {
			return found, remaining, errBorrowedAddressInUse
		}
	}
	return found, remaining, nil
}
