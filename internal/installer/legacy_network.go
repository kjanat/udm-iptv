package installer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/vishvananda/netlink"

	"github.com/kjanat/udm-iptv/internal/config"
)

const legacyNetworkPending = "legacy-network.pending"

var errLegacyNetworkMismatch = errors.New("legacy IPTV interface does not match the saved v4 configuration and requested v5 configuration; interface left unchanged")

type legacyNetworkLinks struct {
	find     func(string) (netlink.Link, error)
	setAlias func(netlink.Link, string) error
}

// migrateLegacyNetwork consumes the provenance saved by the Debian preinstall
// script after successfully stopping the installed v4 service. A matching interface can be
// handed to v5, which replaces it and its old addresses/routes during startup.
// The pending file survives until installation health and cleanup succeed.
func migrateLegacyNetwork(plan Plan, links legacyNetworkLinks) error {
	previous, err := config.ImportLegacy(filepath.Join(plan.StateDir, legacyNetworkPending))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read pending v4 network migration: %w", err)
	}
	if previous.WAN.VLAN == 0 {
		return nil // An untagged uplink remains borrowed, never adopted.
	}
	if previous.WAN.Interface != plan.Config.WAN.Interface ||
		previous.WAN.VLAN != plan.Config.WAN.VLAN ||
		previous.WAN.VLANInterface != plan.Config.WAN.VLANInterface {
		return fmt.Errorf("%w: v4 %s VLAN %d on %s; v5 %s VLAN %d on %s", errLegacyNetworkMismatch,
			previous.WAN.VLANInterface, previous.WAN.VLAN, previous.WAN.Interface,
			plan.Config.WAN.VLANInterface, plan.Config.WAN.VLAN, plan.Config.WAN.Interface)
	}
	return handOverLegacyVLAN(previous, links)
}

func handOverLegacyVLAN(previous config.Config, links legacyNetworkLinks) error {
	name := previous.WAN.VLANInterface
	link, err := links.find(name)
	if _, absent := errors.AsType[netlink.LinkNotFoundError](err); absent {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect legacy IPTV interface %s: %w", name, err)
	}
	parent, err := links.find(previous.WAN.Interface)
	if err != nil {
		return fmt.Errorf("inspect legacy WAN interface %s: %w", previous.WAN.Interface, err)
	}
	if !matchesLegacyVLAN(link, parent, previous) {
		return fmt.Errorf("%w: expected %s VLAN %d on %s", errLegacyNetworkMismatch,
			name, previous.WAN.VLAN, previous.WAN.Interface)
	}
	if link.Attrs().Alias == "udm-iptv" {
		return nil // A previous activation attempt already handed this over.
	}
	if err := links.setAlias(link, "udm-iptv"); err != nil {
		return fmt.Errorf("hand over legacy IPTV interface %s: %w", name, err)
	}
	return nil
}

func matchesLegacyVLAN(link, parent netlink.Link, previous config.Config) bool {
	vlan, ok := link.(*netlink.Vlan)
	return ok && vlan.VlanId == previous.WAN.VLAN &&
		vlan.ParentIndex == parent.Attrs().Index &&
		(vlan.Alias == "" || vlan.Alias == "udm-iptv")
}
