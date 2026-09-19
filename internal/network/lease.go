package network

import (
	"errors"
	"fmt"
	"net"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// Apply the complete desired set before removing obsolete DHCP routes. A failed
// replacement skips cleanup; successful replacements are retained for a retry.
func reconcileLeaseRoutes(linkIndex int, desired []netlink.Route, ops leaseOperations) error {
	current, err := ops.routes(netlink.FAMILY_V4, &netlink.Route{LinkIndex: linkIndex}, netlink.RT_FILTER_OIF)
	if err != nil {
		return fmt.Errorf("read DHCP routes: %w", err)
	}
	installed := make(map[string]bool, len(current))
	for _, route := range current {
		installed[leaseRouteIdentity(route)] = true
	}
	for _, route := range desired {
		if installed[leaseRouteIdentity(route)] {
			continue
		}
		if err := ops.replaceRoute(&route); err != nil {
			return fmt.Errorf("apply DHCP route %s: %w", route.Dst, err)
		}
	}

	return removeObsoleteLeaseRoutes(linkIndex, current, desired, ops)
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
	if previous.Address == "" {
		return false, nil
	}
	address, _, err := leaseAddress(previous)
	if err != nil {
		return false, err
	}
	if desired != nil && sameAddress(*address, *desired) {
		return false, nil
	}
	err = ops.deleteAddress(link, address)
	if errors.Is(err, unix.EADDRNOTAVAIL) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("remove the previous lease address: %w", err)
	}

	return true, nil
}
