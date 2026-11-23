package service

import (
	"errors"
	"net"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vishvananda/netlink"

	"github.com/kjanat/udm-iptv/internal/network"
)

func TestLeaseStateRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "run", "lease.json")
	lease := network.Lease{Action: "bound", Interface: "iptv", Address: "10.207.71.227", Mask: "20", Routers: []string{"10.207.64.1"}, StaticRoutes: []string{"213.75.112.0/21", "10.207.64.1"}, Options: map[string]string{"dns": "195.121.1.34", "lease": "3600"}}
	if err := writeLeaseState(path, lease, "run-a", nil); err != nil {
		t.Fatal(err)
	}
	state, err := readLeaseState(path)
	if err != nil {
		t.Fatal(err)
	}
	if state.Received.IsZero() || state.Owner != "run-a" || !state.Applied || state.Failure != "" || state.Lease.Address != lease.Address || state.Lease.Options["dns"] != "195.121.1.34" || len(state.Lease.StaticRoutes) != 2 {
		t.Fatalf("lease state = %+v", state)
	}
}

var errRouteRefused = errors.New("apply DHCP routes: route refused")

func TestLeaseStateRecordsAFailedApplication(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "run", "lease.json")
	if err := writeLeaseState(path, network.Lease{Action: "bound", Interface: "iptv"}, "run-a", errRouteRefused); err != nil {
		t.Fatal(err)
	}
	state, err := readLeaseState(path)
	if err != nil {
		t.Fatal(err)
	}
	if state.Applied || state.Failure != errRouteRefused.Error() {
		t.Fatalf("failed application recorded as %+v", state)
	}
}

// The daemon waits for the hook's record of a lease its own client applied
// on this interface, and stops waiting the moment the hook reports it could
// not apply one. A lease another run's client delivered is not this run's.
func TestLeaseReadyNeedsThisRunsAppliedLease(t *testing.T) {
	t.Parallel()
	const owner = "run-b"
	received := time.Date(2026, 9, 19, 2, 0, 0, 0, time.UTC)
	bound := network.Lease{Action: "bound", Interface: "iptv", Address: "10.207.67.179", Mask: "20"}
	for name, test := range map[string]struct {
		state LeaseState
		ready bool
		err   error
	}{
		"applied":         {LeaseState{Received: received, Owner: owner, Applied: true, Lease: bound}, true, nil},
		"renewed":         {LeaseState{Received: received, Owner: owner, Applied: true, Lease: network.Lease{Action: "renew", Interface: "iptv"}}, true, nil},
		"other run":       {LeaseState{Received: received.Add(time.Second), Owner: "run-a", Applied: true, Lease: bound}, false, nil},
		"unowned":         {LeaseState{Received: received.Add(time.Second), Applied: true, Lease: bound}, false, nil},
		"other interface": {LeaseState{Received: received, Owner: owner, Applied: true, Lease: network.Lease{Action: "bound", Interface: "eth8"}}, false, nil},
		"deconfig":        {LeaseState{Received: received, Owner: owner, Applied: true, Lease: network.Lease{Action: "deconfig", Interface: "iptv"}}, false, nil},
		"not applied":     {LeaseState{Received: received, Owner: owner, Failure: "apply DHCP routes: refused", Lease: bound}, false, errLeaseNotApplied},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ready, err := leaseReady(test.state, "iptv", owner)
			if ready != test.ready || !errors.Is(err, test.err) {
				t.Fatalf("ready=%v err=%v", ready, err)
			}
			if test.err != nil && !strings.Contains(err.Error(), test.state.Failure) {
				t.Fatalf("failure text lost: %v", err)
			}
		})
	}
}

func TestFailedLeaseStateRetainsRouteOwnership(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "lease.json")
	lease := network.Lease{Action: "renew", Interface: "eth8", ManagedRoutes: []netlink.Route{{LinkIndex: 52, Dst: &net.IPNet{IP: net.ParseIP("198.51.100.0").To4(), Mask: net.CIDRMask(24, 32)}, Gw: net.ParseIP("192.0.2.1").To4(), Protocol: 16, Priority: 252}}}
	address, err := netlink.ParseAddr("192.0.2.2/24")
	if err != nil {
		t.Fatal(err)
	}
	lease.ManagedAddresses = []netlink.Addr{*address}
	if err := writeLeaseState(path, lease, "run-a", errRouteRefused); err != nil {
		t.Fatal(err)
	}
	state, err := readLeaseState(path)
	if err != nil {
		t.Fatal(err)
	}
	if state.Applied || len(state.Lease.ManagedRoutes) != 1 {
		t.Fatalf("failed lease ownership lost: %+v", state)
	}
	if len(state.Lease.ManagedAddresses) != 1 || state.Lease.ManagedAddresses[0].String() != address.String() {
		t.Fatalf("recorded address ownership lost: %v", state.Lease.ManagedAddresses)
	}
	got, want := state.Lease.ManagedRoutes[0], lease.ManagedRoutes[0]
	// JSON's net.IP parser returns a 16-byte representation of an IPv4 address.
	got.Dst.IP = got.Dst.IP.To4()
	got.Gw = got.Gw.To4()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("recorded route changed: %+v, want %+v", got, want)
	}
}
