package service

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vishvananda/netlink"

	"github.com/kjanat/udm-iptv/internal/network"
)

const watchTimeout = 20 * time.Millisecond

func addressUpdate(linkIndex int, ip string, added bool) netlink.AddrUpdate {
	return netlink.AddrUpdate{
		LinkAddress: net.IPNet{IP: net.ParseIP(ip), Mask: net.CIDRMask(20, 32)},
		LinkIndex:   linkIndex, NewAddr: added,
	}
}

type addressWatch struct {
	updates  chan netlink.AddrUpdate
	failures chan error
	present  atomic.Int32
	cancel   context.CancelFunc
}

func startAddressWatch(t *testing.T, present int32) *addressWatch {
	t.Helper()
	return startAddressWatchWith(t, present, false)
}

func startAddressWatchWith(t *testing.T, present int32, checked bool) *addressWatch {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	watch := &addressWatch{updates: make(chan netlink.AddrUpdate), failures: make(chan error, 1), cancel: cancel}
	watch.present.Store(present)
	supervisor := &leaseAddressWatch{linkIndex: 7, remaining: func() (int, error) { return int(watch.present.Load()), nil }, timeout: watchTimeout}
	if checked {
		supervisor.check()
	}
	go supervisor.run(ctx, watch.updates, watch.failures)

	return watch
}

func (watch *addressWatch) expectFailure(t *testing.T, want error) {
	t.Helper()
	select {
	case err := <-watch.failures:
		if !errors.Is(err, want) {
			t.Fatalf("failure = %v; want %v", err, want)
		}
	case <-time.After(10 * watchTimeout):
		t.Fatal("address loss was not reported")
	}
}

func (watch *addressWatch) expectQuiet(t *testing.T) {
	t.Helper()
	select {
	case err := <-watch.failures:
		t.Fatalf("unexpected failure: %v", err)
	case <-time.After(3 * watchTimeout):
	}
}

// A lease that is not restored within the recovery window fails the run.
func TestLeaseAddressLossIsReportedAfterTheRecoveryWindow(t *testing.T) {
	t.Parallel()
	watch := startAddressWatch(t, 0)
	watch.updates <- addressUpdate(7, "10.207.101.2", false)
	watch.expectFailure(t, errIPTVAddressLost)
}

// A deconfig followed by a fresh bound within the window is a renewal, and a
// rebind installs the new address before the old one goes.
func TestLeaseAddressRecoveryWithinTheWindowIsNotAFailure(t *testing.T) {
	t.Parallel()
	watch := startAddressWatch(t, 0)
	watch.updates <- addressUpdate(7, "10.207.101.2", false)
	watch.present.Store(1)
	watch.updates <- addressUpdate(7, "10.207.101.3", true)
	watch.expectQuiet(t)

	rebind := startAddressWatch(t, 1)
	rebind.updates <- addressUpdate(7, "10.207.101.2", false)
	rebind.expectQuiet(t)
}

// The hook records its lease after the kernel announces the address, so an
// address that arrives before the record is judged again when the timer
// expires.
func TestLeaseAddressRecordedAfterTheKernelEventIsNotAFailure(t *testing.T) {
	t.Parallel()
	watch := startAddressWatch(t, 0)
	watch.updates <- addressUpdate(7, "10.207.101.2", false)
	watch.updates <- addressUpdate(7, "10.207.101.3", true)
	watch.present.Store(1)
	watch.expectQuiet(t)
}

// An address another owner adds to a shared uplink is not the lease.
func TestLeaseAddressWatchIgnoresForeignAddresses(t *testing.T) {
	t.Parallel()
	watch := startAddressWatch(t, 0)
	watch.updates <- addressUpdate(7, "10.207.101.2", false)
	watch.updates <- addressUpdate(7, "192.0.2.10", true)
	watch.expectFailure(t, errIPTVAddressLost)
}

// Other links and IPv6 addresses do not concern the lease.
func TestLeaseAddressWatchIgnoresOtherLinksAndFamilies(t *testing.T) {
	t.Parallel()
	watch := startAddressWatch(t, 0)
	watch.updates <- addressUpdate(8, "10.207.101.2", false)
	watch.updates <- netlink.AddrUpdate{LinkAddress: net.IPNet{IP: net.ParseIP("fe80::1"), Mask: net.CIDRMask(64, 128)}, LinkIndex: 7}
	watch.expectQuiet(t)
}

func TestLeaseAddressWatchStopsWithTheRun(t *testing.T) {
	t.Parallel()
	watch := startAddressWatch(t, 0)
	watch.updates <- addressUpdate(7, "10.207.101.2", false)
	watch.cancel()
	watch.expectQuiet(t)
	closed := startAddressWatch(t, 0)
	close(closed.updates)
	closed.expectFailure(t, errAddressSubscriptionClosed)
}

// A deletion that happened before the subscription is never delivered, so
// the watch checks the link once when it starts.
func TestLeaseAddressWatchChecksTheLinkAtStart(t *testing.T) {
	t.Parallel()
	bare := startAddressWatchWith(t, 0, true)
	bare.expectFailure(t, errIPTVAddressLost)
	held := startAddressWatchWith(t, 1, true)
	held.expectQuiet(t)
	restored := startAddressWatchWith(t, 0, true)
	restored.present.Store(1)
	restored.updates <- addressUpdate(7, "10.207.101.2", true)
	restored.expectQuiet(t)
}

func TestLeaseAddressesOnCountsOnlyTheHooksAddresses(t *testing.T) {
	t.Parallel()
	lease, _ := netlink.ParseAddr("10.207.101.2/20")
	firmware, _ := netlink.ParseAddr("192.0.2.10/24")
	path := filepath.Join(t.TempDir(), "lease.json")
	list := func(addresses ...netlink.Addr) func() ([]netlink.Addr, error) {
		return func() ([]netlink.Addr, error) { return addresses, nil }
	}
	if count, err := leaseAddressesOn(path, list(*lease, *firmware)); err != nil || count != 0 {
		t.Fatalf("without a lease record: %d, %v", count, err)
	}
	record := network.Lease{Action: "bound", Interface: "eth8", ManagedAddresses: []netlink.Addr{*lease}}
	if err := writeLeaseState(path, record, "run", nil); err != nil {
		t.Fatal(err)
	}
	if count, err := leaseAddressesOn(path, list(*firmware)); err != nil || count != 0 {
		t.Fatalf("firmware address only: %d, %v", count, err)
	}
	if count, err := leaseAddressesOn(path, list(*firmware, *lease)); err != nil || count != 1 {
		t.Fatalf("firmware and lease addresses: %d, %v", count, err)
	}
	if _, err := leaseAddressesOn(path, func() ([]netlink.Addr, error) { return nil, os.ErrPermission }); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("listing failure lost: %v", err)
	}
}
