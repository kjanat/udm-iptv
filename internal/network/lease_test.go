package network

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

var (
	errInjectedFailure = errors.New("injected failure")
	errInjectedNetlink = errors.New("injected netlink failure")
)

func TestLeaseFailuresKeepOperationAndCause(t *testing.T) {
	want := errInjectedFailure
	for _, stage := range []string{"find DHCP interface", "apply DHCP address", "bring DHCP interface up", "apply DHCP routes", "retire previous DHCP address"} {
		t.Run(stage, func(t *testing.T) {
			fixture := &leaseFixture{}
			ops := fixture.ops()
			switch stage {
			case "find DHCP interface":
				ops.link = func(string) (netlink.Link, error) { return nil, want }
			case "apply DHCP address":
				ops.replaceAddress = func(netlink.Link, *netlink.Addr) error { return want }
			case "bring DHCP interface up":
				ops.up = func(netlink.Link) error { return want }
			case "apply DHCP routes":
				ops.replaceRoute = func(*netlink.Route) error { return want }
			case "retire previous DHCP address":
				ops.addresses = func(netlink.Link, int) ([]netlink.Addr, error) { return nil, want }
			}
			err := applyLease(testLease(), true, ops)
			if !errors.Is(err, want) || !strings.Contains(err.Error(), stage) {
				t.Fatalf("missing cause or operation: %v", err)
			}
		})
	}
}

type leaseFixture struct {
	routes        []netlink.Route
	addresses     []netlink.Addr
	changes       []string
	failReplace   bool
	dropSecondary bool
}

func (f *leaseFixture) ops() leaseOperations {
	return leaseOperations{
		link:           func(string) (netlink.Link, error) { return &netlink.Dummy{Index: 52}, nil },
		addresses:      func(netlink.Link, int) ([]netlink.Addr, error) { return f.addresses, nil },
		replaceAddress: f.replaceAddress,
		deleteAddress:  f.deleteAddress,
		up:             f.up,
		routes:         f.listRoutes,
		replaceRoute:   f.replaceRoute,
		deleteRoute:    f.deleteRoute,
	}
}

func (f *leaseFixture) replaceAddress(_ netlink.Link, a *netlink.Addr) error {
	f.changes = append(f.changes, "address")
	for _, old := range f.addresses {
		if sameAddress(old, *a) {
			return nil
		}
	}
	f.addresses = append(f.addresses, *a)

	return nil
}

func (f *leaseFixture) deleteAddress(_ netlink.Link, a *netlink.Addr) error {
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
}

func (f *leaseFixture) up(netlink.Link) error {
	f.changes = append(f.changes, "up")

	return nil
}

func (f *leaseFixture) listRoutes(int, *netlink.Route, uint64) ([]netlink.Route, error) {
	return append([]netlink.Route(nil), f.routes...), nil
}

func (f *leaseFixture) replaceRoute(r *netlink.Route) error {
	f.changes = append(f.changes, "replace-route")
	if f.failReplace {
		return errInjectedNetlink
	}
	for i, old := range f.routes {
		if leaseRouteKey(old) == leaseRouteKey(*r) {
			f.routes[i] = *r

			return nil
		}
	}
	f.routes = append(f.routes, *r)

	return nil
}

func (f *leaseFixture) deleteRoute(r *netlink.Route) error {
	f.changes = append(f.changes, "delete-route")
	for i, old := range f.routes {
		if leaseRouteKey(old) == leaseRouteKey(*r) {
			f.routes = append(f.routes[:i:i], f.routes[i+1:]...)

			break
		}
	}

	return nil
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
		err := applyLease(lease, true, fixture.ops())
		if err == nil {
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
	err := applyLease(lease, true, fixture.ops())
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		fixture.changes = nil
		err := applyLease(lease, true, fixture.ops())
		if err != nil {
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
			assertFailedReplacementKeepsOldState(t, fixture, err, []netlink.Route{old, unrelated})

			continue
		}
		assertReplacementRetiredOldState(t, fixture, err, lease.Address)
	}
}

func assertFailedReplacementKeepsOldState(t *testing.T, fixture *leaseFixture, err error, wantRoutes []netlink.Route) {
	t.Helper()
	if err == nil {
		t.Fatal("replacement error ignored")
	}
	if !reflect.DeepEqual(fixture.routes, wantRoutes) {
		t.Fatal("failed replacement removed old routes")
	}
	if len(fixture.addresses) != 2 {
		t.Fatal("failed replacement removed old address")
	}
}

func assertReplacementRetiredOldState(t *testing.T, fixture *leaseFixture, err error, address string) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"address", "up", "replace-route", "delete-route", "delete-address", "address"}
	if !reflect.DeepEqual(fixture.changes, want) {
		t.Fatalf("order: %v", fixture.changes)
	}
	if len(fixture.addresses) != 1 || fixture.addresses[0].IP.String() != address {
		t.Fatal("stale address retained")
	}
	if len(fixture.routes) != 2 || fixture.routes[0].Protocol != unix.RTPROT_STATIC {
		t.Fatal("unrelated static route removed")
	}
}

func TestLeaseRoutePolicies(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name         string
		staticRoutes []string
		prefixLength int
		allowDefault bool
		want         []string
	}{
		{
			name:         "host lease keeps the gateway reachable",
			staticRoutes: []string{"213.75.112.0/21", "192.0.2.1"},
			prefixLength: 32,
			allowDefault: true,
			want:         []string{"192.0.2.1/32 on-link metric 252", "213.75.112.0/21 via 192.0.2.1 metric 252"},
		},
		{name: "router fallback stays off when not permitted", prefixLength: 24},
		{
			name:         "router fallback when permitted",
			prefixLength: 24,
			allowDefault: true,
			want:         []string{"0.0.0.0/0 via 192.0.2.254 metric 252"},
		},
		{
			name:         "explicit RFC3442 default survives the policy",
			staticRoutes: []string{"0.0.0.0/0", "192.0.2.1"},
			prefixLength: 24,
			want:         []string{"0.0.0.0/0 via 192.0.2.1 metric 252"},
		},
	} {
		lease := testLease()
		lease.Routers = []string{"192.0.2.254"}
		lease.StaticRoutes = test.staticRoutes
		routes, err := leaseRoutes(lease, 52, test.prefixLength, test.allowDefault)
		if err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		if got := describeRoutes(routes); !slices.Equal(got, test.want) {
			t.Fatalf("%s: %v", test.name, got)
		}
	}
}

func describeRoutes(routes []netlink.Route) []string {
	described := make([]string, 0, len(routes))
	for _, route := range routes {
		via := "on-link"
		if len(route.Gw) != 0 {
			via = "via " + route.Gw.String()
		}
		described = append(described, fmt.Sprintf("%s %s metric %d", route.Dst, via, route.Priority))
	}

	return described
}

func TestDeconfigRemovesLeaseState(t *testing.T) {
	t.Parallel()
	fixture := &leaseFixture{}
	lease := testLease()
	err := applyLease(lease, true, fixture.ops())
	if err != nil {
		t.Fatal(err)
	}
	lease.Action = "deconfig"
	err = applyLease(lease, true, fixture.ops())
	if err != nil {
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
	err := applyLease(lease, true, fixture.ops())
	if err != nil {
		t.Fatal(err)
	}
	lease.Address = "192.0.2.3"
	err = applyLease(lease, true, fixture.ops())
	if err != nil {
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
	err := applyLease(lease, true, fixture.ops())
	if err != nil {
		t.Fatal(err)
	}
	fixture.changes = nil
	lease.StaticRoutes[1] = "192.0.2.254"
	err = applyLease(lease, true, fixture.ops())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fixture.changes, []string{"address", "up", "replace-route"}) {
		t.Fatalf("gateway change deleted a route: %v", fixture.changes)
	}
	if len(fixture.routes) != 1 || fixture.routes[0].Gw.String() != "192.0.2.254" {
		t.Fatalf("replacement missing: %v", fixture.routes)
	}
}
