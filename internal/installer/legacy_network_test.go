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

func legacyTestVLAN() *netlink.Vlan {
	return &netlink.Vlan{Name: "iptv", Index: 20, ParentIndex: 8, VlanId: 4}
}

// legacyTestLinks answers lookups for the v4 interface and its parent and
// fails the test on any mutation.
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
			t.Fatal("unexpected interface handover")
			return nil
		},
		remove: func(netlink.Link) error {
			t.Fatal("unexpected interface removal")
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

// The live interface must be the one v4 recorded. A v5 configuration that
// differs from v4 is a corrected configuration.
func TestLegacyNetworkMigrationRejectsInterfacesV4DidNotCreate(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		change func(*netlink.Vlan)
	}{
		{"observed parent differs", func(v *netlink.Vlan) { v.ParentIndex = 9 }},
		{"observed VLAN differs", func(v *netlink.Vlan) { v.VlanId = 5 }},
		{"another owner", func(v *netlink.Vlan) { v.Alias = "firmware" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			plan := legacyNetworkPlan(t, legacyNetworkConfig)
			vlan := legacyTestVLAN()
			test.change(vlan)
			links := legacyTestLinks(t, vlan)
			if _, err := inspectLegacyNetwork(plan, links); !errors.Is(err, errLegacyNetworkMismatch) {
				t.Fatalf("inspection error = %v; want mismatch", err)
			}
			if err := migrateLegacyNetwork(plan, links); !errors.Is(err, errLegacyNetworkMismatch) {
				t.Fatalf("migration error = %v; want mismatch", err)
			}
			assertPendingLegacyNetwork(t, plan)
		})
	}
}

// A v5 configuration reusing the v4 name adopts the v4 interface, whatever
// parent or VLAN v5 chose. Daemon startup replaces an adopted interface.
func TestLegacyNetworkMigrationAdoptsWhenTheNameIsReused(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		change func(*Plan)
	}{
		{"same configuration", func(*Plan) {}},
		{"corrected parent", func(p *Plan) { p.Config.WAN.Interface = "eth9" }},
		{"corrected VLAN", func(p *Plan) { p.Config.WAN.VLAN = 5 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			plan := legacyNetworkPlan(t, legacyNetworkConfig)
			test.change(&plan)
			vlan := legacyTestVLAN()
			links := legacyTestLinks(t, vlan)
			handover, err := inspectLegacyNetwork(plan, links)
			if err != nil || handover.link != vlan || !handover.adopt {
				t.Fatalf("inspection = %+v, %v; want adoption", handover, err)
			}
			mutations := 0
			links.setAlias = func(link netlink.Link, alias string) error {
				if link != vlan || alias != legacyNetworkAlias {
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
		})
	}
}

// A v5 configuration under another name leaves nothing to replace the v4
// interface, so its stale addresses and routes go with it.
func TestLegacyNetworkMigrationRemovesAnInterfaceV5WillNotReuse(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		change func(*Plan)
	}{
		{"corrected name", func(p *Plan) { p.Config.WAN.VLANInterface = "other" }},
		{"untagged v5", func(p *Plan) { p.Config.WAN.VLAN = 0 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			plan := legacyNetworkPlan(t, legacyNetworkConfig)
			test.change(&plan)
			vlan := legacyTestVLAN()
			links := legacyTestLinks(t, vlan)
			handover, err := inspectLegacyNetwork(plan, links)
			if err != nil || handover.link != vlan || handover.adopt {
				t.Fatalf("inspection = %+v, %v; want removal", handover, err)
			}
			removed := 0
			links.remove = func(link netlink.Link) error {
				if link != vlan {
					t.Fatalf("unexpected removal: %v", link)
				}
				removed++
				return nil
			}
			if err := migrateLegacyNetwork(plan, links); err != nil {
				t.Fatal(err)
			}
			if removed != 1 {
				t.Fatalf("removal performed %d times", removed)
			}
			assertPendingLegacyNetwork(t, plan)
		})
	}
}

// Stale provenance without a live interface, and an interface that already
// carries our alias from an earlier attempt or a daemon start, need nothing.
func TestLegacyNetworkMigrationRetryAfterHandoverOrReplacement(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		link netlink.Link
	}{
		{"handed over", &netlink.Vlan{Name: "iptv", Index: 20, ParentIndex: 8, VlanId: 4, Alias: legacyNetworkAlias}},
		{"replaced on corrected parent", &netlink.Vlan{Name: "iptv", Index: 21, ParentIndex: 9, VlanId: 4, Alias: legacyNetworkAlias}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			plan := legacyNetworkPlan(t, legacyNetworkConfig)
			plan.Config.WAN.Interface = "eth9"
			links := legacyTestLinks(t, test.link)
			handover, err := inspectLegacyNetwork(plan, links)
			if err != nil || handover.link != nil {
				t.Fatalf("inspection = %+v, %v; want nothing to do", handover, err)
			}
			if err := migrateLegacyNetwork(plan, links); err != nil {
				t.Fatal(err)
			}
			assertPendingLegacyNetwork(t, plan)
		})
	}
	plan := legacyNetworkPlan(t, legacyNetworkConfig)
	plan.Config.WAN.Interface = "eth9"
	links := legacyNetworkLinks{find: func(string) (netlink.Link, error) {
		return nil, netlink.LinkNotFoundError{}
	}}
	if err := migrateLegacyNetwork(plan, links); err != nil {
		t.Fatalf("stale provenance without a live interface blocked the install: %v", err)
	}
}

// An interface handed over on an earlier attempt is ours; a v5 configuration
// that then drops its name leaves nothing to replace it, so it goes.
func TestLegacyNetworkMigrationRemovesAHandedOverInterfaceV5Renamed(t *testing.T) {
	t.Parallel()
	plan := legacyNetworkPlan(t, legacyNetworkConfig)
	plan.Config.WAN.VLANInterface = "other"
	vlan := &netlink.Vlan{Name: "iptv", Index: 20, ParentIndex: 8, VlanId: 4, Alias: legacyNetworkAlias}
	links := legacyTestLinks(t, vlan)
	handover, err := inspectLegacyNetwork(plan, links)
	if err != nil || handover.link != vlan || handover.adopt {
		t.Fatalf("inspection = %+v, %v; want removal", handover, err)
	}
	removed := 0
	links.remove = func(link netlink.Link) error {
		if link != vlan {
			t.Fatalf("unexpected removal: %v", link)
		}
		removed++
		return nil
	}
	if err := migrateLegacyNetwork(plan, links); err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removal performed %d times", removed)
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

func TestLegacyNetworkMigrationRejectsANonVLAN(t *testing.T) {
	t.Parallel()
	plan := legacyNetworkPlan(t, legacyNetworkConfig)
	links := legacyTestLinks(t, &netlink.Dummy{Name: "iptv", Index: 20})
	if err := migrateLegacyNetwork(plan, links); !errors.Is(err, errLegacyNetworkMismatch) {
		t.Fatalf("migration error = %v; want mismatch", err)
	}
	assertPendingLegacyNetwork(t, plan)
}

func TestLegacyNetworkMigrationPreservesFailure(t *testing.T) {
	t.Parallel()
	plan := legacyNetworkPlan(t, legacyNetworkConfig)
	cause := os.ErrPermission
	for _, failAt := range []string{"iptv", "eth8", "alias", "remove"} {
		t.Run(failAt, func(t *testing.T) {
			t.Parallel()
			failing := plan
			if failAt == "remove" {
				failing.Config.WAN.VLANInterface = "other"
			}
			links := legacyTestLinks(t, legacyTestVLAN())
			find := links.find
			links.find = func(name string) (netlink.Link, error) {
				if name == failAt {
					return nil, cause
				}
				return find(name)
			}
			links.setAlias = func(netlink.Link, string) error { return cause }
			links.remove = func(netlink.Link) error { return cause }
			if err := migrateLegacyNetwork(failing, links); !errors.Is(err, cause) {
				t.Fatalf("migration error = %v; want original failure", err)
			}
			assertPendingLegacyNetwork(t, failing)
		})
	}
}
