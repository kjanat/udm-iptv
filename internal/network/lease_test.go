package network

import (
	"errors"
	"reflect"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

type leaseFixture struct {
	routes        []netlink.Route
	addresses     []netlink.Addr
	changes       []string
	failReplace   bool
	dropSecondary bool
}

func (f *leaseFixture) ops() leaseOperations {
	return leaseOperations{
		link: func(string) (netlink.Link, error) {
			return &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Index: 52}}, nil
		},
		addresses: func(netlink.Link, int) ([]netlink.Addr, error) { return f.addresses, nil },
		replaceAddress: func(_ netlink.Link, a *netlink.Addr) error {
			f.changes = append(f.changes, "address")
			for _, old := range f.addresses {
				if sameAddress(old, *a) {
					return nil
				}
			}
			f.addresses = append(f.addresses, *a)
			return nil
		},
		deleteAddress: func(_ netlink.Link, a *netlink.Addr) error {
			f.changes = append(f.changes, "delete-address")
			if f.dropSecondary {
				f.addresses = nil
				f.routes = nil
				return nil
			}
			for i, old := range f.addresses {
				if sameAddress(old, *a) {
					f.addresses = append(f.addresses[:i:i], f.addresses[i+1:]...)
					break
				}
			}
			return nil
		},
		up: func(netlink.Link) error { f.changes = append(f.changes, "up"); return nil },
		routes: func(int, *netlink.Route, uint64) ([]netlink.Route, error) {
			return append([]netlink.Route(nil), f.routes...), nil
		},
		replaceRoute: func(r *netlink.Route) error {
			f.changes = append(f.changes, "replace-route")
			if f.failReplace {
				return errors.New("injected netlink failure")
			}
			for i, old := range f.routes {
				if leaseRouteKey(old) == leaseRouteKey(*r) {
					f.routes[i] = *r
					return nil
				}
			}
			f.routes = append(f.routes, *r)
			return nil
		},
		deleteRoute: func(r *netlink.Route) error {
			f.changes = append(f.changes, "delete-route")
			for i, old := range f.routes {
				if leaseRouteKey(old) == leaseRouteKey(*r) {
					f.routes = append(f.routes[:i:i], f.routes[i+1:]...)
					break
				}
			}
			return nil
		},
	}
}

func testLease() Lease {
	return Lease{Action: "renew", Interface: "iptv", Address: "192.0.2.2", Mask: "24", StaticRoutes: []string{"213.75.112.0/21", "192.0.2.1"}}
}

func TestInvalidLeaseDoesNotMutateNetwork(t *testing.T) {
	t.Parallel()
	for _, change := range []func(*Lease){
		func(l *Lease) { l.StaticRoutes = append(l.StaticRoutes, "bad") },
		func(l *Lease) { l.StaticRoutes = append(l.StaticRoutes, "invalid", "192.0.2.1") },
		func(l *Lease) { l.StaticRoutes[1] = "invalid" },
		func(l *Lease) { l.StaticRoutes[0] = "2001:db8::/32" },
		func(l *Lease) { l.StaticRoutes[1] = "2001:db8::1" },
		func(l *Lease) { l.Address = "2001:db8::1" },
		func(l *Lease) { l.Broadcast = "invalid" },
		func(l *Lease) { l.Mask = "255.0.255.0" },
		func(l *Lease) { l.Metric = -1 },
	} {
		lease := testLease()
		change(&lease)
		fixture := &leaseFixture{}
		if err := applyLease(lease, true, fixture.ops()); err == nil {
			t.Errorf("invalid lease accepted: %+v", lease)
		}
		if len(fixture.changes) != 0 {
			t.Fatalf("invalid lease changed network: %v", fixture.changes)
		}
	}
}

func TestUnchangedRenewalDoesNotReplaceOrDeleteRoutes(t *testing.T) {
	t.Parallel()
	fixture := &leaseFixture{}
	lease := testLease()
	if err := applyLease(lease, true, fixture.ops()); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		fixture.changes = nil
		if err := applyLease(lease, true, fixture.ops()); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(fixture.changes, []string{"address", "up"}) {
			t.Fatalf("renew changed routes: %v", fixture.changes)
		}
		if len(fixture.routes) != 1 {
			t.Fatalf("duplicate routes: %v", fixture.routes)
		}
	}
}

func TestChangedLeaseAppliesBeforeRemovingOldState(t *testing.T) {
	t.Parallel()
	for _, fail := range []bool{false, true} {
		fixture := &leaseFixture{}
		lease := testLease()
		if err := applyLease(lease, true, fixture.ops()); err != nil {
			t.Fatal(err)
		}
		old := fixture.routes[0]
		unrelated := old
		unrelated.Protocol = unix.RTPROT_STATIC
		unrelated.Priority++
		fixture.routes = append(fixture.routes, unrelated)
		fixture.changes, fixture.failReplace = nil, fail
		lease.Address, lease.StaticRoutes[0] = "192.0.2.3", "198.51.100.0/24"
		err := applyLease(lease, true, fixture.ops())
		if fail {
			if err == nil {
				t.Fatal("replacement error ignored")
			}
			if !reflect.DeepEqual(fixture.routes, []netlink.Route{old, unrelated}) {
				t.Fatal("failed replacement removed old routes")
			}
			if len(fixture.addresses) != 2 {
				t.Fatal("failed replacement removed old address")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"address", "up", "replace-route", "delete-route", "delete-address", "address"}
		if !reflect.DeepEqual(fixture.changes, want) {
			t.Fatalf("order: %v", fixture.changes)
		}
		if len(fixture.addresses) != 1 || fixture.addresses[0].IP.String() != lease.Address {
			t.Fatal("stale address retained")
		}
		if len(fixture.routes) != 2 || fixture.routes[0].Protocol != unix.RTPROT_STATIC {
			t.Fatal("unrelated static route removed")
		}
	}
}

func TestLeaseRoutePolicies(t *testing.T) {
	t.Parallel()
	lease := testLease()
	lease.Routers = []string{"192.0.2.254"}
	routes, err := leaseRoutes(lease, 52, 32, true)
	if err != nil || len(routes) != 2 {
		t.Fatalf("host lease routes: %v, %v", routes, err)
	}
	if routes[0].Dst.String() != "192.0.2.1/32" || routes[1].Priority != 252 {
		t.Fatalf("host gateway route missing: %v", routes)
	}
	lease.StaticRoutes = nil
	routes, err = leaseRoutes(lease, 52, 24, false)
	if err != nil || len(routes) != 0 {
		t.Fatalf("unwanted router fallback: %v, %v", routes, err)
	}
	routes, err = leaseRoutes(lease, 52, 24, true)
	if err != nil || len(routes) != 1 || routes[0].Dst.String() != "0.0.0.0/0" {
		t.Fatalf("missing permitted fallback: %v, %v", routes, err)
	}
	lease.StaticRoutes = []string{"0.0.0.0/0", "192.0.2.1"}
	routes, err = leaseRoutes(lease, 52, 24, false)
	if err != nil || len(routes) != 1 || routes[0].Gw.String() != "192.0.2.1" {
		t.Fatalf("explicit RFC3442 default lost: %v, %v", routes, err)
	}
}

func TestDeconfigRemovesLeaseState(t *testing.T) {
	t.Parallel()
	fixture := &leaseFixture{}
	lease := testLease()
	if err := applyLease(lease, true, fixture.ops()); err != nil {
		t.Fatal(err)
	}
	lease.Action = "deconfig"
	if err := applyLease(lease, true, fixture.ops()); err != nil {
		t.Fatal(err)
	}
	if len(fixture.routes) != 0 || len(fixture.addresses) != 0 {
		t.Fatal("lease state retained after deconfig")
	}
}

func TestLeaseSurvivesPrimaryAddressCleanup(t *testing.T) {
	t.Parallel()
	fixture := &leaseFixture{dropSecondary: true}
	lease := testLease()
	if err := applyLease(lease, true, fixture.ops()); err != nil {
		t.Fatal(err)
	}
	lease.Address = "192.0.2.3"
	if err := applyLease(lease, true, fixture.ops()); err != nil {
		t.Fatal(err)
	}
	if len(fixture.addresses) != 1 || fixture.addresses[0].IP.String() != lease.Address || len(fixture.routes) != 1 {
		t.Fatalf("new lease lost after primary cleanup: addresses=%v routes=%v", fixture.addresses, fixture.routes)
	}
}

func TestGatewayChangeReplacesRouteWithoutDeletingReplacement(t *testing.T) {
	t.Parallel()
	fixture := &leaseFixture{}
	lease := testLease()
	if err := applyLease(lease, true, fixture.ops()); err != nil {
		t.Fatal(err)
	}
	fixture.changes = nil
	lease.StaticRoutes[1] = "192.0.2.254"
	if err := applyLease(lease, true, fixture.ops()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fixture.changes, []string{"address", "up", "replace-route"}) {
		t.Fatalf("gateway change deleted a route: %v", fixture.changes)
	}
	if len(fixture.routes) != 1 || fixture.routes[0].Gw.String() != "192.0.2.254" {
		t.Fatalf("replacement missing: %v", fixture.routes)
	}
}
