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

func (table *fakeNATTable) ListWithCounters(string, string) ([]string, error) {
	return slices.Clone(table.rules), nil
}

func (table *fakeNATTable) Delete(_, _ string, spec ...string) error {
	if table.fail {
		return errDelete
	}
	index := slices.IndexFunc(table.rules, func(line string) bool { return sameRule(line, spec) })
	if index < 0 {
		return fmt.Errorf("%w: %s", errMissing, strings.Join(spec, " "))
	}
	table.deleted = append(table.deleted, table.rules[index])
	table.rules = slices.Delete(table.rules, index, index+1)

	return nil
}

func (table *fakeNATTable) AppendUnique(_, _ string, spec ...string) error {
	if !slices.ContainsFunc(table.rules, func(line string) bool { return sameRule(line, spec) }) {
		table.rules = append(table.rules, "-A POSTROUTING "+strings.Join(spec, " "))
	}

	return nil
}

// sameRule compares a listed line with a spec the way iptables -C does:
// without the counters, and with "-d 0.0.0.0/0" meaning no -d at all.
func sameRule(line string, spec []string) bool {
	fields := strings.Fields(strings.ReplaceAll(line, `"udm-iptv"`, "udm-iptv"))
	if len(fields) < 2 || fields[0] != "-A" {
		return false
	}
	stripped, _, _ := splitCounters(fields[2:])

	return slices.Equal(withoutAnyDestination(stripped), withoutAnyDestination(spec))
}

func withoutAnyDestination(spec []string) []string {
	index := slices.Index(spec, "-d")
	if index < 0 || index+1 >= len(spec) || spec[index+1] != "0.0.0.0/0" {
		return spec
	}

	return slices.Concat(spec[:index], spec[index+2:])
}

// routerChain is iptables -t nat -v -S POSTROUTING on a UDM Pro that ran the
// shell version before the Go version.
func routerChain() []string {
	return []string{
		"-P POSTROUTING ACCEPT -c 1556696 121474668",
		"-A POSTROUTING -c 68702225 6702457058 -j UBIOS_POSTROUTING_JUMP",
		"-A POSTROUTING -d 213.75.0.0/16 -o iptv -c 187 27452 -j MASQUERADE",
		"-A POSTROUTING -d 217.166.0.0/16 -o iptv -c 0 0 -j MASQUERADE",
		"-A POSTROUTING -d 195.121.0.0/16 -o iptv -c 0 0 -j MASQUERADE",
		"-A POSTROUTING -d 213.75.0.0/16 -o iptv -m comment --comment udm-iptv -c 0 0 -j MASQUERADE",
		"-A POSTROUTING -d 217.166.0.0/16 -o iptv -m comment --comment udm-iptv -c 12 3456 -j MASQUERADE",
		"-A POSTROUTING -d 195.121.0.0/16 -o iptv -m comment --comment udm-iptv -c 0 0 -j MASQUERADE",
		"-A POSTROUTING -d 10.0.0.0/8 -o eth8.6 -c 5 500 -j MASQUERADE",
	}
}

func kpnConfig() config.Config {
	value := config.DefaultKPN()
	value.WAN.NATDestinations = []string{"213.75.0.0/16", "217.166.0.0/16", "195.121.0.0/16"}

	return value
}

func TestEnsureNATRemovesOnlyStaleOwnedRules(t *testing.T) {
	t.Parallel()
	table := &fakeNATTable{rules: routerChain()}
	value := kpnConfig()
	value.WAN.NATDestinations = []string{"213.75.0.0/16", "217.166.0.0/16", "213.75.112.0/21"}
	removed, err := reconcileNAT(table, value)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"-P POSTROUTING ACCEPT -c 1556696 121474668",
		"-A POSTROUTING -c 68702225 6702457058 -j UBIOS_POSTROUTING_JUMP",
		"-A POSTROUTING -d 213.75.0.0/16 -o iptv -c 187 27452 -j MASQUERADE",
		"-A POSTROUTING -d 217.166.0.0/16 -o iptv -c 0 0 -j MASQUERADE",
		"-A POSTROUTING -d 195.121.0.0/16 -o iptv -c 0 0 -j MASQUERADE",
		"-A POSTROUTING -d 213.75.0.0/16 -o iptv -m comment --comment udm-iptv -c 0 0 -j MASQUERADE",
		"-A POSTROUTING -d 217.166.0.0/16 -o iptv -m comment --comment udm-iptv -c 12 3456 -j MASQUERADE",
		"-A POSTROUTING -d 10.0.0.0/8 -o eth8.6 -c 5 500 -j MASQUERADE",
		"-A POSTROUTING -d 213.75.112.0/21 -o iptv -m comment --comment udm-iptv -j MASQUERADE",
	}
	if !slices.Equal(table.rules, want) {
		t.Fatalf("chain =\n%s\nwant\n%s", strings.Join(table.rules, "\n"), strings.Join(want, "\n"))
	}
	wantRemoved := []NATRule{
		{Destination: "195.121.0.0/16", Managed: true},
	}
	if !slices.Equal(removed, wantRemoved) {
		t.Fatalf("removed = %v, want %v", removed, wantRemoved)
	}
	if got := removed[0].String(); got != "managed 195.121.0.0/16: 0 packets, 0 bytes" {
		t.Fatalf("removed[0] = %q", got)
	}
	for _, line := range table.deleted {
		if strings.Contains(strings.Join(strings.Fields(line), " "), " -c ") && !strings.Contains(line, "-c ") {
			t.Fatalf("delete spec kept counters: %s", line)
		}
	}
}

func TestEnsureNATLeavesAConsistentChainAlone(t *testing.T) {
	t.Parallel()
	table := &fakeNATTable{rules: routerChain()[:2]}
	if _, err := reconcileNAT(table, kpnConfig()); err != nil {
		t.Fatal(err)
	}
	before := slices.Clone(table.rules)
	removed, err := reconcileNAT(table, kpnConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(table.rules, before) || len(table.deleted) != 0 || len(removed) != 0 {
		t.Fatalf("second reconcile changed the chain: %q, removed %v", table.rules, removed)
	}
}

// iptables prints a rule for every destination without -d, so the
// unrestricted rule must be recognised in that form to survive a restart.
func TestEnsureNATKeepsTheUnrestrictedRule(t *testing.T) {
	t.Parallel()
	table := &fakeNATTable{rules: []string{
		"-P POSTROUTING ACCEPT -c 0 0",
		"-A POSTROUTING -o iptv -m comment --comment udm-iptv -c 9 900 -j MASQUERADE",
	}}
	value := kpnConfig()
	value.WAN.NATDestinations = []string{"0.0.0.0/0"}
	removed, err := reconcileNAT(table, value)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 0 || len(table.rules) != 2 {
		t.Fatalf("unrestricted rule was replaced: removed %v, chain %q", removed, table.rules)
	}
	rules, err := masqueradeRules(table, "iptv")
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].Destination != "0.0.0.0/0" || rules[0].Packets != 9 {
		t.Fatalf("rules = %+v", rules)
	}
}

func TestRemoveNATPreservesUnmanagedRulesOnTheInterface(t *testing.T) {
	t.Parallel()
	table := &fakeNATTable{rules: routerChain()}
	removed, err := removeNAT(table, kpnConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(table.rules) != 6 || len(table.deleted) != 3 || len(removed) != 3 {
		t.Fatalf("chain after removal: %q, removed %v", table.rules, removed)
	}
	if removed[0] != (NATRule{Destination: "213.75.0.0/16", Managed: true}) || removed[1] != (NATRule{Destination: "217.166.0.0/16", Managed: true, Packets: 12, Bytes: 3456}) {
		t.Fatalf("removed = %v", removed)
	}
	if _, err := removeNAT(&fakeNATTable{rules: routerChain(), fail: true}, kpnConfig()); !errors.Is(err, errDelete) {
		t.Fatalf("err = %v", err)
	}
}

func TestSplitCountersLeavesOtherSpecsAlone(t *testing.T) {
	t.Parallel()
	spec, packets, bytes := splitCounters(strings.Fields("-d 10.0.0.0/8 -o iptv -c 5 500 -j MASQUERADE"))
	if !slices.Equal(spec, strings.Fields("-d 10.0.0.0/8 -o iptv -j MASQUERADE")) || packets != 5 || bytes != 500 {
		t.Fatalf("spec=%v packets=%d bytes=%d", spec, packets, bytes)
	}
	plain := strings.Fields("-d 10.0.0.0/8 -o iptv -j MASQUERADE")
	if spec, packets, bytes := splitCounters(plain); !slices.Equal(spec, plain) || packets != 0 || bytes != 0 {
		t.Fatalf("spec=%v packets=%d bytes=%d", spec, packets, bytes)
	}
	odd := strings.Fields("-o iptv -c x y -j MASQUERADE")
	if spec, _, _ := splitCounters(odd); !slices.Equal(spec, odd) {
		t.Fatalf("malformed counters changed the spec: %v", spec)
	}
}

func TestNATOwnershipAcceptsOnlyExactComment(t *testing.T) {
	t.Parallel()
	table := &fakeNATTable{rules: []string{
		`-A POSTROUTING -o iptv -m comment --comment "udm-iptv" -j MASQUERADE`,
		`-A POSTROUTING -o iptv -m comment --comment "udm-iptv other" -j MASQUERADE`,
		`-A POSTROUTING -o iptv -m comment --comment udm-iptv-other -j MASQUERADE`,
		`-A POSTROUTING -o iptv -m comment --comment other -j MASQUERADE`,
	}}
	removed, err := removeNAT(table, kpnConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || len(table.rules) != 3 || !removed[0].Managed {
		t.Fatalf("comment ownership mismatch: removed=%v remaining=%v", removed, table.rules)
	}
}
