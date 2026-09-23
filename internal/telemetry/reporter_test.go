package telemetry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/getsentry/sentry-go/attribute"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/config/configtest"
)

var (
	errSecret          = errors.New("secret")
	errLeakyPayload    = errors.New("password=supersecret 192.0.2.55 /home/private-router/config.json")
	errOperationFailed = errors.New("failure")
	errPrivateFailure  = errors.New("private failure")
)

type recordingTransport struct {
	mu         sync.Mutex
	events     []*sentry.Event
	configured bool
}

func (t *recordingTransport) Configure(sentry.ClientOptions) { t.configured = true }
func (t *recordingTransport) SendEvent(event *sentry.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, event)
}
func (*recordingTransport) Flush(time.Duration) bool              { return true }
func (*recordingTransport) FlushWithContext(context.Context) bool { return true }
func (*recordingTransport) Close()                                {}

func newTestReporter(settings config.Telemetry, transport sentry.Transport) (*Reporter, error) {
	return newReporter(settings, "test", transport, "https://public@example.invalid/1")
}

func testSettings() config.Telemetry {
	settings := configtest.Custom().Telemetry
	settings.Enabled, settings.TraceRate = true, 1

	return settings
}

func newRecordingReporter(t *testing.T, settings config.Telemetry) (*Reporter, *recordingTransport) {
	t.Helper()
	transport := &recordingTransport{}
	r, err := newTestReporter(settings, transport)
	if err != nil {
		t.Fatal(err)
	}

	return r, transport
}

func assertEqual[T comparable](t *testing.T, name string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}

func assertNoSecrets(t *testing.T, data string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if strings.Contains(data, secret) {
			t.Fatalf("leaked %q: %s", secret, data)
		}
	}
}

type productCounts struct {
	failures int
	traces   int
	logs     int
	metrics  int
}

func countProducts(events []*sentry.Event) productCounts {
	var counts productCounts
	for _, event := range events {
		if len(event.Exception) > 0 {
			counts.failures++
		}
		if event.Type == "transaction" {
			counts.traces++
		}
		counts.logs += len(event.Logs)
		counts.metrics += len(event.Metrics)
	}

	return counts
}

func failureEvents(events []*sentry.Event) []*sentry.Event {
	var result []*sentry.Event
	for _, event := range events {
		if len(event.Exception) > 0 {
			result = append(result, event)
		}
	}

	return result
}

func countNamedMetric(events []*sentry.Event, name string) int {
	var total int
	for _, event := range events {
		for _, metric := range event.Metrics {
			if metric.Name == name {
				total++
			}
		}
	}

	return total
}

func countErrorLogs(events []*sentry.Event) int {
	var total int
	for _, event := range events {
		for _, log := range event.Logs {
			if log.Level == sentry.LogLevelError {
				total++
			}
		}
	}

	return total
}

func TestMissingBuildEndpointDoesNotUseEnvironment(t *testing.T) {
	t.Setenv("SENTRY_DSN", "https://public@example.invalid/2")
	transport := &recordingTransport{}
	if _, err := newReporter(testSettings(), "test", transport, ""); err == nil || transport.configured {
		t.Fatal("unstamped build initialized telemetry")
	}
	if r, err := newReporter(config.Telemetry{}, "test", transport, ""); err != nil || r.client != nil {
		t.Fatal("disabled telemetry required a build endpoint")
	}
}

func TestBuildEndpoint(t *testing.T) {
	t.Setenv("SENTRY_DSN", "https://runtime@example.invalid/2")
	r, err := New(testSettings(), "test", "", "")
	if DSN == "" {
		if err == nil {
			t.Fatal("unstamped build accepted telemetry")
		}

		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.client.Options().Dsn != DSN {
		t.Fatal("build endpoint was not used")
	}
}

func assertPrivacyFilters(t *testing.T, options sentry.ClientOptions) {
	t.Helper()
	if options.BeforeSend == nil {
		t.Fatal("errors are missing their privacy filter")
	}
	if options.BeforeSendTransaction == nil {
		t.Fatal("traces are missing their privacy filter")
	}
	if options.BeforeSendLog == nil {
		t.Fatal("logs are missing their privacy filter")
	}
	if options.BeforeSendMetric == nil {
		t.Fatal("metrics are missing their privacy filter")
	}
	if options.TracesSampler != nil {
		t.Fatal("SDK ignored configured fixed sampling rate")
	}
}

func TestEnvironmentFor(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"5.0.0-preview.1": envPreview,
		"5.0.0-rc.1":      envPreview,
		"4.3.1":           envProduction,
		"dev":             envDevelopment,
		"":                envDevelopment,
		"0.0.0-SNAPSHOT":  envDevelopment,
		"5.0.0-next":      envDevelopment,
		"5.0.0-arch1":     envProduction,
		"5.0.0-source":    envProduction,
		"5.0.0-rc1":       envPreview,
		"5.0.0-beta.2+b1": envPreview,
	}
	for version, want := range cases {
		if got := environmentFor(version); got != want {
			t.Errorf("environmentFor(%q) = %q, want %q", version, got, want)
		}
	}
	r, _ := newTestReporter(testSettings(), &recordingTransport{})
	if r.environment != envProduction {
		t.Fatalf("test reporter environment = %q", r.environment)
	}
	preview, err := newReporter(testSettings(), "5.0.0-preview.1", &recordingTransport{}, "https://public@example.invalid/1")
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "preview environment", preview.environment, envPreview)
	assertEqual(t, "preview client environment", preview.client.Options().Environment, envPreview)
}

func TestSDKConfigurationPreservesPrivacyAndSampling(t *testing.T) {
	settings := testSettings()
	settings.TraceRate = 0.2
	r, _ := newRecordingReporter(t, settings)
	defer r.Close()
	options := r.client.Options()
	collection := r.client.GetDataCollection()
	assertEqual(t, "personal data collection", collection.UserInfo.Or(true), false)
	assertEqual(t, "debug output", options.Debug, false)
	assertEqual(t, "collected HTTP bodies", len(collection.HTTPBodies), 0)
	assertEqual(t, "cookie collection", collection.Cookies.Mode, sentry.CollectionOff)
	assertEqual(t, "query parameter collection", collection.QueryParams.Mode, sentry.CollectionOff)
	assertEqual(t, "request header collection", collection.HTTPHeaders.Request.Mode, sentry.CollectionOff)
	assertEqual(t, "response header collection", collection.HTTPHeaders.Response.Mode, sentry.CollectionOff)
	assertEqual(t, "tracing", options.EnableTracing, true)
	assertEqual(t, "traces sample rate", options.TracesSampleRate, 0.2)
	assertEqual(t, "release", options.Release, "udm-iptv@test")
	stamp := vcsStamp(debug.ReadBuildInfo())
	assertEqual(t, "dist", options.Dist, stamp.revision)
	assertEqual(t, "environment", options.Environment, "production")
	assertEqual(t, "server name", options.ServerName, "udm-iptv")
	assertPrivacyFilters(t, options)
}

func TestDisabledDoesNotInitializeSDK(t *testing.T) {
	t.Setenv("SENTRY_DSN", DSN)
	transport := &recordingTransport{}
	settings := configtest.Custom().Telemetry
	settings.Enabled = false
	r, err := newTestReporter(settings, transport)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	_ = r.Run(context.Background(), "install", func(context.Context) error {
		called = true

		return errSecret
	})
	r.Gauge(context.Background(), "daemon.uptime", 1)
	r.Close()
	if !called || transport.configured || len(transport.events) != 0 {
		t.Fatal("disabled telemetry initialized SDK or changed execution")
	}
}

type exceptionRelations struct {
	groups  int
	parents int
}

func relationsOf(t *testing.T, exceptions []sentry.Exception) exceptionRelations {
	t.Helper()
	ids := map[int]bool{}
	for _, exception := range exceptions {
		if exception.Mechanism == nil {
			t.Fatal("missing relationship")
		}
		ids[exception.Mechanism.ExceptionID] = true
	}
	var result exceptionRelations
	for _, exception := range exceptions {
		if exception.Mechanism.IsExceptionGroup {
			result.groups++
		}
		parent := exception.Mechanism.ParentID
		if parent == nil {
			continue
		}
		result.parents++
		if !ids[*parent] {
			t.Fatal("dangling parent")
		}
	}

	return result
}

func TestWrappedAndJoinedErrorsPreserveRelationships(t *testing.T) {
	r, transport := newRecordingReporter(t, testSettings())
	cause := fmt.Errorf("wrapper: %w", errors.Join(errOperationFailed, &os.PathError{Op: "open", Path: "/data/udm-iptv/config.json", Err: os.ErrPermission}))
	if err := r.Run(context.Background(), "install", func(context.Context) error { return cause }); !errors.Is(err, cause) {
		t.Fatal("local error changed")
	}
	r.Close()
	failures := failureEvents(transport.events)
	assertEqual(t, "grouped events", len(failures), 1)
	assertEqual(t, "error chain length", len(failures[0].Exception), 5)
	assertEqual(t, "exception relationships", relationsOf(t, failures[0].Exception), exceptionRelations{groups: 1, parents: 4})
}

func TestSetMetadataPreservesPrintableEvidence(t *testing.T) {
	t.Parallel()
	r, _ := newRecordingReporter(t, testSettings())
	r.SetMetadata("UDR7", "5.1.31", "UDMPRO.al324.v5.1.31.5acc35d.260819.1714", "0xea15", "improxy", "kpn")
	assertEqual(t, "model", r.metadata["model"], "UDR7")
	assertEqual(t, "firmware", r.metadata["firmware"], "5.1.31")
	assertEqual(t, "firmware_discovery", r.metadata["firmware_discovery"], "UDMPRO.al324.v5.1.31.5acc35d.260819.1714")
	assertEqual(t, "sysid", r.metadata["sysid"], "ea15")
	r.SetMetadata("UDM-Pro", "latest", "UDMPRO.al324.v5.1.31.5acc35d.260819.1714 extra", "78:45:58:f8:ed:4f", "dnsmasq", "nope")
	if len(r.metadata) != 6 {
		t.Fatalf("printable metadata missing: %+v", r.metadata)
	}
	r.SetMetadata("bad\nboard", "", "", "", "", "")
	if len(r.metadata) != 0 {
		t.Fatalf("control characters kept: %+v", r.metadata)
	}
}

// A package record that trails the executable has to be visible on every
// report, so the tag names the kind of installation and the recorded version.
func TestSetInstallationTagsThePackageRecord(t *testing.T) {
	t.Parallel()
	r, _ := newRecordingReporter(t, testSettings())
	running := "5.0.0-preview.2"
	r.release = "udm-iptv@" + running
	if r.SetInstallation("") {
		t.Fatal("a standalone installation reported as stale")
	}
	assertEqual(t, "standalone", r.eventTags()["installation"], installationStandalone)
	if _, tagged := r.eventTags()["package_version"]; tagged {
		t.Fatal("a standalone installation carries a package version")
	}
	if r.SetInstallation(running) {
		t.Fatal("a matching record reported as stale")
	}
	assertEqual(t, "package", r.eventTags()["installation"], installationPackage)
	assertEqual(t, "package version", r.eventTags()["package_version"], running)
	if !r.SetInstallation("5.0.0-preview.1") {
		t.Fatal("a trailing record was not reported as stale")
	}
	assertEqual(t, "stale", r.eventTags()["installation"], installationStale)
	assertEqual(t, "stale version", r.eventTags()["package_version"], "5.0.0-preview.1")
	if r.SetInstallation("5.0.0 && reboot") {
		t.Fatal("garbage reported as stale")
	}
	assertEqual(t, "garbage", r.eventTags()["installation"], installationPackage)
	if _, tagged := r.eventTags()["package_version"]; tagged {
		t.Fatal("garbage kept as a package version")
	}
	event := r.filterEvent(&sentry.Event{}, nil)
	assertEqual(t, "event tag", event.Tags["installation"], installationPackage)
}

func TestWarnRecordsAWarningLog(t *testing.T) {
	r, transport := newRecordingReporter(t, testSettings())
	r.Warn(context.Background(), "dpkg records udm-iptv 5.0.0-preview.1 while 5.0.0-preview.2 runs")
	r.Close()
	transport.mu.Lock()
	defer transport.mu.Unlock()
	for _, event := range transport.events {
		for _, log := range event.Logs {
			if log.Body == "dpkg records udm-iptv 5.0.0-preview.1 while 5.0.0-preview.2 runs" && log.Level == sentry.LogLevelWarn {
				return
			}
		}
	}
	t.Fatal("warning log not delivered")
}

func TestAllProductsDelivered(t *testing.T) {
	r, transport := newRecordingReporter(t, testSettings())
	r.SetMetadata("UDMPRO", "5.1.31", "UDMPRO.al324.v5.1.31.5acc35d.260819.1714", "ea15", "improxy", "kpn")
	if r.dist != "" && r.eventTags()["vcs.revision"] != r.dist {
		t.Fatal("device metadata replaced the build revision")
	}
	err := r.Run(context.Background(), "install", func(context.Context) error { return errLeakyPayload })
	if err == nil || err.Error() != errLeakyPayload.Error() {
		t.Fatal("local error was changed")
	}
	r.Gauge(context.Background(), "daemon.uptime", 12)
	r.Close()
	transport.mu.Lock()
	defer transport.mu.Unlock()
	failures := failureEvents(transport.events)
	assertEqual(t, "failures", len(failures), 1)
	assertEqual(t, "exception value", failures[0].Exception[0].Value, errLeakyPayload.Error())
	assertEqual(t, "delivered products", countProducts(transport.events), productCounts{failures: 1, traces: 1, logs: 2, metrics: 4})
}

func noisyEvent() *sentry.Event {
	event := &sentry.Event{Transaction: "install", Message: "lease bound on eth8.4", User: sentry.User{IPAddress: "192.0.2.55"}}
	event.Contexts = map[string]sentry.Context{"device": {"arch": "arm64"}}
	event.Contexts["trace"] = sentry.Context{"trace_id": sentry.TraceID{1}, "span_id": sentry.SpanID{2}}
	event.Attachments = []*sentry.Attachment{{Filename: attachmentName, ContentType: "text/plain", Payload: []byte("Profile: kpn")}}
	event.Exception = []sentry.Exception{{Type: "error", Value: "open /data/udm-iptv/config.json: permission denied", Stacktrace: &sentry.Stacktrace{Frames: []sentry.Frame{{Filename: "config.go", Function: "config.Load", Lineno: 42}}}}}

	return event
}

func assertAttribute(t *testing.T, subject string, attributes map[string]attribute.Value, name, want string) {
	t.Helper()
	value, ok := attributes[name].AsInterface().(string)
	if !ok || value != want {
		t.Fatalf("%s attribute %s = %v, want %q", subject, name, attributes[name].AsInterface(), want)
	}
}

func assertEventFiltered(t *testing.T, r *Reporter) {
	t.Helper()
	clean := r.filterEvent(noisyEvent(), nil)
	if clean == nil {
		t.Fatal("event dropped")
	}
	assertEqual(t, "exception message", clean.Exception[0].Value, "open /data/udm-iptv/config.json: permission denied")
	assertEqual(t, "message", clean.Message, "lease bound on eth8.4")
	assertEqual(t, "environment", clean.Environment, "production")
	assertEqual(t, "dist", clean.Dist, r.dist)
	if r.dist != "" {
		assertEqual(t, "vcs.revision", clean.Tags["vcs.revision"], r.dist)
	}
	assertEqual(t, "user", clean.User.ID, r.installationID())
	assertEqual(t, "attachments", len(clean.Attachments), 1)
	assertEqual(t, "device context", fmt.Sprint(clean.Contexts["device"]["arch"]), "arm64")
	assertEqual(t, "frame line", clean.Exception[0].Stacktrace.Frames[0].Lineno, 42)
	if r.filterEvent(&sentry.Event{Transaction: researchTransaction}, nil) != nil {
		t.Fatal("research event sent as an issue")
	}
}

func assertLogsFiltered(t *testing.T, r *Reporter) {
	t.Helper()
	log := r.filterLog(&sentry.Log{Body: "lease 192.0.2.55 on eth8.4", Attributes: map[string]attribute.Value{"source": attribute.StringValue("udhcpc")}})
	if log == nil {
		t.Fatal("log dropped")
	}
	assertEqual(t, "body", log.Body, "lease 192.0.2.55 on eth8.4")
	assertAttribute(t, "log", log.Attributes, "source", "udhcpc")
	environment, _ := log.Attributes["sentry.environment"].AsInterface().(string)
	assertEqual(t, "log environment", environment, r.environment)
}

func assertMetricsFiltered(t *testing.T, r *Reporter) {
	t.Helper()
	wizard := r.filterMetric(&sentry.Metric{Name: "wizard.event", Attributes: map[string]attribute.Value{"event": attribute.StringValue("help"), "question": attribute.StringValue("vlan")}})
	if wizard == nil {
		t.Fatal("wizard metric dropped")
	}
	assertAttribute(t, "wizard metric", wizard.Attributes, "event", "help")
	assertAttribute(t, "wizard metric", wizard.Attributes, "question", "vlan")
	release, _ := wizard.Attributes["sentry.release"].AsInterface().(string)
	assertEqual(t, "metric release", release, r.release)
}

func TestFiltersStampReleaseAndInstallation(t *testing.T) {
	r, _ := newRecordingReporter(t, testSettings())
	defer r.Close()
	assertEventFiltered(t, r)
	assertLogsFiltered(t, r)
	assertMetricsFiltered(t, r)
}

func assertOnlyProduct(t *testing.T, settings config.Telemetry, product string) {
	t.Helper()
	r, transport := newRecordingReporter(t, settings)
	_ = r.Run(context.Background(), "install", func(context.Context) error { return errOperationFailed })
	r.Close()
	counts := countProducts(transport.events)
	for name, total := range map[string]int{"errors": counts.failures, "logs": counts.logs, "metrics": counts.metrics, "tracing": counts.traces} {
		if name == product {
			if total == 0 {
				t.Fatalf("enabled product %s sent nothing", name)
			}

			continue
		}
		assertEqual(t, "records for disabled product "+name, total, 0)
	}
}

func TestProductSwitches(t *testing.T) {
	for _, test := range []struct {
		name     string
		settings config.Telemetry
	}{
		{"errors", config.Telemetry{Enabled: true, TraceRate: 1, Errors: true}},
		{"logs", config.Telemetry{Enabled: true, TraceRate: 1, Logs: true}},
		{"metrics", config.Telemetry{Enabled: true, TraceRate: 1, Metrics: true}},
		{"tracing", config.Telemetry{Enabled: true, TraceRate: 1, Tracing: true}},
	} {
		t.Run(test.name, func(t *testing.T) { assertOnlyProduct(t, test.settings, test.name) })
	}
}

func TestPersistentLimitsAndRevokedConsent(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	for range 5 {
		if allowPersistedReason(dir, "errors", 5, now) != nil {
			t.Fatal("budget exhausted early")
		}
	}
	assertEqual(t, "budget after exhaustion", allowPersistedReason(dir, "errors", 5, now) == nil, false)
	assertEqual(t, "budget after clock rollback", allowPersistedReason(dir, "errors", 5, now.Add(-time.Hour)) == nil, false)
	assertEqual(t, "budget in the next window", allowPersistedReason(dir, "errors", 5, now.Add(time.Minute)) == nil, true)
	path := filepath.Join(dir, "config.json")
	value := configtest.Custom()
	value.Telemetry = testSettings()
	if err := config.Save(path, value); err != nil {
		t.Fatal(err)
	}
	r, _ := newRecordingReporter(t, value.Telemetry)
	defer r.Close()
	r.configPath = path
	value.Telemetry.Enabled = false
	if err := config.Save(path, value); err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "reporting after revoked consent", r.allow("logs", 30), false)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "reporting with unreadable consent", r.allow("metrics", 60), false)
}

func TestNestedTracesPreserveChildTimings(t *testing.T) {
	transport := &recordingTransport{}
	r, err := newTestReporter(testSettings(), transport)
	if err != nil {
		t.Fatal(err)
	}
	err = r.Run(context.Background(), "install", func(ctx context.Context) error {
		return r.Run(ctx, "service.health", func(context.Context) error { return nil })
	})
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	var traces int
	for _, event := range transport.events {
		if event.Type != "transaction" {
			continue
		}
		traces++
		if event.Transaction != "install" || len(event.Spans) != 1 || event.Spans[0].Op != "service.health" {
			t.Fatalf("nested timing lost: %+v", event)
		}
	}
	if traces != 1 {
		t.Fatalf("expected one trace with a child, got %d", traces)
	}
}

func runPanickingOperation(t *testing.T, r *Reporter) {
	t.Helper()
	defer func() {
		if recover() != "secret panic" {
			t.Error("panic changed or swallowed")
		}
	}()
	_ = r.Run(context.Background(), "daemon", func(context.Context) error { panic("secret panic") })
}

func assertUnhandledPanic(t *testing.T, event *sentry.Event) {
	t.Helper()
	mechanism := event.Exception[0].Mechanism
	if mechanism == nil || mechanism.Handled == nil || *mechanism.Handled {
		t.Fatal("panic was not marked unhandled")
	}
}

func TestPanicIsReportedWithoutSwallowingOrLosingValue(t *testing.T) {
	r, transport := newRecordingReporter(t, testSettings())
	runPanickingOperation(t, r)
	r.Close()
	failures := failureEvents(transport.events)
	assertEqual(t, "panic reports", len(failures), 1)
	assertUnhandledPanic(t, failures[0])
	assertEqual(t, "panic value", failures[0].Exception[0].Value, "secret panic")
	assertEqual(t, "panic type", failures[0].Exception[0].Type, "panic(string)")
}

func TestZeroTraceRateSendsNoTraces(t *testing.T) {
	settings := testSettings()
	settings.TraceRate = 0
	transport := &recordingTransport{}
	r, err := newTestReporter(settings, transport)
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Run(context.Background(), "install", func(context.Context) error { return nil })
	r.Close()
	for _, event := range transport.events {
		if event.Type == "transaction" {
			t.Fatal("zero sampling rate sent a trace")
		}
	}
}

func correlatedFailures(t *testing.T, event *sentry.Event, spans map[string]*sentry.Span) int {
	t.Helper()
	if len(event.Exception) == 0 {
		return 0
	}
	span := spans[event.Transaction]
	trace := event.Contexts["trace"]
	if span == nil || trace["trace_id"] != span.TraceID || trace["span_id"] != span.SpanID {
		t.Fatalf("error lost its active span: %+v", trace)
	}

	return 1
}

func correlatedLogs(t *testing.T, event *sentry.Event, spans map[string]*sentry.Span) int {
	t.Helper()
	for _, log := range event.Logs {
		span := spans[strings.Fields(log.Body)[0]]
		if span == nil || log.TraceID != span.TraceID || log.SpanID != span.SpanID {
			t.Fatalf("log lost its active span: %+v", log)
		}
	}

	return len(event.Logs)
}

func TestErrorsAndLogsKeepActiveSpan(t *testing.T) {
	r, transport := newRecordingReporter(t, testSettings())
	spans := map[string]*sentry.Span{}
	_ = r.Run(context.Background(), "install", func(ctx context.Context) error {
		spans["install"] = sentry.SpanFromContext(ctx)

		return r.Run(ctx, "service.health", func(ctx context.Context) error {
			spans["service.health"] = sentry.SpanFromContext(ctx)

			return errPrivateFailure
		})
	})
	r.Close()
	failures, logs := 0, 0
	for _, event := range transport.events {
		failures += correlatedFailures(t, event, spans)
		logs += correlatedLogs(t, event, spans)
	}
	assertEqual(t, "correlated errors", failures, 1)
	assertEqual(t, "correlated logs", logs, 4)
}

type outcomeCase struct {
	name      string
	err       error
	status    sentry.SpanStatus
	failures  int
	errorLogs int
}

func assertTraceStatus(t *testing.T, events []*sentry.Event, status sentry.SpanStatus) {
	t.Helper()
	for _, event := range events {
		if event.Type != "transaction" {
			continue
		}
		if event.Contexts["trace"]["status"] != status {
			t.Fatalf("wrong status: %+v", event.Contexts["trace"])
		}
	}
}

func assertOutcomeSignals(t *testing.T, test outcomeCase) {
	t.Helper()
	r, transport := newRecordingReporter(t, testSettings())
	if err := r.Run(context.Background(), "install", func(context.Context) error { return test.err }); !errors.Is(err, test.err) {
		t.Fatal("operation result changed")
	}
	r.Close()
	counts := countProducts(transport.events)
	assertEqual(t, "traces", counts.traces, 1)
	assertEqual(t, "errors", counts.failures, test.failures)
	assertEqual(t, "failed-operation metrics", countNamedMetric(transport.events, "operation.failed"), test.failures)
	assertEqual(t, "error logs", countErrorLogs(transport.events), test.errorLogs)
	assertTraceStatus(t, transport.events, test.status)
}

func TestCancellationAndDeadlineTraceStatus(t *testing.T) {
	for _, test := range []outcomeCase{
		{name: "cancelled", err: context.Canceled, status: sentry.SpanStatusCanceled, failures: 0, errorLogs: 0},
		{name: "deadline", err: context.DeadlineExceeded, status: sentry.SpanStatusDeadlineExceeded, failures: 1, errorLogs: 1},
	} {
		t.Run(test.name, func(t *testing.T) { assertOutcomeSignals(t, test) })
	}
}

func TestFailuresShareTheOperationTrace(t *testing.T) {
	r, transport := newRecordingReporter(t, testSettings())
	_ = r.Run(context.Background(), "install", func(ctx context.Context) error {
		return r.Run(ctx, "service.health", func(context.Context) error { return errOperationFailed })
	})
	r.client.Flush(time.Second)
	traces := map[string]int{}
	transactions, failures := 0, 0
	for _, event := range transport.events {
		switch {
		case event.Type == "transaction":
			transactions++
		case len(event.Exception) > 0:
			failures++
		default:
			continue
		}
		traces[fmt.Sprint(event.Contexts["trace"]["trace_id"])]++
	}
	assertEqual(t, "transactions", transactions, 1)
	assertEqual(t, "failures", failures, 1)
	if len(traces) != 1 {
		t.Fatalf("failures left the operation trace: %v", traces)
	}
}

func TestFailureCarriesOperationTrailAndDiagnostics(t *testing.T) {
	r, transport := newRecordingReporter(t, testSettings())
	r.hub.AddBreadcrumb(&sentry.Breadcrumb{Category: "http", Message: "GET https://api.github.com/repos/kjanat/udm-iptv/releases/latest"}, nil)
	_ = r.Run(context.Background(), "install", func(ctx context.Context) error {
		return r.Run(ctx, "service.health", func(ctx context.Context) error {
			Attach(ctx, []byte("=== udm-iptv failure diagnostics ===\nProfile: kpn\n"))

			return errOperationFailed
		})
	})
	r.client.Flush(time.Second)
	failures := failureEvents(transport.events)
	assertEqual(t, "failures", len(failures), 1)
	trails := map[string][]string{}
	for _, event := range failures {
		assertEqual(t, event.Transaction+" attachments", len(event.Attachments), 1)
		assertEqual(t, event.Transaction+" attachment name", event.Attachments[0].Filename, attachmentName)
		assertEqual(t, event.Transaction+" attachment type", event.Attachments[0].ContentType, "text/plain")
		for _, crumb := range event.Breadcrumbs {
			trails[event.Transaction] = append(trails[event.Transaction], crumb.Message)
		}
	}
	request := "GET https://api.github.com/repos/kjanat/udm-iptv/releases/latest"
	if want := []string{request, "install started", "service.health started", "service.health failed"}; !slices.Equal(trails["service.health"], want) {
		t.Fatalf("service.health trail = %q, want %q", trails["service.health"], want)
	}
}

func TestFailureCarriesRuntimeContextsAndModules(t *testing.T) {
	r, transport := newRecordingReporter(t, testSettings())
	_ = r.Run(context.Background(), "install", func(context.Context) error { return errOperationFailed })
	r.client.Flush(time.Second)
	failures := failureEvents(transport.events)
	assertEqual(t, "failures", len(failures), 1)
	event := failures[0]
	assertEqual(t, "device arch", fmt.Sprint(event.Contexts["device"]["arch"]), runtime.GOARCH)
	assertEqual(t, "os name", fmt.Sprint(event.Contexts["os"]["name"]), runtime.GOOS)
	assertEqual(t, "runtime version", fmt.Sprint(event.Contexts["runtime"]["version"]), runtime.Version())
	if _, ok := event.Contexts["trace"]; !ok {
		t.Fatal("trace context dropped")
	}
	if len(event.Modules) == 0 {
		t.Fatal("module list dropped")
	}
	if _, ok := event.Modules["github.com/getsentry/sentry-go"]; !ok {
		t.Fatalf("module list lacks the SDK: %v", event.Modules)
	}
}

func TestInstallationStepsBecomeChildSpans(t *testing.T) {
	r, transport := newRecordingReporter(t, testSettings())
	_ = r.Run(context.Background(), "install", func(ctx context.Context) error {
		stepCtx, finish := Step(ctx, "Install persistent executable")
		_, nested := Step(stepCtx, "Write /data/udm-iptv/config.json")
		nested(nil)
		finish(nil)

		return nil
	})
	r.client.Flush(time.Second)
	var transactions []*sentry.Event
	for _, event := range transport.events {
		if event.Type == "transaction" {
			transactions = append(transactions, event)
		}
	}
	assertEqual(t, "transactions", len(transactions), 1)
	assertEqual(t, "child spans", len(transactions[0].Spans), 2)
	descriptions := make([]string, 0, len(transactions[0].Spans))
	for _, span := range transactions[0].Spans {
		assertEqual(t, "span op", span.Op, stepOp)
		descriptions = append(descriptions, span.Description)
	}
	slices.Sort(descriptions)
	if want := []string{"Install persistent executable", "Write /data/udm-iptv/config.json"}; !slices.Equal(descriptions, want) {
		t.Fatalf("steps = %q, want %q", descriptions, want)
	}
}

func TestLineWriterLogsCompleteLines(t *testing.T) {
	r, transport := newRecordingReporter(t, testSettings())
	writer := r.LineWriter(context.Background(), "proxy")
	if _, err := writer.Write([]byte("joined 239.1.1.1 on eth8.4\npart")); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("ial line\n")); err != nil {
		t.Fatal(err)
	}
	r.client.Flush(time.Second)
	var bodies []string
	for _, event := range transport.events {
		for _, log := range event.Logs {
			bodies = append(bodies, log.Body)
			assertAttribute(t, "line", log.Attributes, "source", "proxy")
		}
	}
	if want := []string{"joined 239.1.1.1 on eth8.4", "partial line"}; !slices.Equal(bodies, want) {
		t.Fatalf("lines = %q, want %q", bodies, want)
	}
	assertEqual(t, "disabled writer", r.LineWriter(context.Background(), "x") == io.Discard, false)
	off, _ := newRecordingReporter(t, config.Telemetry{Enabled: true, Errors: true, TraceRate: 1})
	assertEqual(t, "writer without logs", off.LineWriter(context.Background(), "proxy") == io.Discard, true)
}
