package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/vishvananda/netlink"
)

// dhcpRecoveryTimeout is how long the IPTV interface may stay without an
// IPv4 address after startup before the run is declared failed; udhcpc
// completes two discovery rounds well inside it.
const dhcpRecoveryTimeout = 2 * dhcpAcquireTimeout

var errIPTVAddressLost = errors.New("the IPTV interface lost its IPv4 address and the DHCP client did not restore it")

// watchLeaseAddress reports on the returned channel when link keeps no IPv4
// address for dhcpRecoveryTimeout. A renewal replaces the address in place
// and a rebind installs the new one before retiring the old, so neither
// leaves the link bare.
func watchLeaseAddress(ctx context.Context, link netlink.Link) (<-chan error, error) {
	failures := make(chan error, 1)
	updates := make(chan netlink.AddrUpdate, addressUpdateBuffer)
	options := netlink.AddrSubscribeOptions{ErrorCallback: func(err error) { reportFailure(failures, err) }}
	if err := netlink.AddrSubscribeWithOptions(updates, ctx.Done(), options); err != nil {
		return nil, fmt.Errorf("subscribe to address changes: %w", err)
	}
	remaining := func() (int, error) {
		addresses, err := netlink.AddrList(link, netlink.FAMILY_V4)
		if err != nil {
			return 0, fmt.Errorf("list IPv4 addresses on %s: %w", link.Attrs().Name, err)
		}

		return len(addresses), nil
	}
	watch := &leaseAddressWatch{linkIndex: link.Attrs().Index, remaining: remaining, timeout: dhcpRecoveryTimeout}
	watch.arm()
	go watch.run(ctx, updates, failures)

	return failures, nil
}

// leaseAddressWatch times how long the link has been without an IPv4 address.
type leaseAddressWatch struct {
	linkIndex int
	remaining func() (int, error)
	timeout   time.Duration
	lost      *time.Timer
}

func (watch *leaseAddressWatch) run(ctx context.Context, updates <-chan netlink.AddrUpdate, failures chan<- error) {
	defer watch.restored()
	for {
		select {
		case <-ctx.Done():
			return
		case <-watch.expired():
			reportFailure(failures, errIPTVAddressLost)

			return
		case update, ok := <-updates:
			if !ok {
				if ctx.Err() == nil {
					reportFailure(failures, errAddressSubscriptionClosed)
				}

				return
			}
			watch.observe(update)
		}
	}
}

func (watch *leaseAddressWatch) expired() <-chan time.Time {
	if watch.lost == nil {
		return nil
	}

	return watch.lost.C
}

func (watch *leaseAddressWatch) observe(update netlink.AddrUpdate) {
	if update.LinkIndex != watch.linkIndex || update.LinkAddress.IP.To4() == nil {
		return
	}
	if update.NewAddr {
		watch.restored()

		return
	}
	watch.arm()
}

// arm starts the recovery timer when the link holds no IPv4 address. Netlink
// does not replay a deletion that happened before the subscription.
func (watch *leaseAddressWatch) arm() {
	if watch.lost != nil {
		return
	}
	count, err := watch.remaining()
	if err == nil && count > 0 {
		return
	}
	watch.lost = time.NewTimer(watch.timeout)
}

func (watch *leaseAddressWatch) restored() {
	if watch.lost != nil {
		watch.lost.Stop()
		watch.lost = nil
	}
}
