package network

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/kjanat/udm-iptv/internal/config"
)

var (
	errDelete  = errors.New("delete refused")
	errMissing = errors.New("rule not present")
)

type fakeNATTable struct {
	rules   []string
	deleted []string
	fail    bool
}

func (table *fakeNATTable) List(string, string) ([]string, error) {
	return slices.Clone(table.rules), nil
}

func (table *fakeNATTable) Delete(_, _ string, spec ...string) error {
	if table.fail {
		return errDelete
	}
	line := "-A POSTROUTING " + strings.Join(spec, " ")
	index := slices.Index(table.rules, line)
	if index < 0 {
		return fmt.Errorf("%w: %s", errMissing, line)
	}
	table.rules = slices.Delete(table.rules, index, index+1)
	table.deleted = append(table.deleted, line)

	return nil
}

func (table *fakeNATTable) AppendUnique(_, _ string, spec ...string) error {
	line := "-A POSTROUTING " + strings.Join(spec, " ")
	if !slices.Contains(table.rules, line) {
		table.rules = append(table.rules, line)
	}

	return nil
}

// routerChain is iptables -t nat -S POSTROUTING on a UDM Pro that ran the
// shell version before the Go version.
func routerChain() []string {
	return []string{
		"-P POSTROUTING ACCEPT",
		"-A POSTROUTING -j UBIOS_POSTROUTING_JUMP",
		"-A POSTROUTING -d 213.75.0.0/16 -o iptv -j MASQUERADE",
		"-A POSTROUTING -d 217.166.0.0/16 -o iptv -j MASQUERADE",
		"-A POSTROUTING -d 195.121.0.0/16 -o iptv -j MASQUERADE",
		"-A POSTROUTING -d 213.75.0.0/16 -o iptv -m comment --comment udm-iptv -j MASQUERADE",
		"-A POSTROUTING -d 217.166.0.0/16 -o iptv -m comment --comment udm-iptv -j MASQUERADE",
		"-A POSTROUTING -d 195.121.0.0/16 -o iptv -m comment --comment udm-iptv -j MASQUERADE",
		"-A POSTROUTING -d 10.0.0.0/8 -o eth8.6 -j MASQUERADE",
	}
}

func kpnConfig() config.Config {
	value := config.DefaultKPN()
	value.WAN.NATDestinations = []string{"213.75.0.0/16", "217.166.0.0/16", "195.121.0.0/16"}

	return value
}

func TestEnsureNATRemovesLegacyAndStaleRules(t *testing.T) {
	t.Parallel()
	table := &fakeNATTable{rules: routerChain()}
	value := kpnConfig()
	value.WAN.NATDestinations = []string{"213.75.0.0/16", "217.166.0.0/16", "213.75.112.0/21"}
	if err := reconcileNAT(table, value); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"-P POSTROUTING ACCEPT",
		"-A POSTROUTING -j UBIOS_POSTROUTING_JUMP",
		"-A POSTROUTING -d 213.75.0.0/16 -o iptv -m comment --comment udm-iptv -j MASQUERADE",
		"-A POSTROUTING -d 217.166.0.0/16 -o iptv -m comment --comment udm-iptv -j MASQUERADE",
		"-A POSTROUTING -d 10.0.0.0/8 -o eth8.6 -j MASQUERADE",
		"-A POSTROUTING -d 213.75.112.0/21 -o iptv -m comment --comment udm-iptv -j MASQUERADE",
	}
	if !slices.Equal(table.rules, want) {
		t.Fatalf("chain =\n%s\nwant\n%s", strings.Join(table.rules, "\n"), strings.Join(want, "\n"))
	}
	if len(table.deleted) != 4 {
		t.Fatalf("deleted %d rules: %q", len(table.deleted), table.deleted)
	}
}

func TestEnsureNATLeavesAConsistentChainAlone(t *testing.T) {
	t.Parallel()
	table := &fakeNATTable{rules: routerChain()[:2]}
	if err := reconcileNAT(table, kpnConfig()); err != nil {
		t.Fatal(err)
	}
	before := slices.Clone(table.rules)
	if err := reconcileNAT(table, kpnConfig()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(table.rules, before) || len(table.deleted) != 0 {
		t.Fatalf("second reconcile changed the chain: %q", table.rules)
	}
}

func TestRemoveNATClearsEveryRuleOnTheInterface(t *testing.T) {
	t.Parallel()
	table := &fakeNATTable{rules: routerChain()}
	if err := removeNAT(table, kpnConfig()); err != nil {
		t.Fatal(err)
	}
	if len(table.rules) != 3 || len(table.deleted) != 6 {
		t.Fatalf("chain after removal: %q", table.rules)
	}
	if rules, _ := masqueradeRules(&fakeNATTable{rules: routerChain()}, "iptv"); rules[0].destination != "213.75.0.0/16" || rules[0].managed || !rules[3].managed {
		t.Fatalf("rules = %+v", rules)
	}
	if err := removeNAT(&fakeNATTable{rules: routerChain(), fail: true}, kpnConfig()); !errors.Is(err, errDelete) {
		t.Fatalf("err = %v", err)
	}
}
