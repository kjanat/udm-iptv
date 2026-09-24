//go:build linux && integration

package network

import (
	"errors"
	"testing"

	"github.com/vishvananda/netlink"

	"github.com/kjanat/udm-iptv/internal/config"
)

func vlanConfig(parent string) config.Config {
	value := config.DefaultKPN()
	value.WAN.Interface = parent
	value.WAN.VLANInterface = "iptv-test"

	return value
}

func linkExists(t *testing.T, name string) bool {
	t.Helper()
	_, err := netlink.LinkByName(name)
	if err == nil {
		return true
	}
	if _, ok := errors.AsType[netlink.LinkNotFoundError](err); ok {
		return false
	}
	t.Fatal(err)

	return false
}

func mustRemoveLink(t *testing.T, value config.Config) {
	t.Helper()
	if err := RemoveLink(value); err != nil {
		t.Fatal(err)
	}
}

func addForeignVLAN(t *testing.T, value config.Config) {
	t.Helper()
	parent, err := netlink.LinkByName(value.WAN.Interface)
	if err != nil {
		t.Fatal(err)
	}
	attributes := netlink.NewLinkAttrs()
	attributes.Name = value.WAN.VLANInterface
	attributes.ParentIndex = parent.Attrs().Index
	if err := netlink.LinkAdd(&netlink.Vlan{LinkAttrs: attributes, VlanId: value.WAN.VLAN}); err != nil {
		t.Fatal(err)
	}
}

// CI runs this test with privileges; all changes live in a private network
// namespace, never in the runner's or router's network namespace.
func TestRemoveLinkDeletesOnlyTheLinkItCreated(t *testing.T) {
	enterPrivateNamespace(t)
	if err := netlink.LinkAdd(&netlink.Dummy{Name: "wan-test"}); err != nil {
		t.Fatal(err)
	}
	value := vlanConfig("wan-test")
	if _, err := EnsureLink(value); err != nil {
		t.Fatal(err)
	}
	mustRemoveLink(t, value)
	if linkExists(t, value.WAN.VLANInterface) {
		t.Fatal("the created VLAN survived RemoveLink")
	}
	mustRemoveLink(t, value)

	addForeignVLAN(t, value)
	mustRemoveLink(t, value)
	if !linkExists(t, value.WAN.VLANInterface) {
		t.Fatal("a VLAN this program did not create was deleted")
	}

	value.WAN.VLAN = 0
	mustRemoveLink(t, value)
	if !linkExists(t, value.WAN.VLANInterface) {
		t.Fatal("an untagged configuration deleted a link")
	}
}

func TestResolvedStaticAddressInKernel(t *testing.T) {
	enterPrivateNamespace(t)
	if err := netlink.LinkAdd(&netlink.Dummy{Name: "static-test"}); err != nil {
		t.Fatal(err)
	}
	link, err := netlink.LinkByName("static-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatal(err)
	}
	value := config.Config{WAN: config.WAN{StaticAddress: "10.20.30.1/24"}}
	addressing, err := value.WAN.Addressing()
	if err != nil {
		t.Fatal(err)
	}
	// Startup and restoration use the same resolved host address. Applying it
	// repeatedly must neither mask the host bits nor duplicate the address.
	for range 2 {
		if err := ApplyStatic(value, addressing, link); err != nil {
			t.Fatal(err)
		}
		assertStaticHost(t, link)
	}
	// Existing addressing preserves that address while adding configured routes.
	value.WAN.StaticAddress = ""
	value.WAN.StaticRoutes = []string{"198.51.100.0/24"}
	if err := ApplyStatic(value, config.Addressing{}, link); err != nil {
		t.Fatal(err)
	}
	assertStaticHost(t, link)
	assertStaticRoute(t, link, "198.51.100.0/24")
}

func assertStaticRoute(t *testing.T, link netlink.Link, destination string) {
	t.Helper()
	routes, err := netlink.RouteList(link, netlink.FAMILY_V4)
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range routes {
		if route.Dst != nil && route.Dst.String() == destination {
			return
		}
	}
	t.Fatalf("missing static route %s: %v", destination, routes)
}

func assertStaticHost(t *testing.T, link netlink.Link) {
	t.Helper()
	addresses, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		t.Fatal(err)
	}
	if len(addresses) != 1 || addresses[0].IPNet.String() != "10.20.30.1/24" {
		t.Fatalf("static host address changed: %v", addresses)
	}
}
