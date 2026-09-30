package service

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
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
	present  int
	cancel   context.CancelFunc
}

func startAddressWatch(t *testing.T, present int) *addressWatch {
	t.Helper()
	return startAddressWatchWith(t, present, false)
}

func startAddressWatchWith(t *testing.T, present int, armed bool) *addressWatch {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	watch := &addressWatch{updates: make(chan netlink.AddrUpdate), failures: make(chan error, 1), present: present, cancel: cancel}
	supervisor := &leaseAddressWatch{linkIndex: 7, remaining: func() (int, error) { return watch.present, nil }, timeout: watchTimeout}
	if armed {
		supervisor.arm()
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
	watch.updates <- addressUpdate(7, "10.207.101.3", true)
	watch.expectQuiet(t)

	rebind := startAddressWatch(t, 1)
	rebind.updates <- addressUpdate(7, "10.207.101.2", false)
	rebind.expectQuiet(t)
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
	restored.updates <- addressUpdate(7, "10.207.101.2", true)
	restored.expectQuiet(t)
}
