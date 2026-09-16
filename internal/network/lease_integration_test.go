//go:build linux && integration

package network

import (
	"fmt"
	"os"
	"runtime"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// CI runs this test with privileges; all changes live in a private network
// namespace, never in the runner's or router's network namespace.
func TestKernelLeaseRenewal(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	original, err := os.Open(fmt.Sprintf("/proc/self/task/%d/ns/net", unix.Gettid()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		err := original.Close()
		if err != nil {
			t.Error(err)
		}
	}()
	if err := unix.Unshare(unix.CLONE_NEWNET); err != nil {
		t.Fatal(err)
	}
	defer func() {
		err := unix.Setns(int(original.Fd()), unix.CLONE_NEWNET)
		if err != nil {
			// Do not let Go reuse a thread still attached to the test namespace.
			panic(err)
		}
	}()
	if err := netlink.LinkAdd(&netlink.Dummy{Name: "iptv-test"}); err != nil {
		t.Fatal(err)
	}
	link, err := netlink.LinkByName("iptv-test")
	if err != nil {
		t.Fatal(err)
	}
	lease := testLease()
	lease.Interface = link.Attrs().Name
	lease.Action = "bound"
	if err := ApplyLease(lease, true); err != nil {
		t.Fatal(err)
	}
	check := func(wantRoutes int) {
		t.Helper()
		addresses, err := netlink.AddrList(link, netlink.FAMILY_V4)
		if err != nil {
			t.Fatal(err)
		}
		if len(addresses) != 1 || addresses[0].IP.String() != lease.Address {
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
	check(1)
	lease.Action = "renew"
	for range 3 {
		err := ApplyLease(lease, true)
		if err != nil {
			t.Fatal(err)
		}
		check(1)
	}
	invalid := lease
	invalid.StaticRoutes = []string{"198.51.100.0/24", "192.0.2.1", "invalid"}
	if err := ApplyLease(invalid, true); err == nil {
		t.Fatal("invalid option accepted")
	}
	check(1)
	// Same subnet, different host: Linux may remove a secondary address when
	// the old primary address is deleted. The new address must survive cleanup.
	lease.Address = "192.0.2.3"
	if err := ApplyLease(lease, true); err != nil {
		t.Fatal(err)
	}
	check(1)
	lease.StaticRoutes = []string{"198.51.100.0/24", "192.0.2.1"}
	if err := ApplyLease(lease, true); err != nil {
		t.Fatal(err)
	}
	check(1)
	lease.Address, lease.Mask = "198.51.100.2", "32"
	if err := ApplyLease(lease, true); err != nil {
		t.Fatal(err)
	}
	check(2)
	lease.Action = "deconfig"
	if err := ApplyLease(lease, true); err != nil {
		t.Fatal(err)
	}
	addresses, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil || len(addresses) != 0 {
		t.Fatalf("deconfig addresses: %v, %v", addresses, err)
	}
	routes, err := netlink.RouteList(link, netlink.FAMILY_V4)
	if err != nil || len(routes) != 0 {
		t.Fatalf("deconfig routes: %v, %v", routes, err)
	}
}
