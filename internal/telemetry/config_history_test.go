package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/config"
)

func researchReporter(t *testing.T) (*Reporter, *recordingTransport) {
	t.Helper()
	r, transport := newRecordingReporter(t, testSettings())
	r.stateDir = t.TempDir()
	t.Cleanup(r.Close)

	return r, transport
}

func reportAt(t *testing.T, transport *recordingTransport, index int) researchReport {
	t.Helper()
	if len(transport.events) <= index {
		t.Fatalf("missing research event %d", index)
	}
	report, ok := transport.events[index].Contexts["research"]["report"].(researchReport)
	if !ok {
		t.Fatal("missing typed research report")
	}
	return report
}

type timeCheck int

const (
	timeUnchecked timeCheck = iota
	timeZero
	timeSet
)

func assertTimeCheck(t *testing.T, name string, value time.Time, check timeCheck) {
	t.Helper()
	switch check {
	case timeZero:
		if !value.IsZero() {
			t.Fatalf("%s is set", name)
		}
	case timeSet:
		if value.IsZero() {
			t.Fatalf("%s is zero", name)
		}
	case timeUnchecked:
	}
}

type historyStep struct {
	name             string
	mutate           func(*config.Config)
	applied          bool
	revision         uint64
	previousRevision uint64
	appliedRevision  uint64
	changed          []string
	appliedChange    timeCheck
	savedChangeOf    int
	appliedChangeOf  int
}

func configurationHistorySteps() []historyStep {
	return []historyStep{
		{
			name: "first save", mutate: func(*config.Config) {}, applied: false,
			revision: 1, previousRevision: 0, appliedRevision: 0, changed: nil,
			appliedChange: timeZero, savedChangeOf: -1, appliedChangeOf: -1,
		},
		{
			name: "apply with a telemetry preference change", mutate: func(value *config.Config) { value.Telemetry.Logs = false }, applied: true,
			revision: 1, previousRevision: 1, appliedRevision: 1, changed: []string{},
			appliedChange: timeSet, savedChangeOf: 0, appliedChangeOf: -1,
		},
		{
			name: "save without applying", mutate: func(value *config.Config) { value.Proxy.QuickLeave = true }, applied: false,
			revision: 2, previousRevision: 1, appliedRevision: 1, changed: []string{"quickleave"},
			appliedChange: timeSet, savedChangeOf: -1, appliedChangeOf: 1,
		},
		{
			name: "reopened settings", mutate: func(*config.Config) {}, applied: false,
			revision: 2, previousRevision: 2, appliedRevision: 1, changed: []string{},
			appliedChange: timeSet, savedChangeOf: 2, appliedChangeOf: 1,
		},
	}
}

func assertHistoryStep(t *testing.T, step historyStep, report researchReport, earlier []researchReport) {
	t.Helper()
	assertEqual(t, step.name+" revision", report.Revision, step.revision)
	assertEqual(t, step.name+" previous revision", report.PreviousRevision, step.previousRevision)
	assertEqual(t, step.name+" applied revision", report.AppliedRevision, step.appliedRevision)
	assertEqual(t, step.name+" applied", report.Applied, step.applied)
	assertTimeCheck(t, step.name+" last applied change", report.LastAppliedChange, step.appliedChange)
	if step.changed != nil && !reflect.DeepEqual(report.ChangedFields, step.changed) {
		t.Fatalf("%s changed fields = %v, want %v", step.name, report.ChangedFields, step.changed)
	}
	if step.savedChangeOf >= 0 {
		assertEqual(t, step.name+" last saved change", report.LastSavedChange, earlier[step.savedChangeOf].LastSavedChange)
	}
	if step.appliedChangeOf >= 0 {
		assertEqual(t, step.name+" last applied change", report.LastAppliedChange, earlier[step.appliedChangeOf].LastAppliedChange)
	}
}

func TestResearchSavedVersusAppliedAndMeaningfulChanges(t *testing.T) {
	r, transport := researchReporter(t)
	value := config.Default()
	steps := configurationHistorySteps()
	reports := make([]researchReport, 0, len(steps))
	for index, step := range steps {
		step.mutate(&value)
		if err := r.RecordConfiguration(context.Background(), value, step.applied, nil); err != nil {
			t.Fatal(err)
		}
		report := reportAt(t, transport, index)
		assertHistoryStep(t, step, report, reports)
		reports = append(reports, report)
	}
	info, err := os.Stat(filepath.Join(r.stateDir, "telemetry-research.json"))
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "research history permissions", info.Mode().Perm(), os.FileMode(0o600))
}

func TestResearchPublicPrefixes(t *testing.T) {
	value := config.Default()
	value.WAN.NATDestinations = []string{"11.22.33.0/24", "11.22.33.44/32", "10.12.0.0/16", "192.0.0.0/8", "203.0.113.0/24"}
	report := snapshot(value)
	if !reflect.DeepEqual(report.NAT, []string{"11.22.33.0/24"}) || report.CustomNAT != 4 {
		t.Fatal("prefix classification failed")
	}
}

func TestResearchSeparateProcessAndLiveRevocation(t *testing.T) {
	r, transport := researchReporter(t)
	value := config.Default()
	if err := r.RecordConfiguration(context.Background(), value, true, nil); err != nil {
		t.Fatal(err)
	}
	first := reportAt(t, transport, 0)
	other, err := newTestReporter(testSettings(), &recordingTransport{})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	other.stateDir = r.stateDir
	if other.installationID() != first.InstallationID {
		t.Fatal("identity changed between processes")
	}
	r.configPath = filepath.Join(r.stateDir, "config.json")
	value.Telemetry.Enabled = false
	if err := config.Save(r.configPath, value); err != nil {
		t.Fatal(err)
	}
	if r.installationID() != "" {
		t.Fatal("opt-out retained correlation")
	}
	if err := r.RecordObservation(Observation{Active: true, UptimeSeconds: 3600}); err != nil {
		t.Fatal(err)
	}
	if len(transport.events) != 1 {
		t.Fatal("observation after revocation")
	}
}

type networkChoiceCase struct {
	name       string
	network    bool
	provider   string
	method     string
	confidence string
}

func networkChoiceConfig() config.Config {
	value := config.Default()
	value.WAN.Interface = "private0"
	value.WAN.VLANMAC = "aa:bb:cc:dd:ee:ff"
	value.WAN.DHCPOptions = []string{"-V", "private-token"}
	value.WAN.StaticAddress = "192.168.199.7/24"
	value.WAN.NATDestinations = append(value.WAN.NATDestinations, "192.168.199.0/24", "11.22.33.44/32")

	return value
}

func assertNetworkIdentity(t *testing.T, identity *NetworkIdentity, test networkChoiceCase) {
	t.Helper()
	if !test.network {
		if identity != nil {
			t.Fatalf("report carried a network identity while the preference was off: %+v", identity)
		}

		return
	}
	if identity == nil {
		t.Fatal("PTR hint lost")
	}
	assertEqual(t, "detected provider", identity.Provider, test.provider)
	assertEqual(t, "detection method", identity.Method, test.method)
	assertEqual(t, "confidence", identity.Confidence, test.confidence)
}

func runNetworkChoiceCase(t *testing.T, test networkChoiceCase) {
	t.Helper()
	r, transport := researchReporter(t)
	r.settings.NetworkIdentity = test.network
	called := false
	lookup := func(context.Context) NetworkIdentity {
		called = true

		return NetworkIdentity{IP: "11.22.33.44", PTR: "Customer.KPN.NET."}
	}
	if err := r.RecordConfiguration(context.Background(), networkChoiceConfig(), true, lookup); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(transport.events[0])
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecrets(t, string(data), "private0", "aa:bb", "private-token", "192.168.199", "11.22.33.44/32", "fingerprint")
	assertEqual(t, "lookup performed", called, test.network)
	assertEqual(t, "PTR name in report", strings.Contains(string(data), "Customer.KPN.NET."), test.network)
	report := reportAt(t, transport, 0)
	assertEqual(t, "custom-prefix count", report.Settings.CustomNAT, 2)
	assertNetworkIdentity(t, report.Network, test)
}

func TestResearchAllowlistAndNetworkChoice(t *testing.T) {
	for _, test := range []networkChoiceCase{
		{name: "off", network: false},
		{name: "on", network: true, provider: "kpn", method: "ptr-suffix", confidence: "low"},
	} {
		t.Run(test.name, func(t *testing.T) { runNetworkChoiceCase(t, test) })
	}
}

func TestResearchOptOutAndForgedEvents(t *testing.T) {
	r, transport := researchReporter(t)
	value := config.Default()
	value.Telemetry.Presets = false
	r.configPath = filepath.Join(r.stateDir, "config.json")
	err := config.Save(r.configPath, value)
	if err != nil {
		t.Fatal(err)
	}
	err = r.RecordConfiguration(context.Background(), value, true, func(context.Context) NetworkIdentity { t.Fatal("lookup after opt-out"); return NetworkIdentity{} })
	if err != nil {
		t.Fatal(err)
	}
	if len(transport.events) != 0 {
		t.Fatal("sent after opt-out")
	}
	if _, err := os.Stat(filepath.Join(r.stateDir, "telemetry-research.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("opt-out created identity")
	}
	event := sentry.NewEvent()
	event.Transaction = "installation.report"
	event.Contexts["research"] = sentry.Context{"report": map[string]any{"secret": "unfiltered"}}
	if r.filterEvent(event, nil) != nil {
		t.Fatal("untyped SDK event bypassed allowlist")
	}
}

func assertRejectedFeedback(t *testing.T, r *Reporter) {
	t.Helper()
	for _, test := range []struct{ name, answer, provider string }{
		{"free text", "arbitrary private text", ""},
		{"arbitrary provider", "working", "private-customer"},
	} {
		if err := r.Feedback(test.answer, test.provider); err == nil {
			t.Fatalf("%s accepted", test.name)
		}
	}
}

func TestResearchResetAndFeedback(t *testing.T) {
	r, transport := researchReporter(t)
	if err := r.RecordConfiguration(context.Background(), config.Default(), true, nil); err != nil {
		t.Fatal(err)
	}
	old := r.installationID()
	assertEqual(t, "identity length", len(old), 32)
	if err := ResetIdentity(r.stateDir); err != nil {
		t.Fatal(err)
	}
	fresh := r.installationID()
	assertEqual(t, "identity length after reset", len(fresh), 32)
	if fresh == old {
		t.Fatal("running reporter did not notice reset")
	}
	if err := r.Feedback("problems", "freedom"); err != nil {
		t.Fatal(err)
	}
	report := reportAt(t, transport, 1)
	assertEqual(t, "feedback", report.Feedback, "problems")
	assertEqual(t, "confirmed provider", report.ConfirmedProvider, "freedom")
	assertEqual(t, "revision after reset", report.Revision, uint64(0))
	assertTimeCheck(t, "last applied change after reset", report.LastAppliedChange, timeZero)
	if report.Settings != nil {
		t.Fatalf("reset retained settings history: %+v", report.Settings)
	}
	assertRejectedFeedback(t, r)
}

func TestResearchRejectsSymlinks(t *testing.T) {
	r, transport := researchReporter(t)
	target := filepath.Join(t.TempDir(), "target")
	if err := atomicfile.Write(target, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(r.stateDir, "telemetry-research.json")); err != nil {
		t.Fatal(err)
	}
	if err := r.RecordConfiguration(context.Background(), config.Default(), true, nil); err == nil {
		t.Fatal("followed state symlink")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "untouched" || len(transport.events) != 0 {
		t.Fatal("symlink target changed or report sent")
	}
}

type lookupCase struct {
	name     string
	body     string
	called   bool
	ip       string
	provider string
	method   string
}

func runLookupCase(t *testing.T, test lookupCase) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(test.body)) }))
	defer server.Close()
	called := false
	result := lookupNetwork(context.Background(), server.Client(), server.URL, func(_ context.Context, ip string) ([]string, error) {
		called = true
		assertEqual(t, "PTR address", ip, "11.22.33.44")

		return []string{"customer.kpn.net."}, nil
	})
	assertEqual(t, "PTR lookup performed", called, test.called)
	assertEqual(t, "public IP", result.IP, test.ip)
	assertEqual(t, "detected provider", result.Provider, test.provider)
	assertEqual(t, "detection method", result.Method, test.method)
}

func TestNetworkLookupIsBoundedAndUsesPTR(t *testing.T) {
	for _, test := range []lookupCase{
		{name: "public address", body: "11.22.33.44", called: true, ip: "11.22.33.44", provider: "kpn", method: "ptr-suffix"},
		{name: "private address", body: "192.168.1.1", called: false, ip: "", provider: "unknown", method: "none"},
		{name: "oversized body", body: strings.Repeat("1", 66), called: false, ip: "", provider: "unknown", method: "none"},
	} {
		t.Run(test.name, func(t *testing.T) { runLookupCase(t, test) })
	}
	for _, name := range []string{"notkpn.net", "kpn.net.attacker.invalid", "customer\n.kpn.net"} {
		result := cleanIdentity(NetworkIdentity{IP: "11.22.33.44", PTR: name})
		assertEqual(t, "provider for "+name, result.Provider, "unknown")
	}
}
