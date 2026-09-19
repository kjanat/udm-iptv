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
