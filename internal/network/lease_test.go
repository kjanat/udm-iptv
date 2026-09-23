package network

import (
	"errors"
	"fmt"
	"net"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/kjanat/udm-iptv/internal/config"
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
			err := applyLease(testLease(), ops)
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

func ownedLink() netlink.Link {
	return &netlink.Dummy{Index: 52, Alias: linkAlias}
}

func borrowedLink() netlink.Link {
	return &netlink.Dummy{Index: 52, Name: "eth8"}
}

func (f *leaseFixture) ops() leaseOperations {
	return leaseOperations{
		link:           func(string) (netlink.Link, error) { return ownedLink(), nil },
		addresses:      func(netlink.Link, int) ([]netlink.Addr, error) { return f.addresses, nil },
		replaceAddress: f.replaceAddress,
		deleteAddress:  f.deleteAddress,
		up:             f.up,
		routes:         f.listRoutes,
		replaceRoute:   f.replaceRoute,
		addRoute:       f.addRoute,
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
		err := applyLease(lease, fixture.ops())
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
	err := applyLease(lease, fixture.ops())
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		fixture.changes = nil
		err := applyLease(lease, fixture.ops())
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
		if err := applyLease(lease, fixture.ops()); err != nil {
			t.Fatal(err)
		}
		old := fixture.routes[0]
		unrelated := old
		unrelated.Protocol = unix.RTPROT_STATIC
		unrelated.Priority++
		fixture.routes = append(fixture.routes, unrelated)
		fixture.changes, fixture.failReplace = nil, fail
		lease.Address, lease.StaticRoutes[0] = "192.0.2.3", "198.51.100.0/24"
		err := applyLease(lease, fixture.ops())
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

type routePolicyCase struct {
	name         string
	staticRoutes []string
	prefixLength int
	policy       config.RoutePolicy
	want         []string
}

func routePolicyCases() []routePolicyCase {
	return []routePolicyCase{
		{
			name:         "host lease keeps the gateway reachable",
			staticRoutes: []string{"213.75.112.0/21", "192.0.2.1"},
			prefixLength: 32,
			policy:       config.RoutesAllowDefault,
			want:         []string{"192.0.2.1/32 on-link metric 252", "213.75.112.0/21 via 192.0.2.1 metric 252"},
		},
		{name: "router option stays off when no default is permitted", prefixLength: 24, policy: config.RoutesNoDefault},
		{
			name:         "router option when a default is permitted",
			prefixLength: 24,
			policy:       config.RoutesAllowDefault,
			want:         []string{"0.0.0.0/0 via 192.0.2.254 metric 252"},
		},
		{
			name:         "an RFC3442 default obeys the policy like the router option",
			staticRoutes: []string{"0.0.0.0/0", "192.0.2.1"},
			prefixLength: 24,
			policy:       config.RoutesNoDefault,
		},
		{
			name:         "an RFC3442 default installs when a default is permitted",
			staticRoutes: []string{"0.0.0.0/0", "192.0.2.1"},
			prefixLength: 24,
			policy:       config.RoutesAllowDefault,
			want:         []string{"0.0.0.0/0 via 192.0.2.1 metric 252"},
		},
		{
			name:         "suppressing the default keeps the specific RFC3442 routes",
			staticRoutes: []string{"0.0.0.0/0", "192.0.2.1", "213.75.112.0/21", "192.0.2.1"},
			prefixLength: 24,
			policy:       config.RoutesNoDefault,
			want:         []string{"213.75.112.0/21 via 192.0.2.1 metric 253"},
		},
		{
			name:         "a migrated NO_GATEWAY opt-out installs no RFC3442 route",
			staticRoutes: []string{"0.0.0.0/0", "192.0.2.1", "213.75.112.0/21", "192.0.2.1"},
			prefixLength: 24,
			policy:       config.RoutesNone,
		},
		{
			name:         "a migrated NO_GATEWAY opt-out installs no router option",
			prefixLength: 24,
			policy:       config.RoutesNone,
		},
	}
}

func TestLeaseRoutePolicies(t *testing.T) {
	t.Parallel()
	for _, test := range routePolicyCases() {
		lease := testLease()
		lease.Routers = []string{"192.0.2.254"}
		lease.StaticRoutes = test.staticRoutes
		routes, err := leaseRoutes(lease, 52, test.prefixLength, test.policy)
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
	err := applyLease(lease, fixture.ops())
	if err != nil {
		t.Fatal(err)
	}
	lease.Action = "deconfig"
	err = applyLease(lease, fixture.ops())
	if err != nil {
		t.Fatal(err)
	}
	if len(fixture.routes) != 0 || len(fixture.addresses) != 0 {
		t.Fatal("lease state retained after deconfig")
	}
}

func uplinkAddress() netlink.Addr {
	return netlink.Addr{IPNet: &net.IPNet{IP: net.ParseIP("203.0.113.10").To4(), Mask: net.CIDRMask(24, 32)}}
}

func borrowedFixture() (*leaseFixture, leaseOperations) {
	fixture := &leaseFixture{addresses: []netlink.Addr{uplinkAddress()}}
	ops := fixture.ops()
	ops.link = func(string) (netlink.Link, error) { return borrowedLink(), nil }

	return fixture, ops
}

// A lease on an interface the firmware owns, such as an untagged uplink, must
// leave the addresses the firmware put there alone.
func TestBorrowedInterfaceKeepsForeignAddresses(t *testing.T) {
	t.Parallel()
	fixture, ops := borrowedFixture()
	lease := testLease()
	if err := applyLeaseChange(&lease, Lease{}, config.RoutesAllowDefault, ops); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(fixture.changes, "address") || slices.Contains(fixture.changes, "delete-address") {
		t.Fatalf("borrowed interface changes: %v", fixture.changes)
	}
	if len(fixture.addresses) != 2 {
		t.Fatalf("uplink address lost: %v", fixture.addresses)
	}
	deconfig := Lease{Action: "deconfig", Interface: lease.Interface}
	if err := applyLeaseChange(&deconfig, lease, config.RoutesAllowDefault, ops); err != nil {
		t.Fatal(err)
	}
	if len(fixture.addresses) != 1 || !sameAddress(fixture.addresses[0], uplinkAddress()) {
		t.Fatalf("deconfig on a borrowed interface left %v", fixture.addresses)
	}
	fixture.changes = nil
	if err := applyLease(deconfig, ops); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(fixture.changes, "delete-address") {
		t.Fatalf("deconfig without a known address deleted one: %v", fixture.changes)
	}
}

// When a lease moves to another address, the previous lease's address is the
// one thing on a borrowed interface this program retires.
func TestBorrowedInterfaceRetiresThePreviousLeaseAddress(t *testing.T) {
	t.Parallel()
	fixture, ops := borrowedFixture()
	lease := testLease()
	if err := applyLeaseChange(&lease, Lease{}, config.RoutesAllowDefault, ops); err != nil {
		t.Fatal(err)
	}
	moved := lease
	moved.Address = "192.0.2.3"
	if err := applyLeaseChange(&moved, lease, config.RoutesAllowDefault, ops); err != nil {
		t.Fatal(err)
	}
	movedAddress, _, err := leaseAddress(moved)
	if err != nil {
		t.Fatal(err)
	}
	if len(fixture.addresses) != 2 || !sameAddress(fixture.addresses[0], uplinkAddress()) || !sameAddress(fixture.addresses[1], *movedAddress) {
		t.Fatalf("a moved lease on a borrowed interface left %v", fixture.addresses)
	}
	fixture.changes = nil
	if err := applyLeaseChange(&moved, moved, config.RoutesAllowDefault, ops); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(fixture.changes, "delete-address") {
		t.Fatalf("an unchanged renewal deleted an address: %v", fixture.changes)
	}
}

func applyLease(lease Lease, ops leaseOperations) error {
	return applyLeaseChange(&lease, Lease{}, config.RoutesAllowDefault, ops)
}

func TestManagedVLANRequiresOwnershipAlias(t *testing.T) {
	t.Parallel()
	marked := &netlink.Vlan{Alias: linkAlias, ParentIndex: 3, VlanId: 6}
	same := &netlink.Vlan{ParentIndex: 2, VlanId: 4}
	foreign := &netlink.Vlan{ParentIndex: 2, VlanId: 6}
	otherParent := &netlink.Vlan{ParentIndex: 9, VlanId: 4}
	for name, test := range map[string]struct {
		vlan *netlink.Vlan
		want bool
	}{
		"marked link on another parent": {marked, true},
		"unmarked configured VLAN":      {same, false},
		"internet VLAN with our name":   {foreign, false},
		"configured ID on another port": {otherParent, false},
	} {
		if got := managedVLAN(test.vlan); got != test.want {
			t.Errorf("%s: managedVLAN = %t, want %t", name, got, test.want)
		}
	}
}

func TestLeaseSurvivesPrimaryAddressCleanup(t *testing.T) {
	t.Parallel()
	fixture := &leaseFixture{dropSecondary: true}
	lease := testLease()
	err := applyLease(lease, fixture.ops())
	if err != nil {
		t.Fatal(err)
	}
	lease.Address = "192.0.2.3"
	err = applyLease(lease, fixture.ops())
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
	err := applyLease(lease, fixture.ops())
	if err != nil {
		t.Fatal(err)
	}
	fixture.changes = nil
	lease.StaticRoutes[1] = "192.0.2.254"
	err = applyLease(lease, fixture.ops())
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

func TestBorrowedLeaseKeepsForeignDHCPRoutes(t *testing.T) {
	fixture, ops := borrowedFixture()
	foreign, err := dhcpRoute(52, "203.0.113.0/24", "203.0.113.1", 252)
	if err != nil {
		t.Fatal(err)
	}
	fixture.routes = []netlink.Route{foreign}
	lease := testLease()
	if err := applyLeaseChange(&lease, Lease{}, config.RoutesAllowDefault, ops); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(fixture.routes, func(route netlink.Route) bool { return leaseRouteIdentity(route) == leaseRouteIdentity(foreign) }) {
		t.Fatal("foreign DHCP route removed during lease application")
	}
	if err := applyLease(Lease{Action: "deconfig", Interface: lease.Interface}, ops); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(fixture.routes, func(route netlink.Route) bool { return leaseRouteIdentity(route) == leaseRouteIdentity(foreign) }) {
		t.Fatal("foreign DHCP route removed during deconfig")
	}
}

func TestBorrowedLeaseRefusesForeignRouteCollision(t *testing.T) {
	for _, gateway := range []string{"192.0.2.1", "192.0.2.254"} {
		t.Run(gateway, func(t *testing.T) {
			fixture, ops := borrowedFixture()
			foreign, err := dhcpRoute(52, "213.75.112.0/21", gateway, 252)
			if err != nil {
				t.Fatal(err)
			}
			fixture.routes = []netlink.Route{foreign}
			if err := applyLease(testLease(), ops); err == nil {
				t.Fatal("foreign route collision accepted")
			}
			if len(fixture.changes) != 0 {
				t.Fatalf("collision mutated network: %v", fixture.changes)
			}
		})
	}
}

func TestResetBorrowedLeaseDoesNotAccessNetwork(t *testing.T) {
	if err := ResetLease(borrowedLink()); err != nil {
		t.Fatal(err)
	}
}

func (f *leaseFixture) addRoute(r *netlink.Route) error {
	for _, old := range f.routes {
		if leaseRouteKey(old) == leaseRouteKey(*r) {
			return unix.EEXIST
		}
	}
	return f.replaceRoute(r)
}

func TestBorrowedLeaseRetiresOnlyRecordedRoutes(t *testing.T) {
	fixture, ops := borrowedFixture()
	lease := testLease()
	// The default was advertised but policy refused it. A firmware route with
	// exactly those advertised attributes must not be treated as ours later.
	lease.StaticRoutes = []string{"213.75.112.0/21", "192.0.2.1", "0.0.0.0/0", "192.0.2.1"}
	foreign, err := dhcpRoute(52, "0.0.0.0/0", "192.0.2.1", 253)
	if err != nil {
		t.Fatal(err)
	}
	fixture.routes = []netlink.Route{foreign}
	if err := applyLeaseChange(&lease, Lease{}, config.RoutesNoDefault, ops); err != nil {
		t.Fatal(err)
	}
	if len(lease.ManagedRoutes) != 1 {
		t.Fatalf("uninstalled routes claimed: %v", lease.ManagedRoutes)
	}
	deconfig := Lease{Action: "deconfig", Interface: lease.Interface}
	if err := applyLeaseChange(&deconfig, lease, config.RoutesNone, ops); err != nil {
		t.Fatal(err)
	}
	if len(fixture.routes) != 1 || !reflect.DeepEqual(fixture.routes[0], foreign) {
		t.Fatalf("foreign route changed: %v", fixture.routes)
	}
}

func TestBorrowedLeasePreservesForeignReplacement(t *testing.T) {
	for _, field := range []string{"gateway", "source", "mtu"} {
		t.Run(field, func(t *testing.T) {
			checkForeignReplacement(t, field)
		})
	}
}

func checkForeignReplacement(t *testing.T, field string) {
	t.Helper()
	fixture, ops := borrowedFixture()
	lease := testLease()
	if err := applyLeaseChange(&lease, Lease{}, config.RoutesAllowDefault, ops); err != nil {
		t.Fatal(err)
	}
	switch field {
	case "gateway":
		fixture.routes[0].Gw = net.ParseIP("192.0.2.254").To4()
	case "source":
		fixture.routes[0].Src = net.ParseIP("192.0.2.99").To4()
	case "mtu":
		fixture.routes[0].MTU = 1400
	}
	replaced := fixture.routes[0]
	fixture.changes = nil
	renewed := testLease()
	if err := applyLeaseChange(&renewed, lease, config.RoutesAllowDefault, ops); !errors.Is(err, errForeignRoute) {
		t.Fatalf("foreign replacement accepted: %v", err)
	}
	if len(fixture.changes) != 0 {
		t.Fatalf("foreign replacement mutated: %v", fixture.changes)
	}
	deconfig := Lease{Action: "deconfig", Interface: lease.Interface}
	if err := applyLeaseChange(&deconfig, lease, config.RoutesAllowDefault, ops); err != nil {
		t.Fatal(err)
	}
	if len(fixture.routes) != 1 || !reflect.DeepEqual(fixture.routes[0], replaced) {
		t.Fatalf("foreign replacement deleted: %v", fixture.routes)
	}
}

func TestBorrowedLeaseRetainsOwnershipAfterPartialFailure(t *testing.T) {
	fixture, ops := borrowedFixture()
	lease := testLease()
	if err := applyLeaseChange(&lease, Lease{}, config.RoutesAllowDefault, ops); err != nil {
		t.Fatal(err)
	}
	changed := testLease()
	changed.StaticRoutes = []string{"198.51.100.0/24", "192.0.2.1", "203.0.113.0/24", "192.0.2.1"}
	calls := 0
	ops.addRoute = func(route *netlink.Route) error {
		calls++
		if calls == 2 {
			return errInjectedFailure
		}
		return fixture.addRoute(route)
	}
	if err := applyLeaseChange(&changed, lease, config.RoutesAllowDefault, ops); !errors.Is(err, errInjectedFailure) {
		t.Fatalf("missing failure: %v", err)
	}
	if len(changed.ManagedRoutes) != 2 {
		t.Fatalf("partial ownership lost: %v", changed.ManagedRoutes)
	}
	deconfig := Lease{Action: "deconfig", Interface: changed.Interface}
	if err := applyLeaseChange(&deconfig, changed, config.RoutesAllowDefault, ops); err != nil {
		t.Fatal(err)
	}
	if len(fixture.routes) != 0 {
		t.Fatalf("partial routes leaked: %v", fixture.routes)
	}
}

func TestBorrowedFailedLeaseCannotClaimForeignAddress(t *testing.T) {
	fixture, ops := borrowedFixture()
	previous := testLease()
	if err := applyLeaseChange(&previous, Lease{}, config.RoutesAllowDefault, ops); err != nil {
		t.Fatal(err)
	}
	proposal := testLease()
	proposal.Address = uplinkAddress().IP.String()
	if err := applyLeaseChange(&proposal, previous, config.RoutesAllowDefault, ops); !errors.Is(err, errForeignAddress) {
		t.Fatalf("foreign address accepted: %v", err)
	}
	// The failed proposal is the hook's next persisted record. Deconfig may
	// retire the old applied address, never the address merely proposed.
	deconfig := Lease{Action: "deconfig", Interface: proposal.Interface}
	if err := applyLeaseChange(&deconfig, proposal, config.RoutesAllowDefault, ops); err != nil {
		t.Fatal(err)
	}
	if len(fixture.addresses) != 1 || !sameAddress(fixture.addresses[0], uplinkAddress()) {
		t.Fatalf("failed lease deleted foreign address: %v", fixture.addresses)
	}
}

func TestBorrowedLeaseExclusiveAddRejectsConcurrentForeignRoute(t *testing.T) {
	fixture, ops := borrowedFixture()
	lease := testLease()
	ops.addRoute = func(route *netlink.Route) error {
		foreign := *route
		foreign.Gw = net.ParseIP("192.0.2.254").To4()
		fixture.routes = append(fixture.routes, foreign)
		return fixture.addRoute(route)
	}
	if err := applyLeaseChange(&lease, Lease{}, config.RoutesAllowDefault, ops); !errors.Is(err, unix.EEXIST) {
		t.Fatalf("concurrent collision accepted: %v", err)
	}
	if len(lease.ManagedRoutes) != 0 || len(fixture.routes) != 1 || fixture.routes[0].Gw.String() != "192.0.2.254" {
		t.Fatalf("concurrent foreign route claimed/replaced: %v / %v", lease.ManagedRoutes, fixture.routes)
	}
}
