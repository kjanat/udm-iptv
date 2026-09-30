package installer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/vishvananda/netlink"

	"github.com/kjanat/udm-iptv/internal/config"
)

const (
	legacyNetworkPending = "legacy-network.pending"
	legacyNetworkAlias   = "udm-iptv"
)

var errLegacyNetworkMismatch = errors.New("legacy IPTV interface does not match the saved v4 configuration; interface left unchanged")

type legacyNetworkLinks struct {
	find     func(string) (netlink.Link, error)
	setAlias func(netlink.Link, string) error
	remove   func(netlink.Link) error
}

func systemLegacyNetworkLinks() legacyNetworkLinks {
	return legacyNetworkLinks{find: netlink.LinkByName, setAlias: netlink.LinkSetAlias, remove: netlink.LinkDel}
}

// legacyHandover is what activation does with the interface the v4 service
// left behind: adopt it when v5 reuses its name, so startup replaces it;
// otherwise remove it with its stale addresses and routes. An interface that
// already carries our alias is ours either way.
type legacyHandover struct {
	link  netlink.Link
	adopt bool
}

// inspectLegacyNetwork validates the provenance the Debian preinstall script
// saved after stopping the v4 service against the live interface. It changes
// nothing. The pending file survives until installation health and cleanup
// succeed.
func inspectLegacyNetwork(plan Plan, links legacyNetworkLinks) (legacyHandover, error) {
	previous, pending, err := pendingLegacyNetwork(plan.StateDir)
	if err != nil || !pending {
		return legacyHandover{}, err
	}
	name := previous.WAN.VLANInterface
	link, err := links.find(name)
	if _, absent := errors.AsType[netlink.LinkNotFoundError](err); absent {
		return legacyHandover{}, nil
	}
	if err != nil {
		return legacyHandover{}, fmt.Errorf("inspect legacy IPTV interface %s: %w", name, err)
	}
	adopt := plan.Config.WAN.VLAN > 0 && plan.Config.WAN.VLANInterface == name
	if link.Attrs().Alias == legacyNetworkAlias {
		if adopt {
			return legacyHandover{}, nil
		}

		return legacyHandover{link: link}, nil
	}
	parent, err := links.find(previous.WAN.Interface)
	if err != nil {
		return legacyHandover{}, fmt.Errorf("inspect legacy WAN interface %s: %w", previous.WAN.Interface, err)
	}
	if !matchesLegacyVLAN(link, parent, previous) {
		return legacyHandover{}, fmt.Errorf("%w: expected %s VLAN %d on %s", errLegacyNetworkMismatch,
			name, previous.WAN.VLAN, previous.WAN.Interface)
	}

	return legacyHandover{link: link, adopt: adopt}, nil
}

// pendingLegacyNetwork reads the v4 provenance awaiting migration. An
// untagged uplink remains borrowed, never adopted.
func pendingLegacyNetwork(stateDir string) (config.Config, bool, error) {
	previous, err := config.ImportLegacy(filepath.Join(stateDir, legacyNetworkPending))
	if errors.Is(err, os.ErrNotExist) {
		return config.Config{}, false, nil
	}
	if err != nil {
		return config.Config{}, false, fmt.Errorf("read pending v4 network migration: %w", err)
	}

	return previous, previous.WAN.VLAN != 0, nil
}

// migrateLegacyNetwork performs the handover inspectLegacyNetwork decided.
func migrateLegacyNetwork(plan Plan, links legacyNetworkLinks) error {
	handover, err := inspectLegacyNetwork(plan, links)
	if err != nil || handover.link == nil {
		return err
	}
	name := handover.link.Attrs().Name
	if handover.adopt {
		if err := links.setAlias(handover.link, legacyNetworkAlias); err != nil {
			return fmt.Errorf("hand over legacy IPTV interface %s: %w", name, err)
		}

		return nil
	}
	if err := links.remove(handover.link); err != nil {
		return fmt.Errorf("remove legacy IPTV interface %s: %w", name, err)
	}

	return nil
}

func matchesLegacyVLAN(link, parent netlink.Link, previous config.Config) bool {
	vlan, ok := link.(*netlink.Vlan)
	return ok && vlan.VlanId == previous.WAN.VLAN &&
		vlan.ParentIndex == parent.Attrs().Index &&
		vlan.Alias == ""
}
