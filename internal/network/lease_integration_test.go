//go:build linux && integration

package network

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/kjanat/udm-iptv/internal/config"
)

// CI runs this test with privileges; all changes live in a private network
// namespace, never in the runner's or router's network namespace.
func TestKernelLeaseRenewal(t *testing.T) {
	enterPrivateNamespace(t)
	if err := netlink.LinkAdd(&netlink.Dummy{Name: "iptv-test"}); err != nil {
		t.Fatal(err)
	}
	link, err := netlink.LinkByName("iptv-test")
	if err != nil {
		t.Fatal(err)
	}
	// The dummy carries no alias, so this is the borrowed-link path: the hook
	// hands over the previous lease and only its address may be retired.
	lease := testLease()
	lease.Interface = link.Attrs().Name
	lease.Action = "bound"
	previous := Lease{}
	apply := func() {
		t.Helper()
		mustApplyLease(t, &lease, previous)
		previous = lease
	}
	apply()
	checkLease(t, link, lease.Address, 1)
	lease.Action = "renew"
	for range 3 {
		apply()
		checkLease(t, link, lease.Address, 1)
	}
	invalid := lease
	invalid.StaticRoutes = []string{"198.51.100.0/24", "192.0.2.1", "invalid"}
	if err := ApplyLease(&invalid, previous, config.RoutesAllowDefault); err == nil {
		t.Fatal("invalid option accepted")
	}
	checkLease(t, link, lease.Address, 1)
	// Same subnet, different host: Linux may remove a secondary address when
	// the old primary address is deleted. The new address must survive cleanup.
	lease.Address = "192.0.2.3"
	apply()
	checkLease(t, link, lease.Address, 1)
	lease.StaticRoutes = []string{"198.51.100.0/24", "192.0.2.1"}
	apply()
	checkLease(t, link, lease.Address, 1)
	lease.Address, lease.Mask = "198.51.100.2", "32"
	apply()
	checkLease(t, link, lease.Address, 2)
	lease.Action = "deconfig"
	apply()
	checkCleared(t, link)
}

func checkCleared(t *testing.T, link netlink.Link) {
	t.Helper()
	addresses, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil || len(addresses) != 0 {
		t.Fatalf("deconfig addresses: %v, %v", addresses, err)
	}
	routes, err := netlink.RouteList(link, netlink.FAMILY_V4)
	if err != nil || len(routes) != 0 {
		t.Fatalf("deconfig routes: %v, %v", routes, err)
	}
}

func enterPrivateNamespace(t *testing.T) {
	t.Helper()
	runtime.LockOSThread()
	t.Cleanup(runtime.UnlockOSThread)
	original, err := os.Open(fmt.Sprintf("/proc/self/task/%d/ns/net", unix.Gettid()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		err := original.Close()
		if err != nil {
			t.Error(err)
		}
	})
	if err := unix.Unshare(unix.CLONE_NEWNET); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		err := unix.Setns(int(original.Fd()), unix.CLONE_NEWNET)
		if err != nil {
			// Do not let Go reuse a thread still attached to the test namespace.
			panic(err)
		}
	})
}

func mustApplyLease(t *testing.T, lease *Lease, previous Lease) {
	t.Helper()
	if err := ApplyLease(lease, previous, config.RoutesAllowDefault); err != nil {
		t.Fatal(err)
	}
}

func checkLease(t *testing.T, link netlink.Link, address string, wantRoutes int) {
	t.Helper()
	addresses, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		t.Fatal(err)
	}
	if len(addresses) != 1 || addresses[0].IP.String() != address {
		t.Fatalf("unexpected lease addresses: %v", addresses)
	}
	routes, err := netlink.RouteListFiltered(netlink.FAMILY_V4, &netlink.Route{LinkIndex: link.Attrs().Index, Protocol: routeProtocolDHCP}, netlink.RT_FILTER_OIF|netlink.RT_FILTER_PROTOCOL)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != wantRoutes {
		t.Fatalf("unexpected DHCP routes: %v", routes)
	}
}

func TestKernelBorrowedLeasePreservesForeignDHCPRoute(t *testing.T) {
	enterPrivateNamespace(t)
	link := kernelDummy(t, "iptv-test")
	firmwareAddress := uplinkAddress()
	if err := netlink.AddrAdd(link, &firmwareAddress); err != nil {
		t.Fatal(err)
	}
	foreign, err := dhcpRoute(link.Attrs().Index, "203.0.113.0/24", "0.0.0.0", 700)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.RouteAdd(&foreign); err != nil {
		t.Fatal(err)
	}
	if err := ResetLease(link); err != nil {
		t.Fatal(err)
	}
	lease := testLease()
	lease.Interface = link.Attrs().Name
	mustApplyLease(t, &lease, Lease{})
	deconfig := Lease{Action: "deconfig", Interface: lease.Interface}
	mustApplyLease(t, &deconfig, lease)
	routes, err := netlink.RouteListFiltered(netlink.FAMILY_V4, &netlink.Route{LinkIndex: link.Attrs().Index, Protocol: routeProtocolDHCP}, netlink.RT_FILTER_OIF|netlink.RT_FILTER_PROTOCOL)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || !trackedRoute(routes[0], []netlink.Route{foreign}) {
		t.Fatalf("foreign DHCP route changed: %v", routes)
	}
}

func TestKernelVLANRequiresOwnership(t *testing.T) {
	enterPrivateNamespace(t)
	parent := kernelDummy(t, "parent-test")
	value := config.DefaultKPN()
	value.WAN.Interface = "parent-test"
	value.WAN.VLANInterface = "iptv-test"
	value.WAN.VLAN = 4
	foreign := &netlink.Vlan{Name: "iptv-test", ParentIndex: parent.Attrs().Index, VlanId: 4}
	if err := netlink.LinkAdd(foreign); err != nil {
		t.Fatal(err)
	}
	before := kernelLink(t, "iptv-test")
	if _, err := ensureVLAN(value, parent); !errors.Is(err, errForeignVLAN) {
		t.Fatalf("foreign matching VLAN accepted: %v", err)
	}
	after := kernelLink(t, "iptv-test")
	if after.Attrs().Index != before.Attrs().Index || after.Attrs().Alias != "" {
		t.Fatalf("foreign VLAN mutated: %v", after)
	}
	if err := netlink.LinkSetAlias(after, linkAlias); err != nil {
		t.Fatal(err)
	}
	replaced, err := ensureVLAN(value, parent)
	if err != nil {
		t.Fatal(err)
	}
	if replaced.Attrs().Index == before.Attrs().Index {
		t.Fatal("marked VLAN not replaced")
	}
	persisted, err := netlink.LinkByName("iptv-test")
	if err != nil || !owned(persisted) {
		t.Fatalf("replacement ownership missing: %v, %v", persisted, err)
	}
}

func TestKernelBorrowedFinalAddressPreservesForeignRoute(t *testing.T) {
	enterPrivateNamespace(t)
	link := kernelDummy(t, "iptv-test")
	lease := testLease()
	lease.Interface = link.Attrs().Name
	mustApplyLease(t, &lease, Lease{})
	foreign, err := dhcpRoute(link.Attrs().Index, "203.0.113.0/24", "0.0.0.0", 700)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.RouteAdd(&foreign); err != nil {
		t.Fatal(err)
	}
	deconfig := Lease{Action: "deconfig", Interface: lease.Interface}
	if err := ApplyLease(&deconfig, lease, config.RoutesAllowDefault); !errors.Is(err, errBorrowedAddressInUse) {
		t.Fatalf("unsafe final address cleanup accepted: %v", err)
	}
	routes, err := netlink.RouteListFiltered(netlink.FAMILY_V4, &netlink.Route{LinkIndex: link.Attrs().Index, Protocol: routeProtocolDHCP}, netlink.RT_FILTER_OIF|netlink.RT_FILTER_PROTOCOL)
	if err != nil || len(routes) != 1 || !trackedRoute(routes[0], []netlink.Route{foreign}) {
		t.Fatalf("foreign route lost: %v / %v", routes, err)
	}
	if err := netlink.RouteDel(&foreign); err != nil {
		t.Fatal(err)
	}
	mustApplyLease(t, &deconfig, lease)
	checkCleared(t, link)
}

func kernelDummy(t *testing.T, name string) netlink.Link {
	t.Helper()
	if err := netlink.LinkAdd(&netlink.Dummy{Name: name}); err != nil {
		t.Fatal(err)
	}
	link := kernelLink(t, name)
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatal(err)
	}
	return link
}

func kernelLink(t *testing.T, name string) netlink.Link {
	t.Helper()
	link, err := netlink.LinkByName(name)
	if err != nil {
		t.Fatal(err)
	}
	return link
}
