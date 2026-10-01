package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/config/configtest"
)

func researchReporter(t *testing.T) (*Reporter, *recordingTransport) {
	t.Helper()
	r, transport := newRecordingReporter(t, testSettings())
	r.stateDir = t.TempDir()
	t.Cleanup(r.Close)

	return r, transport
}

func TestObservationCheckInNamesTheInstallation(t *testing.T) {
	r, transport := researchReporter(t)
	if err := r.RecordObservation(context.Background(), Observation{Active: true}); err != nil {
		t.Fatal(err)
	}
	r.ObservationCheckIn(true)
	r.ObservationCheckIn(false)
	r.client.Flush(time.Second)
	var checkIns []*sentry.Event
	for _, event := range transport.events {
		if event.CheckIn != nil {
			checkIns = append(checkIns, event)
		}
	}
	assertEqual(t, "check-ins", len(checkIns), 2)
	id := r.installationID()
	assertEqual(t, "monitor", checkIns[0].CheckIn.MonitorSlug, "observation-"+id[:12])
	assertEqual(t, "healthy status", checkIns[0].CheckIn.Status, sentry.CheckInStatusOK)
	assertEqual(t, "unhealthy status", checkIns[1].CheckIn.Status, sentry.CheckInStatusError)
	if checkIns[0].MonitorConfig == nil || checkIns[0].MonitorConfig.CheckInMargin != 15 || checkIns[0].MonitorConfig.FailureIssueThreshold != 2 {
		t.Fatalf("monitor config = %+v", checkIns[0].MonitorConfig)
	}
	silent, _ := newRecordingReporter(t, testSettings())
	silent.ObservationCheckIn(true)
}

func reportAt(t *testing.T, transport *recordingTransport, index int) researchReport {
	t.Helper()
	var reports []researchReport
	for _, event := range transport.events {
		if event.Transaction == researchTransaction {
			t.Fatal("research sent as an issue")
		}
		for _, log := range event.Logs {
			if !researchLog(log.Body) {
				continue
			}
			raw, ok := log.Attributes["research.report"]
			if !ok {
				t.Fatal("missing research.report attribute")
			}
			text, _ := raw.AsInterface().(string)
			var report researchReport
			if err := json.Unmarshal([]byte(text), &report); err != nil {
				t.Fatal(err)
			}
			if report.ChangedFields == nil {
				report.ChangedFields = []string{}
			}
			reports = append(reports, report)
		}
	}
	if len(reports) <= index {
		t.Fatalf("missing research log %d (have %d)", index, len(reports))
	}

	return reports[index]
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
			revision: 2, previousRevision: 1, appliedRevision: 1, changed: []string{"proxy.quickLeave"},
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
	value := configtest.Custom()
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

func TestConfigurationReportCarriesTheWholeConfiguration(t *testing.T) {
	r, transport := researchReporter(t)
	value := networkChoiceConfig()
	if err := r.RecordConfiguration(context.Background(), value, true, nil); err != nil {
		t.Fatal(err)
	}
	report := reportAt(t, transport, 0)
	if report.Settings == nil {
		t.Fatal("configuration missing")
	}
	assertEqual(t, "interface", report.Settings.WAN.Interface, "private0")
	assertEqual(t, "vlan mac", report.Settings.WAN.VLANMAC, "aa:bb:cc:dd:ee:ff")
	assertEqual(t, "static address", report.Settings.WAN.StaticAddress, "192.168.199.7/24")
	if !reflect.DeepEqual(report.Settings.WAN.DHCPOptions, []string{"-V", "private-token"}) {
		t.Fatalf("dhcp options = %v", report.Settings.WAN.DHCPOptions)
	}
	if !slices.Contains(report.Settings.WAN.NATDestinations, "192.168.199.0/24") {
		t.Fatalf("custom prefix dropped: %v", report.Settings.WAN.NATDestinations)
	}
	assertEqual(t, "telemetry preferences", report.Settings.Telemetry.Enabled, value.Telemetry.Enabled)
}

func TestResearchSeparateProcessAndLiveRevocation(t *testing.T) {
	r, transport := researchReporter(t)
	value := configtest.Custom()
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
	if err := r.RecordObservation(context.Background(), Observation{Active: true, UptimeSeconds: 3600}); err != nil {
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
	value := configtest.Custom()
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
	assertNoSecrets(t, string(data), "fingerprint")
	assertEqual(t, "lookup performed", called, test.network)
	assertEqual(t, "PTR name in report", strings.Contains(string(data), "Customer.KPN.NET."), test.network)
	report := reportAt(t, transport, 0)
	assertNetworkIdentity(t, report.Network, test)
}

func TestResearchNetworkChoice(t *testing.T) {
	for _, test := range []networkChoiceCase{
		{name: "off", network: false},
		{name: "on", network: true, provider: "kpn", method: "ptr-suffix", confidence: "low"},
	} {
		t.Run(test.name, func(t *testing.T) { runNetworkChoiceCase(t, test) })
	}
}

func TestResearchOptOutAndForgedEvents(t *testing.T) {
	r, transport := researchReporter(t)
	value := configtest.Custom()
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
		if err := r.Feedback(context.Background(), test.answer, test.provider); err == nil {
			t.Fatalf("%s accepted", test.name)
		}
	}
}

func TestResearchResetAndFeedback(t *testing.T) {
	r, transport := researchReporter(t)
	if err := r.RecordConfiguration(context.Background(), configtest.Custom(), true, nil); err != nil {
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
	if err := r.Feedback(context.Background(), "problems", "freedom"); err != nil {
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

func TestFeedbackReportsRateLimitedDrop(t *testing.T) {
	r, transport := researchReporter(t)
	for i := range presetsPerMinute {
		if err := r.Feedback(context.Background(), "working", "freedom"); err != nil {
			t.Fatalf("feedback %d: %v", i, err)
		}
	}
	if err := r.Feedback(context.Background(), "working", "freedom"); !errors.Is(err, errFeedbackNotQueued) {
		t.Fatalf("rate-limited feedback reported success: %v", err)
	}
	assertEqual(t, "events sent", len(transport.events), presetsPerMinute)
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
	if err := r.RecordConfiguration(context.Background(), configtest.Custom(), true, nil); err == nil {
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
	result := lookupNetwork(context.Background(), testSources(server, func(_ context.Context, ip string) ([]string, error) {
		called = true
		assertEqual(t, "PTR address", ip, "11.22.33.44")

		return []string{"customer.kpn.net."}, nil
	}))
	assertEqual(t, "PTR lookup performed", called, test.called)
	assertEqual(t, "IP source", result.IPSource, map[bool]string{true: ipSourceHTTPS, false: ""}[test.ip != ""])
	assertEqual(t, "public IP", result.IP, test.ip)
	assertEqual(t, "detected provider", result.Provider, test.provider)
	assertEqual(t, "detection method", result.Method, test.method)
}

const lookupTestBudget = 30 * time.Millisecond

var errNoEgress = errors.New("no default route")

const testUserAgent = "udm-iptv/5.0.0-test"

// testSources asks the server for the address and the given resolver for the
// name; the kernel has no route to offer.
func testSources(server *httptest.Server, ptr func(context.Context, string) ([]string, error)) lookupSources {
	return lookupSources{
		egress:    func() (netip.Addr, error) { return netip.Addr{}, errNoEgress },
		client:    server.Client(),
		endpoint:  server.URL,
		userAgent: testUserAgent,
		ptr:       ptr,
		catalog:   config.DefaultCatalog(),
		budget:    lookupTestBudgets,
	}
}

func blockingPTR(ctx context.Context, _ string) ([]string, error) {
	<-ctx.Done()

	return nil, fmt.Errorf("resolver gave up: %w", ctx.Err())
}

func noPTR(context.Context, string) ([]string, error) { return nil, nil }

var lookupTestBudgets = lookupBudget{https: lookupTestBudget, ptr: lookupTestBudget}

func failingServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("public IP requested although the WAN address was known")
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(server.Close)

	return server
}

func catalogAddress(t *testing.T, asn string) netip.Addr {
	t.Helper()
	networks, err := config.EmbeddedProviderNetworks()
	if err != nil {
		t.Fatal(err)
	}
	for _, network := range networks {
		if network.ASN == asn && network.Prefix.Addr().Is4() {
			return network.Prefix.Addr().Next()
		}
	}
	t.Fatalf("no IPv4 prefix for %s in the embedded table", asn)

	return netip.Addr{}
}

// A public WAN address inside a catalog provider's network answers the whole
// lookup locally.
func TestNetworkLookupUsesTheWANAddressAndItsNetwork(t *testing.T) {
	address := catalogAddress(t, "AS1136")
	sources := testSources(failingServer(t), blockingPTR)
	sources.egress = func() (netip.Addr, error) { return address, nil }
	result := lookupNetwork(context.Background(), sources)
	want := NetworkIdentity{
		IP: address.String(), IPSource: ipSourceWAN, ASN: "AS1136", ASNSource: asnSourceTable, Provider: "kpn", Method: "asn",
		Confidence: "medium", Status: "ip-and-asn", ObservedAt: result.ObservedAt,
	}
	if result != want {
		t.Fatalf("WAN lookup:\n got %+v\nwant %+v", result, want)
	}
}

// A private WAN address, behind another router or carrier NAT, falls back to
// the edge lookup, and an address outside every catalog network falls back
// to reverse DNS.
func TestNetworkLookupFallsBackFromAPrivateWANAddress(t *testing.T) {
	fast := publicIPServer(t, 0)
	sources := testSources(fast, noPTR)
	sources.egress = func() (netip.Addr, error) { return netip.MustParseAddr("192.168.1.1"), nil }
	result := lookupNetwork(context.Background(), sources)
	if result.IP != "11.22.33.44" || result.IPSource != ipSourceHTTPS || result.ASN != "" || result.Status != "ip-only" {
		t.Fatalf("private WAN fallback: %+v", result)
	}
}

// The edge names the network behind a private WAN address, so no reverse DNS
// lookup is needed; the request identifies the program by its user agent.
func TestNetworkLookupTakesTheASNFromTheEdge(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertEqual(t, "user agent", r.UserAgent(), testUserAgent)
		assertEqual(t, "accept", r.Header.Get("Accept"), "application/json")
		_, _ = w.Write([]byte(`{"ip":"11.22.33.44","asn":"AS1136","as_name":"KPN B.V.","country_code":"NL","provider":"kpn"}`))
	}))
	t.Cleanup(server.Close)
	result := lookupNetwork(context.Background(), testSources(server, blockingPTR))
	want := NetworkIdentity{
		IP: "11.22.33.44", IPSource: ipSourceHTTPS, ASN: "AS1136", ASNSource: asnSourceEdge, Provider: "kpn", Method: "asn",
		Confidence: "medium", Status: "ip-and-asn", HTTPSMillis: result.HTTPSMillis, ObservedAt: result.ObservedAt,
	}
	if result != want {
		t.Fatalf("edge lookup:\n got %+v\nwant %+v", result, want)
	}
}

// A public WAN address outside every built-in network still asks the edge,
// which may know the network from fresher data.
func TestNetworkLookupAsksTheEdgeForAnUnlistedWANAddress(t *testing.T) {
	server := edgeServer(t, `{"ip":"11.22.33.44","asn":"AS1136"}`)
	sources := testSources(server, blockingPTR)
	sources.egress = func() (netip.Addr, error) { return netip.MustParseAddr("11.22.33.44"), nil }
	result := lookupNetwork(context.Background(), sources)
	if result.IP != "11.22.33.44" || result.IPSource != ipSourceWAN || result.ASN != "AS1136" || result.ASNSource != asnSourceEdge || result.Provider != "kpn" {
		t.Fatalf("unlisted WAN address: %+v", result)
	}
}

// An ASN without a provider profile is still reported, and reverse DNS gets
// its turn at naming the provider.
func TestNetworkLookupKeepsAnUnknownASNAndTriesReverseDNS(t *testing.T) {
	server := edgeServer(t, `{"ip":"11.22.33.44","asn":"AS64496"}`)
	called := false
	result := lookupNetwork(context.Background(), testSources(server, func(context.Context, string) ([]string, error) {
		called = true

		return []string{"customer.kpn.net."}, nil
	}))
	if !called || result.ASN != "AS64496" || result.Status != "ip-and-asn" || result.PTR != "customer.kpn.net." || result.Provider != "kpn" || result.Method != "ptr-suffix" {
		t.Fatalf("unknown ASN: %+v", result)
	}
}

// When the edge is down, a public WAN address still gets its reverse DNS
// lookup, and both failures are reported.
func TestNetworkLookupSurvivesAnEdgeFailureWithAWANAddress(t *testing.T) {
	server := edgeServer(t, `{"ip":"11.22.33.44","asn":"AS1136"}`)
	server.Close()
	sources := testSources(server, blockingPTR)
	sources.egress = func() (netip.Addr, error) { return netip.MustParseAddr("11.22.33.44"), nil }
	result := lookupNetwork(context.Background(), sources)
	if result.IP != "11.22.33.44" || result.IPSource != ipSourceWAN || result.Status != "ip-only" {
		t.Fatalf("edge failure: %+v", result)
	}
	if !strings.HasPrefix(result.LookupError, "request edge identity:") || !strings.Contains(result.LookupError, "; reverse DNS lookup:") {
		t.Fatalf("edge failure error: %q", result.LookupError)
	}
}

func TestNetworkLookupRejectsAMalformedEdgeASN(t *testing.T) {
	server := edgeServer(t, `{"ip":"11.22.33.44","asn":"1136"}`)
	result := lookupNetwork(context.Background(), testSources(server, blockingPTR))
	if result.IP != "" || result.Status != "unavailable" || !strings.Contains(result.LookupError, `ASN "1136"`) {
		t.Fatalf("malformed ASN: %+v", result)
	}
}

func edgeServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
	t.Cleanup(server.Close)

	return server
}

func publicIPServer(t *testing.T, delay time.Duration) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(delay):
			_, _ = w.Write([]byte(`{"ip":"11.22.33.44","asn":null}`))
		}
	}))
	t.Cleanup(server.Close)

	return server
}

// Each phase has its own deadline and its own elapsed time.
func TestNetworkLookupSlowHTTPSLeavesTheDNSBudgetUntouched(t *testing.T) {
	slow := publicIPServer(t, 10*lookupTestBudget)
	result := lookupNetwork(context.Background(), testSources(slow, blockingPTR))
	if result.IP != "" || result.Status != "unavailable" || !strings.HasPrefix(result.LookupError, "request edge identity:") {
		t.Fatalf("slow HTTPS: %+v", result)
	}
	if result.HTTPSMillis < lookupTestBudget.Milliseconds() || result.PTRMillis != 0 {
		t.Fatalf("slow HTTPS timing: %+v", result)
	}
}

func TestNetworkLookupSlowResolverKeepsTheIP(t *testing.T) {
	fast := publicIPServer(t, 0)
	result := lookupNetwork(context.Background(), testSources(fast, blockingPTR))
	if result.IP != "11.22.33.44" || result.Status != "ip-only" || !strings.HasPrefix(result.LookupError, "reverse DNS lookup:") {
		t.Fatalf("slow resolver: %+v", result)
	}
	if result.PTRMillis < lookupTestBudget.Milliseconds() {
		t.Fatalf("slow resolver timing: %+v", result)
	}
}

func TestNetworkLookupWithoutAPTRRecordIsIPOnly(t *testing.T) {
	fast := publicIPServer(t, 0)
	result := lookupNetwork(context.Background(), testSources(fast, noPTR))
	if result.IP != "11.22.33.44" || result.Status != "ip-only" || result.LookupError != "" {
		t.Fatalf("no PTR record: %+v", result)
	}
}

func TestNetworkLookupReportsCallerCancellation(t *testing.T) {
	fast := publicIPServer(t, 0)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	result := lookupNetwork(cancelled, testSources(fast, blockingPTR))
	if result.IP != "" || !strings.HasPrefix(result.LookupError, "request edge identity:") || !strings.Contains(result.LookupError, context.Canceled.Error()) {
		t.Fatalf("cancelled caller: %+v", result)
	}
}

func TestNetworkLookupIsBoundedAndUsesPTR(t *testing.T) {
	for _, test := range []lookupCase{
		{name: "public address", body: `{"ip":"11.22.33.44"}`, called: true, ip: "11.22.33.44", provider: "kpn", method: "ptr-suffix"},
		{name: "private address", body: `{"ip":"192.168.1.1"}`, called: false, ip: "", provider: "unknown", method: "none"},
		{name: "oversized body", body: `{"ip":"11.22.33.44","as_name":"` + strings.Repeat("x", edgeBodyLimit) + `"}`, called: false, ip: "", provider: "unknown", method: "none"},
		{name: "not JSON", body: "11.22.33.44", called: false, ip: "", provider: "unknown", method: "none"},
	} {
		t.Run(test.name, func(t *testing.T) { runLookupCase(t, test) })
	}
	for _, name := range []string{"notkpn.net", "kpn.net.attacker.invalid", "customer\n.kpn.net"} {
		result := cleanIdentity(config.DefaultCatalog(), NetworkIdentity{IP: "11.22.33.44", PTR: name})
		assertEqual(t, "provider for "+name, result.Provider, "unknown")
	}
}
