package installer

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/config"
)

const legacyNetworkConfig = `IPTV_WAN_INTERFACE=eth8
IPTV_WAN_VLAN=4
IPTV_WAN_VLAN_INTERFACE=iptv
IPTV_LAN_INTERFACES=br0
IPTV_WAN_RANGES=213.75.0.0/16
`

func legacyNetworkPlan(t *testing.T, contents string) Plan {
	t.Helper()
	directory := t.TempDir()
	if err := atomicfile.Write(filepath.Join(directory, legacyNetworkPending), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	value := config.DefaultKPN()
	value.WAN.Interface = "eth8"
	value.WAN.VLANInterface = "iptv"
	return Plan{StateDir: directory, Config: value}
}

func TestLegacyNetworkMigrationRejectsUnrelatedInterfaces(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		change func(*Plan, *netlink.Vlan)
	}{
		{"configured parent changed", func(p *Plan, _ *netlink.Vlan) { p.Config.WAN.Interface = "eth9" }},
		{"configured VLAN changed", func(p *Plan, _ *netlink.Vlan) { p.Config.WAN.VLAN = 5 }},
		{"configured name changed", func(p *Plan, _ *netlink.Vlan) { p.Config.WAN.VLANInterface = "other" }},
		{"observed parent differs", func(_ *Plan, v *netlink.Vlan) { v.ParentIndex = 9 }},
		{"observed VLAN differs", func(_ *Plan, v *netlink.Vlan) { v.VlanId = 5 }},
		{"another owner", func(_ *Plan, v *netlink.Vlan) { v.Alias = "firmware" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			plan := legacyNetworkPlan(t, legacyNetworkConfig)
			vlan := legacyTestVLAN()
			test.change(&plan, vlan)
			links := legacyTestLinks(t, vlan)
			if err := migrateLegacyNetwork(plan, links); !errors.Is(err, errLegacyNetworkMismatch) {
				t.Fatalf("migration error = %v; want mismatch", err)
			}
			assertPendingLegacyNetwork(t, plan)
		})
	}
}

func legacyTestVLAN() *netlink.Vlan {
	return &netlink.Vlan{Name: "iptv", Index: 20, ParentIndex: 8, VlanId: 4}
}

func legacyTestLinks(t *testing.T, link netlink.Link) legacyNetworkLinks {
	t.Helper()
	return legacyNetworkLinks{
		find: func(name string) (netlink.Link, error) {
			if name == "eth8" {
				return &netlink.Dummy{Name: name, Index: 8}, nil
			}
			if name != "iptv" {
				t.Fatalf("unexpected interface lookup %q", name)
			}
			return link, nil
		},
		setAlias: func(netlink.Link, string) error {
			t.Fatal("unexpected interface mutation")
			return nil
		},
	}
}

func assertPendingLegacyNetwork(t *testing.T, plan Plan) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(plan.StateDir, legacyNetworkPending)); err != nil {
		t.Fatalf("pending migration lost before health check: %v", err)
	}
}

func TestLegacyNetworkMigrationHandoverAndRetry(t *testing.T) {
	t.Parallel()
	plan := legacyNetworkPlan(t, legacyNetworkConfig)
	vlan := legacyTestVLAN()
	links := legacyTestLinks(t, vlan)
	mutations := 0
	links.setAlias = func(link netlink.Link, alias string) error {
		if link != vlan || alias != "udm-iptv" {
			t.Fatalf("unexpected handover: %v %q", link, alias)
		}
		vlan.Alias = alias
		mutations++
		return nil
	}
	for range 2 {
		if err := migrateLegacyNetwork(plan, links); err != nil {
			t.Fatal(err)
		}
		assertPendingLegacyNetwork(t, plan)
	}
	if mutations != 1 {
		t.Fatalf("handover performed %d times", mutations)
	}
}

func TestLegacyNetworkMigrationNoPendingOrUntagged(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		contents string
		remove   bool
	}{
		{"no migration", legacyNetworkConfig, true},
		{"untagged uplink", strings.ReplaceAll(legacyNetworkConfig, "IPTV_WAN_VLAN=4", "IPTV_WAN_VLAN=0"), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			plan := legacyNetworkPlan(t, test.contents)
			if test.remove {
				if err := os.Remove(filepath.Join(plan.StateDir, legacyNetworkPending)); err != nil {
					t.Fatal(err)
				}
			}
			if err := migrateLegacyNetwork(plan, legacyNetworkLinks{}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLegacyNetworkMigrationMissingAndNonVLAN(t *testing.T) {
	t.Parallel()
	plan := legacyNetworkPlan(t, legacyNetworkConfig)
	links := legacyNetworkLinks{find: func(string) (netlink.Link, error) {
		return nil, netlink.LinkNotFoundError{}
	}}
	if err := migrateLegacyNetwork(plan, links); err != nil {
		t.Fatal(err)
	}
	links = legacyTestLinks(t, &netlink.Dummy{Name: "iptv", Index: 20})
	if err := migrateLegacyNetwork(plan, links); !errors.Is(err, errLegacyNetworkMismatch) {
		t.Fatalf("migration error = %v; want mismatch", err)
	}
	assertPendingLegacyNetwork(t, plan)
}

func TestLegacyNetworkMigrationPreservesFailure(t *testing.T) {
	t.Parallel()
	plan := legacyNetworkPlan(t, legacyNetworkConfig)
	cause := os.ErrPermission
	for _, failAt := range []string{"iptv", "eth8", "alias"} {
		t.Run(failAt, func(t *testing.T) {
			links := legacyTestLinks(t, legacyTestVLAN())
			find := links.find
			links.find = func(name string) (netlink.Link, error) {
				if name == failAt {
					return nil, cause
				}
				return find(name)
			}
			links.setAlias = func(netlink.Link, string) error { return cause }
			if err := migrateLegacyNetwork(plan, links); !errors.Is(err, cause) {
				t.Fatalf("migration error = %v; want original failure", err)
			}
			assertPendingLegacyNetwork(t, plan)
		})
	}
}
