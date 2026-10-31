package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/vishvananda/netlink"
)

// dhcpRecoveryTimeout is how long the IPTV interface may stay without its
// lease address before the run is declared failed; udhcpc completes two
// discovery rounds well inside it.
const dhcpRecoveryTimeout = 2 * dhcpAcquireTimeout

var errIPTVAddressLost = errors.New("the IPTV interface lost its IPv4 address and the DHCP client did not restore it")

// watchLeaseAddress reports on the returned channel when link carries none
// of the addresses the DHCP hook recorded as its own for dhcpRecoveryTimeout.
func watchLeaseAddress(ctx context.Context, link netlink.Link) (<-chan error, error) {
	failures := make(chan error, 1)
	updates := make(chan netlink.AddrUpdate, addressUpdateBuffer)
	options := netlink.AddrSubscribeOptions{ErrorCallback: func(err error) { reportFailure(failures, err) }}
	if err := netlink.AddrSubscribeWithOptions(updates, ctx.Done(), options); err != nil {
		return nil, fmt.Errorf("subscribe to address changes: %w", err)
	}
	remaining := func() (int, error) {
		return leaseAddressesOn(leaseStatePath, func() ([]netlink.Addr, error) {
			addresses, err := netlink.AddrList(link, netlink.FAMILY_V4)
			if err != nil {
				return nil, fmt.Errorf("list IPv4 addresses on %s: %w", link.Attrs().Name, err)
			}

			return addresses, nil
		})
	}
	watch := &leaseAddressWatch{linkIndex: link.Attrs().Index, remaining: remaining, timeout: dhcpRecoveryTimeout}
	watch.check()
	go watch.run(ctx, updates, failures)

	return failures, nil
}

// leaseAddressesOn counts the addresses the hook's lease record claims that
// the link still carries. Other addresses on a shared uplink do not count.
func leaseAddressesOn(statePath string, list func() ([]netlink.Addr, error)) (int, error) {
	state, err := readLeaseState(statePath)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	addresses, err := list()
	if err != nil {
		return 0, err
	}
	count := 0
	for _, address := range addresses {
		if slices.ContainsFunc(state.Lease.ManagedAddresses, func(managed netlink.Addr) bool { return managed.IP.Equal(address.IP) }) {
			count++
		}
	}

	return count, nil
}

// leaseAddressWatch times how long the link has been without its lease address.
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
			watch.lost = nil
			if watch.check() {
				continue
			}
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
	watch.check()
}

// check reports whether the link holds a lease address. The hook writes its
// lease record after the kernel has announced the address, so a change is
// judged again when the recovery timer expires.
func (watch *leaseAddressWatch) check() bool {
	count, err := watch.remaining()
	if err == nil && count > 0 {
		watch.restored()

		return true
	}
	watch.arm()

	return false
}

func (watch *leaseAddressWatch) arm() {
	if watch.lost == nil {
		watch.lost = time.NewTimer(watch.timeout)
	}
}

func (watch *leaseAddressWatch) restored() {
	if watch.lost != nil {
		watch.lost.Stop()
		watch.lost = nil
	}
}
