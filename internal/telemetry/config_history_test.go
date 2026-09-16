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

	"github.com/getsentry/sentry-go"

	"github.com/kjanat/udm-iptv/internal/config"
)

func researchReporter(t *testing.T) (*Reporter, *recordingTransport) {
	t.Helper()
	transport := &recordingTransport{}
	r, err := newTestReporter(testSettings(), "test", transport)
	if err != nil {
		t.Fatal(err)
	}
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

func TestResearchSavedVersusAppliedAndMeaningfulChanges(t *testing.T) {
	r, transport := researchReporter(t)
	value := config.Default()
	if err := r.RecordConfiguration(context.Background(), value, false, nil); err != nil {
		t.Fatal(err)
	}
	first := reportAt(t, transport, 0)
	if first.Revision != 1 || first.Applied || !first.LastAppliedChange.IsZero() {
		t.Fatal("saved configuration marked applied")
	}
	value.Telemetry.Logs = false
	if err := r.RecordConfiguration(context.Background(), value, true, nil); err != nil {
		t.Fatal(err)
	}
	applied := reportAt(t, transport, 1)
	if applied.Revision != 1 || !applied.Applied || applied.LastAppliedChange.IsZero() || applied.LastSavedChange != first.LastSavedChange {
		t.Fatal("application or telemetry preference counted as a settings change")
	}
	value.Proxy.QuickLeave = true
	if err := r.RecordConfiguration(context.Background(), value, false, nil); err != nil {
		t.Fatal(err)
	}
	failed := reportAt(t, transport, 2)
	if failed.Revision != 2 || failed.PreviousRevision != 1 || failed.AppliedRevision != 1 || failed.LastAppliedChange != applied.LastAppliedChange || !reflect.DeepEqual(failed.ChangedFields, []string{"quickleave"}) {
		t.Fatal("failed application advanced applied history or lost changed fields")
	}
	if err := r.RecordConfiguration(context.Background(), value, false, nil); err != nil {
		t.Fatal(err)
	}
	repeated := reportAt(t, transport, 3)
	if repeated.Revision != 2 || repeated.LastSavedChange != failed.LastSavedChange || len(repeated.ChangedFields) != 0 {
		t.Fatal("reopening settings counted as a change")
	}
	info, err := os.Stat(filepath.Join(r.stateDir, "telemetry-research.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("research history permissions")
	}
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
	other, err := newTestReporter(testSettings(), "test", &recordingTransport{})
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

func TestResearchAllowlistAndNetworkChoice(t *testing.T) {
	for _, network := range []bool{false, true} {
		t.Run(map[bool]string{false: "off", true: "on"}[network], func(t *testing.T) {
			r, transport := researchReporter(t)
			r.settings.NetworkIdentity = network
			value := config.Default()
			value.WAN.Interface = "private0"
			value.WAN.VLANMAC = "aa:bb:cc:dd:ee:ff"
			value.WAN.DHCPOptions = []string{"-V", "private-token"}
			value.WAN.StaticAddress = "192.168.199.7/24"
			value.WAN.NATDestinations = append(value.WAN.NATDestinations, "192.168.199.0/24", "11.22.33.44/32")
			called := false
			lookup := func(context.Context) NetworkIdentity {
				called = true
				return NetworkIdentity{IP: "11.22.33.44", PTR: "Customer.KPN.NET."}
			}
			if err := r.RecordConfiguration(context.Background(), value, true, lookup); err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(transport.events[0])
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"private0", "aa:bb", "private-token", "192.168.199", "11.22.33.44/32", "fingerprint"} {
				if strings.Contains(string(data), secret) {
					t.Fatalf("report leaked %s", secret)
				}
			}
			if called != network || strings.Contains(string(data), "Customer.KPN.NET.") != network {
				t.Fatal("network preference ignored")
			}
			report := reportAt(t, transport, 0)
			if report.Settings.CustomNAT != 2 {
				t.Fatal("custom-prefix count lost")
			}
			if network && (report.Network.Provider != "kpn" || report.Network.Confidence != "low") {
				t.Fatal("PTR hint lost or overconfident")
			}
		})
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

func TestResearchResetAndFeedback(t *testing.T) {
	r, transport := researchReporter(t)
	err := r.RecordConfiguration(context.Background(), config.Default(), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	old := r.installationID()
	if len(old) != 32 {
		t.Fatal("identity missing")
	}
	err = ResetIdentity(r.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if id := r.installationID(); id == old || len(id) != 32 {
		t.Fatal("running reporter did not notice reset")
	}
	err = r.Feedback("problems", "freedom")
	if err != nil {
		t.Fatal(err)
	}
	report := reportAt(t, transport, 1)
	if report.Feedback != "problems" || report.ConfirmedProvider != "freedom" || report.Revision != 0 || !report.LastAppliedChange.IsZero() {
		t.Fatal("reset retained history or feedback lost")
	}
	err = r.Feedback("arbitrary private text", "")
	if err == nil {
		t.Fatal("free text accepted")
	}
	err = r.Feedback("working", "private-customer")
	if err == nil {
		t.Fatal("arbitrary provider accepted")
	}
}

func TestResearchRejectsSymlinks(t *testing.T) {
	r, transport := researchReporter(t)
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
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

func TestNetworkLookupIsBoundedAndUsesPTR(t *testing.T) {
	for _, body := range []string{"11.22.33.44", "192.168.1.1", strings.Repeat("1", 66)} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
		called := false
		result := lookupNetwork(context.Background(), server.Client(), server.URL, func(_ context.Context, ip string) ([]string, error) {
			called = true
			if ip != "11.22.33.44" {
				t.Fatal("wrong PTR address")
			}
			return []string{"customer.kpn.net."}, nil
		})
		server.Close()
		if body == "11.22.33.44" {
			if !called || result.Provider != "kpn" || result.Method != "ptr-suffix" {
				t.Fatal("missing provider hint")
			}
		} else if called || result.IP != "" {
			t.Fatal("invalid external-IP response accepted")
		}
	}
	for _, name := range []string{"notkpn.net", "kpn.net.attacker.invalid", "customer\n.kpn.net"} {
		result := cleanIdentity(NetworkIdentity{IP: "11.22.33.44", PTR: name})
		if result.Provider != "unknown" {
			t.Fatal("unsafe PTR matching")
		}
	}
}
