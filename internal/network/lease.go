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
	for _, route := range desired {
		unchanged := false
		for _, old := range current {
			if leaseRouteKey(old) == leaseRouteKey(route) && old.Protocol == route.Protocol && old.Scope == route.Scope && old.Gw.Equal(route.Gw) {
				unchanged = true

				break
			}
		}
		if !unchanged {
			err := ops.replaceRoute(&route)
			if err != nil {
				return fmt.Errorf("apply DHCP route %s: %w", route.Dst, err)
			}
		}
	}
	// Delete gateway-dependent routes before their on-link gateway routes.
	for _, gatewayRoute := range []bool{false, true} {
		for _, old := range current {
			if old.LinkIndex != linkIndex || int(old.Protocol) != routeProtocolDHCP || (len(old.Gw) == 0) != gatewayRoute {
				continue
			}
			keep := false
			for _, route := range desired {
				if leaseRouteKey(old) == leaseRouteKey(route) {
					keep = true

					break
				}
			}
			if !keep {
				err := ops.deleteRoute(&old)
				if err != nil {
					return fmt.Errorf("remove obsolete DHCP route %s: %w", old.Dst, err)
				}
			}
		}
	}

	return nil
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

func removeOldLeaseAddresses(link netlink.Link, desired *netlink.Addr, ops leaseOperations) (bool, error) {
	addresses, err := ops.addresses(link, netlink.FAMILY_V4)
	if err != nil {
		return false, fmt.Errorf("read assigned DHCP addresses: %w", err)
	}
	removed := false
	for _, address := range addresses {
		if desired != nil && sameAddress(address, *desired) {
			continue
		}
		err := ops.deleteAddress(link, &address)
		if err != nil && !errors.Is(err, unix.EADDRNOTAVAIL) {
			return removed, fmt.Errorf("remove obsolete DHCP address: %w", err)
		}
		removed = true
	}

	return removed, nil
}
