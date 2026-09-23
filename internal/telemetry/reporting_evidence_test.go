package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/config/configtest"
)

func TestOperationalIdentitySurvivesDisabledPresets(t *testing.T) {
	settings := testSettings()
	settings.Presets = false
	r, transport := newRecordingReporter(t, settings)
	r.stateDir = t.TempDir()
	defer r.Close()
	want := r.installationID()
	if !validIdentity(want) {
		t.Fatal("operational reporting has no persisted installation identity")
	}
	other, otherTransport := newRecordingReporter(t, settings)
	other.stateDir = r.stateDir
	defer other.Close()
	if other.installationID() != want {
		t.Fatal("operational identity changed between processes")
	}
	_ = other.Run(t.Context(), "service.health", func(context.Context) error { return errOperationFailed })
	other.Flush()
	failures := failureEvents(otherTransport.events)
	if len(failures) != 1 || failures[0].User.ID != want {
		t.Fatal("failure lost installation correlation when presets were disabled")
	}
	if other.installationID() != want || len(transport.events) != 0 {
		t.Fatal("identity lookup emitted a report or changed its value")
	}
}

func TestIdentityCorruptionIsVisibleWithoutReplacingEvidence(t *testing.T) {
	r, _ := researchReporter(t)
	var output bytes.Buffer
	r.deliveryOutput = &output
	path := filepath.Join(r.stateDir, "telemetry-research.json")
	const broken = "{broken research state"
	if err := atomicfile.Write(path, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	if r.installationID() != "" || !strings.Contains(output.String(), "decode installation identity") {
		t.Fatal("corrupt identity was silently replaced or ignored")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != broken {
		t.Fatal("corrupt identity evidence was overwritten")
	}
}

func TestResearchDeliveryFailureReachesCaller(t *testing.T) {
	r, transport := researchReporter(t)
	r.window = time.Now()
	r.counts["presets"] = presetsPerMinute
	value := configtest.Custom()
	if err := r.RecordConfiguration(t.Context(), value, true, nil); !errors.Is(err, errResearchNotQueued) {
		t.Fatalf("configuration report falsely succeeded: %v", err)
	}
	if err := r.RecordObservation(t.Context(), Observation{Active: true}); !errors.Is(err, errResearchNotQueued) {
		t.Fatalf("observation report falsely succeeded: %v", err)
	}
	state, err := loadResearchState(filepath.Join(r.stateDir, "telemetry-research.json"))
	if err != nil || state.Settings.Profile != value.Profile || len(transport.events) != 0 {
		t.Fatal("delivery failure lost local configuration history")
	}
}

func TestResearchEncodingFailureReachesCaller(t *testing.T) {
	r, transport := researchReporter(t)
	err := r.RecordObservation(t.Context(), Observation{Snapshot: json.RawMessage("not JSON")})
	if err == nil || !strings.Contains(err.Error(), "encode observation report") || len(transport.events) != 0 {
		t.Fatalf("invalid snapshot was silently dropped: %v", err)
	}
}

func TestResearchFlushFailureReachesCaller(t *testing.T) {
	r, err := newTestReporter(testSettings(), &refusingFlushTransport{})
	if err != nil {
		t.Fatal(err)
	}
	r.stateDir = t.TempDir()
	defer r.Close()
	if err := r.RecordObservation(t.Context(), Observation{Active: true}); !errors.Is(err, errResearchNotFlushed) {
		t.Fatalf("incomplete flush reported success: %v", err)
	}
}

func TestRepeatedNetworkReportsKeepObservedIdentity(t *testing.T) {
	r, transport := researchReporter(t)
	lookups := 0
	lookup := func(context.Context) NetworkIdentity {
		lookups++
		return NetworkIdentity{IP: "11.22.33.44", PTR: "customer.kpn.net."}
	}
	for range 2 {
		if err := r.RecordConfiguration(t.Context(), configtest.Custom(), true, lookup); err != nil {
			t.Fatal(err)
		}
	}
	first, second := reportAt(t, transport, 0), reportAt(t, transport, 1)
	assertCachedNetworkIdentity(t, first.Network, second.Network)
	if lookups != 1 {
		t.Fatalf("cached report repeated network lookup: %d", lookups)
	}
	r.settings.NetworkIdentity = false
	if err := r.RecordConfiguration(t.Context(), configtest.Custom(), true, lookup); err != nil {
		t.Fatal(err)
	}
	if reportAt(t, transport, 2).Network != nil || lookups != 1 {
		t.Fatal("network opt-out retained cached identity")
	}
}

func assertCachedNetworkIdentity(t *testing.T, first, second *NetworkIdentity) {
	t.Helper()
	if first == nil || second == nil {
		t.Fatal("lookup budget erased report identity")
	}
	if first.ObservedAt.IsZero() || *first != *second {
		t.Fatalf("cached identity changed its original evidence: %+v; %+v", first, second)
	}
}
