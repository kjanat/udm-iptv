package diagnostics

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kjanat/udm-iptv/internal/proxyinventory"
)

func TestProxyAvailabilityIsInTextAndJSON(t *testing.T) {
	value := Snapshot{Proxies: proxyinventory.Inventory{
		{Name: "improxy", Available: true, Source: "system", Path: "/usr/sbin/improxy", Version: "0.3", BuildID: "abcd1234", Features: map[string]proxyinventory.Feature{"ipv4_querier_election": {Status: "unknown", Evidence: "not queried"}}},
		{Name: "igmpproxy", Source: "missing", Reason: "not installed"},
	}}
	text := RenderSnapshot(value)
	for _, expected := range []string{"improxy executable: available (system)", "improxy reported version: 0.3", "ELF build ID abcd1234", "ipv4_querier_election: unknown", "igmpproxy executable: unavailable (missing): not installed"} {
		if !strings.Contains(text, expected) {
			t.Errorf("missing %q", expected)
		}
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Snapshot
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Proxies.Find("improxy").BuildID != "abcd1234" || decoded.Proxies.Find("igmpproxy").Available {
		t.Fatal("structured discovery lost evidence")
	}
}

func TestOldSnapshotDoesNotInventMissingProxies(t *testing.T) {
	if !strings.Contains(RenderSnapshot(Snapshot{}), "Proxy availability: not collected") {
		t.Fatal("old capture reported a proxy as absent")
	}
}
