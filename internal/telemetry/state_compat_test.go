package telemetry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/config/configtest"
)

func TestLegacyResearchStateSurvivesUpgrade(t *testing.T) {
	r, transport := researchReporter(t)
	path := filepath.Join(r.stateDir, "telemetry-research.json")
	const id = "0123456789abcdef0123456789abcdef"
	legacy := seedLegacyResearchState(t, path)
	if got := r.installationID(); got != id {
		t.Fatalf("legacy installation identity = %q, want %q", got, id)
	}
	before, err := loadResearchState(path)
	if err != nil {
		t.Fatal(err)
	}
	if before.hasSettings() {
		t.Fatal("reduced snapshot was treated as a full configuration")
	}
	if err := r.RecordObservation(t.Context(), Observation{Active: true}); err != nil {
		t.Fatal(err)
	}
	if reportAt(t, transport, 0).Settings != nil {
		t.Fatal("observation invented configuration from the reduced snapshot")
	}
	assertLegacyResearchStateUnchanged(t, path, legacy)
	assertLegacyResearchMigration(t, r, transport, path, id)
}

func TestResearchStateRejectsMalformedSettings(t *testing.T) {
	for _, settings := range []string{
		`{"proxy":"improxy"}`,
		`{"selected_profile":"custom","proxy":42}`,
		`{"profile":"custom","proxy":{"igmpVersion":"broken"}}`,
		`{"selected_profile":42,"proxy":"improxy"}`,
	} {
		t.Run(settings, func(t *testing.T) {
			var state researchState
			if err := json.Unmarshal([]byte(`{"settings":`+settings+`}`), &state); err == nil {
				t.Fatal("malformed settings accepted as legacy state")
			}
		})
	}
}

func seedLegacyResearchState(t *testing.T, path string) string {
	t.Helper()
	const legacy = `{
		"id":"0123456789abcdef0123456789abcdef",
		"revision":7,"applied_revision":6,
		"fingerprint":"old-saved","applied_fingerprint":"old-applied",
		"last_saved_change":"2026-09-15T10:00:00Z",
		"last_applied_change":"2026-09-15T09:00:00Z",
		"settings":{"selected_profile":"custom","vlan":4,"dhcp":true,
			"static_address_configured":false,"dhcp_routes":"no-default",
			"proxy":"improxy","igmp_version":3,"quickleave":false,
			"proxy_debug":false,"downstream_count":1,
			"nat_prefixes":["213.75.0.0/16"],"source_prefixes":[],
			"omitted_nat_prefix_count":1,"omitted_source_prefix_count":0}
	}`
	if err := atomicfile.Write(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	return legacy
}

func assertLegacyResearchStateUnchanged(t *testing.T, path, legacy string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var original, rewritten any
	if err := json.Unmarshal([]byte(legacy), &original); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &rewritten); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original, rewritten) {
		t.Fatal("observation changed legacy state")
	}
}

func assertLegacyResearchMigration(t *testing.T, r *Reporter, transport *recordingTransport, path, id string) {
	t.Helper()
	value := configtest.Custom()
	if err := r.RecordConfiguration(t.Context(), value, true, nil); err != nil {
		t.Fatal(err)
	}
	after, err := loadResearchState(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.ID != id || after.Revision != 8 || after.AppliedRevision != 8 || after.legacySettings != nil || !reflect.DeepEqual(after.Settings, normalized(value)) {
		t.Fatalf("configuration save did not migrate legacy history: %+v", after)
	}
	report := reportAt(t, transport, 1)
	assertEqual(t, "installation identity", report.InstallationID, id)
	assertEqual(t, "previous revision", report.PreviousRevision, uint64(7))
	assertEqual(t, "revision", report.Revision, uint64(8))
}
