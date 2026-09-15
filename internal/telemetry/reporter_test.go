package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/getsentry/sentry-go/attribute"
	"github.com/kjanat/udm-iptv/internal/config"
)

type recordingTransport struct {
	mu         sync.Mutex
	events     []*sentry.Event
	configured bool
}

func newTestReporter(settings config.Telemetry, version string, transport sentry.Transport) (*Reporter, error) {
	return newReporter(settings, version, transport, "https://public@example.invalid/1")
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

func (t *recordingTransport) Configure(sentry.ClientOptions) { t.configured = true }
func (t *recordingTransport) SendEvent(event *sentry.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, event)
}
func (*recordingTransport) Flush(time.Duration) bool              { return true }
func (*recordingTransport) FlushWithContext(context.Context) bool { return true }
func (*recordingTransport) Close()                                {}

func testSettings() config.Telemetry {
	settings := config.Default().Telemetry
	settings.Enabled, settings.TraceRate = true, 1
	return settings
}

func TestSDKConfigurationPreservesPrivacyAndSampling(t *testing.T) {
	settings := testSettings()
	settings.TraceRate = 0.2
	r, err := newTestReporter(settings, "test", &recordingTransport{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	options := r.client.Options()
	collection := r.client.GetDataCollection()
	if collection.UserInfo.Or(true) || options.Debug {
		t.Fatal("SDK enabled personal data collection or debug output")
	}
	if len(collection.HTTPBodies) != 0 || collection.Cookies.Mode != sentry.CollectionOff || collection.QueryParams.Mode != sentry.CollectionOff || collection.HTTPHeaders.Request.Mode != sentry.CollectionOff || collection.HTTPHeaders.Response.Mode != sentry.CollectionOff {
		t.Fatal("SDK enabled automatic HTTP data collection")
	}
	if !options.EnableTracing || options.TracesSampleRate != 0.2 || options.TracesSampler != nil {
		t.Fatal("SDK ignored configured fixed sampling rate")
	}
	if options.BeforeSend == nil || options.BeforeSendTransaction == nil || options.BeforeSendLog == nil || options.BeforeSendMetric == nil {
		t.Fatal("a telemetry product is missing its privacy filter")
	}
	if options.Release != "udm-iptv@test" || options.Environment != "production" || options.ServerName != "udm-iptv" {
		t.Fatal("SDK metadata defaults changed")
	}
}

func TestDisabledDoesNotInitializeSDK(t *testing.T) {
	t.Setenv("SENTRY_DSN", DSN)
	transport := &recordingTransport{}
	settings := config.Default().Telemetry
	settings.Enabled = false
	r, err := newTestReporter(settings, "test", transport)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	_ = r.Run(context.Background(), "install", func(context.Context) error { called = true; return errors.New("secret") })
	r.Gauge(context.Background(), "daemon.uptime", 1)
	r.Close()
	if !called || transport.configured || len(transport.events) != 0 {
		t.Fatal("disabled telemetry initialized SDK or changed execution")
	}
}

func TestWrappedAndJoinedErrorsPreserveRelationships(t *testing.T) {
	transport := &recordingTransport{}
	r, err := newTestReporter(testSettings(), "test", transport)
	if err != nil {
		t.Fatal(err)
	}
	cause := fmt.Errorf("secret wrapper: %w", errors.Join(errors.New("secret first"), &os.PathError{Op: "open", Path: "/secret/path", Err: os.ErrPermission}))
	if err := r.Run(context.Background(), "install", func(context.Context) error { return cause }); err != cause {
		t.Fatal("local error changed")
	}
	r.Close()
	var failures int
	for _, event := range transport.events {
		if len(event.Exception) == 0 {
			continue
		}
		failures++
		if len(event.Exception) != 5 {
			t.Fatalf("lost error chain: %+v", event.Exception)
		}
		ids := map[int]bool{}
		groups, parents := 0, 0
		for _, exception := range event.Exception {
			if exception.Mechanism == nil {
				t.Fatal("missing relationship")
			}
			ids[exception.Mechanism.ExceptionID] = true
			if exception.Mechanism.IsExceptionGroup {
				groups++
			}
		}
		for _, exception := range event.Exception {
			if parent := exception.Mechanism.ParentID; parent != nil {
				parents++
				if !ids[*parent] {
					t.Fatal("dangling parent")
				}
			}
		}
		if groups != 1 || parents != 4 {
			t.Fatalf("groups=%d parents=%d", groups, parents)
		}
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "secret") {
			t.Fatalf("private data leaked: %s", data)
		}
	}
	if failures != 1 {
		t.Fatalf("expected one grouped event, got %d", failures)
	}
}

func TestAllProductsAndPrivacy(t *testing.T) {
	transport := &recordingTransport{}
	r, err := newTestReporter(testSettings(), "test", transport)
	if err != nil {
		t.Fatal(err)
	}
	r.SetMetadata("UDMPRO", "5.1.31", "improxy", "kpn")
	secret := "password=supersecret 192.0.2.55 /home/private-router/config.json"
	err = r.Run(context.Background(), "install", func(context.Context) error { return errors.New(secret) })
	if err == nil || err.Error() != secret {
		t.Fatal("local error was changed")
	}
	r.Gauge(context.Background(), "daemon.uptime", 12)
	r.Close()
	transport.mu.Lock()
	defer transport.mu.Unlock()
	var errorsSeen, traces, logs, metrics int
	for _, event := range transport.events {
		if len(event.Exception) > 0 {
			errorsSeen++
		}
		if event.Type == "transaction" {
			traces++
		}
		logs += len(event.Logs)
		metrics += len(event.Metrics)
		for _, value := range []any{event, event.Logs, event.Metrics} {
			data, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"supersecret", "192.0.2.55", "private-router", "sentry.server.address", "server_name"} {
				if strings.Contains(string(data), forbidden) {
					t.Fatalf("leaked %q: %s", forbidden, data)
				}
			}
		}
	}
	if errorsSeen != 1 || traces != 1 || logs != 2 || metrics != 4 {
		t.Fatalf("missing products: errors=%d traces=%d logs=%d metrics=%d", errorsSeen, traces, logs, metrics)
	}
}

func TestFiltersDiscardUnknownData(t *testing.T) {
	r, err := newTestReporter(testSettings(), "test", &recordingTransport{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	event := &sentry.Event{Transaction: "install", ServerName: "secret-host", Message: "secret-message", User: sentry.User{IPAddress: "192.0.2.55"}}
	event.Contexts = map[string]sentry.Context{"device": {"secret": "secret-context"}}
	event.Contexts["trace"] = sentry.Context{"trace_id": sentry.TraceID{1}, "span_id": sentry.SpanID{2}, "secret": "secret-trace-data"}
	event.Attachments = []*sentry.Attachment{{Filename: "secret-file", Payload: []byte("secret-config")}}
	event.Exception = []sentry.Exception{{Type: "error", Value: "secret-error", Stacktrace: &sentry.Stacktrace{Frames: []sentry.Frame{{Filename: "/home/secret/file.go", AbsPath: "secret-path", Vars: map[string]any{"password": "secret"}, ContextLine: "secret-source", Function: "main.run", Lineno: 42}}}}}
	event.Exception[0].Mechanism = &sentry.Mechanism{Type: "secret", Description: "secret", Source: "secret", HelpLink: "secret", Data: map[string]any{"secret": "secret"}}
	clean := r.filterEvent(event, nil)
	if clean.Exception[0].Value != "install failed" {
		t.Fatalf("unexpected exception message: %q", clean.Exception[0].Value)
	}
	data, err := json.Marshal(clean)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret") || len(clean.Attachments) != 0 {
		t.Fatalf("unfiltered event: %s", data)
	}
	if clean.Exception[0].Stacktrace.Frames[0].Lineno != 42 {
		t.Fatal("lost diagnostic stack location")
	}
	log := r.filterLog(&sentry.Log{Body: "install completed", Attributes: map[string]attribute.Value{"secret": attribute.StringValue("secret")}})
	if _, exists := log.Attributes["secret"]; exists {
		t.Fatal("log attributes leaked")
	}
	if r.filterLog(&sentry.Log{Body: "password=secret"}) != nil {
		t.Fatal("raw log accepted")
	}
	if r.filterMetric(&sentry.Metric{Name: "secret.address"}) != nil {
		t.Fatal("unknown metric accepted")
	}
}

func TestProductSwitches(t *testing.T) {
	for _, product := range []string{"errors", "logs", "metrics", "tracing"} {
		t.Run(product, func(t *testing.T) {
			settings := config.Telemetry{Enabled: true, TraceRate: 1}
			switch product {
			case "errors":
				settings.Errors = true
			case "logs":
				settings.Logs = true
			case "metrics":
				settings.Metrics = true
			case "tracing":
				settings.Tracing = true
			}
			transport := &recordingTransport{}
			r, err := newTestReporter(settings, "test", transport)
			if err != nil {
				t.Fatal(err)
			}
			_ = r.Run(context.Background(), "install", func(context.Context) error { return errors.New("failure") })
			r.Close()
			for _, event := range transport.events {
				if (len(event.Exception) > 0 && product != "errors") || (len(event.Logs) > 0 && product != "logs") || (len(event.Metrics) > 0 && product != "metrics") || (event.Type == "transaction" && product != "tracing") {
					t.Fatalf("disabled product sent: %s", event.Type)
				}
			}
			if len(transport.events) == 0 {
				t.Fatal("enabled product missing")
			}
		})
	}
}

func TestPersistentLimitsAndRevokedConsent(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	for range 5 {
		if !allowPersisted(dir, "errors", 5, now) {
			t.Fatal("budget exhausted early")
		}
	}
	if allowPersisted(dir, "errors", 5, now) {
		t.Fatal("new process could bypass budget")
	}
	if allowPersisted(dir, "errors", 5, now.Add(-time.Hour)) {
		t.Fatal("clock rollback reset budget")
	}
	if !allowPersisted(dir, "errors", 5, now.Add(time.Minute)) {
		t.Fatal("budget did not renew")
	}
	path := filepath.Join(dir, "config.json")
	value := config.Default()
	value.Telemetry = testSettings()
	if err := config.Save(path, value); err != nil {
		t.Fatal(err)
	}
	r, err := newTestReporter(value.Telemetry, "test", &recordingTransport{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.configPath = path
	value.Telemetry.Enabled = false
	if err := config.Save(path, value); err != nil {
		t.Fatal(err)
	}
	if r.allow("logs", 30) {
		t.Fatal("revoked consent ignored")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if r.allow("metrics", 60) {
		t.Fatal("unreadable consent enabled reporting")
	}
}

func TestNestedTracesPreserveChildTimings(t *testing.T) {
	transport := &recordingTransport{}
	r, err := newTestReporter(testSettings(), "test", transport)
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

func TestPanicIsReportedWithoutSwallowingOrLeakingValue(t *testing.T) {
	transport := &recordingTransport{}
	r, err := newTestReporter(testSettings(), "test", transport)
	if err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if recover() != "secret panic" {
				t.Error("panic changed or swallowed")
			}
		}()
		_ = r.Run(context.Background(), "daemon", func(context.Context) error { panic("secret panic") })
	}()
	r.Close()
	var failures int
	for _, event := range transport.events {
		if len(event.Exception) == 0 {
			continue
		}
		failures++
		if mechanism := event.Exception[0].Mechanism; mechanism == nil || mechanism.Handled == nil || *mechanism.Handled {
			t.Fatal("panic was not marked unhandled")
		}
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "secret panic") {
			t.Fatal("panic value leaked")
		}
	}
	if failures != 1 {
		t.Fatalf("panic reports: %d", failures)
	}
}

func TestZeroTraceRateSendsNoTraces(t *testing.T) {
	settings := testSettings()
	settings.TraceRate = 0
	transport := &recordingTransport{}
	r, err := newTestReporter(settings, "test", transport)
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

func TestErrorsAndLogsKeepActiveSpan(t *testing.T) {
	transport := &recordingTransport{}
	r, err := newTestReporter(testSettings(), "test", transport)
	if err != nil {
		t.Fatal(err)
	}
	spans := map[string]*sentry.Span{}
	_ = r.Run(context.Background(), "install", func(ctx context.Context) error {
		spans["install"] = sentry.SpanFromContext(ctx)
		return r.Run(ctx, "service.health", func(ctx context.Context) error {
			spans["service.health"] = sentry.SpanFromContext(ctx)
			return errors.New("private failure")
		})
	})
	r.Close()
	var failures, logs int
	for _, event := range transport.events {
		if len(event.Exception) > 0 {
			failures++
			span := spans[event.Transaction]
			trace := event.Contexts["trace"]
			if span == nil || trace["trace_id"] != span.TraceID || trace["span_id"] != span.SpanID {
				t.Fatalf("error lost its active span: %+v", trace)
			}
		}
		for _, log := range event.Logs {
			logs++
			span := spans[strings.Fields(log.Body)[0]]
			if span == nil || log.TraceID != span.TraceID || log.SpanID != span.SpanID {
				t.Fatalf("log lost its active span: %+v", log)
			}
		}
	}
	if failures != 2 || logs != 4 {
		t.Fatalf("missing correlated records: errors=%d logs=%d", failures, logs)
	}
}

func TestCancellationAndDeadlineTraceStatus(t *testing.T) {
	for _, test := range []struct {
		name     string
		err      error
		status   sentry.SpanStatus
		failures int
	}{
		{"cancelled", context.Canceled, sentry.SpanStatusCanceled, 0},
		{"deadline", context.DeadlineExceeded, sentry.SpanStatusDeadlineExceeded, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := &recordingTransport{}
			r, err := newTestReporter(testSettings(), "test", transport)
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Run(context.Background(), "install", func(context.Context) error { return test.err }); err != test.err {
				t.Fatal("operation result changed")
			}
			r.Close()
			var traces, failures, failedMetrics int
			for _, event := range transport.events {
				if event.Type == "transaction" {
					traces++
					if event.Contexts["trace"]["status"] != test.status {
						t.Fatalf("wrong status: %+v", event.Contexts["trace"])
					}
				}
				if len(event.Exception) > 0 {
					failures++
				}
				for _, metric := range event.Metrics {
					if metric.Name == "operation.failed" {
						failedMetrics++
					}
				}
				for _, log := range event.Logs {
					if test.failures == 0 && log.Level == sentry.LogLevelError {
						t.Fatal("cancellation logged as error")
					}
				}
			}
			if traces != 1 || failures != test.failures || failedMetrics != test.failures {
				t.Fatalf("unexpected signals: traces=%d errors=%d failed metrics=%d", traces, failures, failedMetrics)
			}
		})
	}
}
