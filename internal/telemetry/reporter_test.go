package telemetry

import (
	"context"
	"encoding/json"
	"errors"
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

func TestDisabledDoesNotInitializeSDK(t *testing.T) {
	t.Setenv("SENTRY_DSN", DSN)
	transport := &recordingTransport{}
	r, err := newReporter(config.Default().Telemetry, "test", transport)
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

func TestAllProductsAndPrivacy(t *testing.T) {
	transport := &recordingTransport{}
	r, err := newReporter(testSettings(), "test", transport)
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
	r, err := newReporter(testSettings(), "test", &recordingTransport{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	event := &sentry.Event{Transaction: "install", ServerName: "secret-host", Message: "secret-message", User: sentry.User{IPAddress: "192.0.2.55"}}
	event.Contexts = map[string]sentry.Context{"device": {"secret": "secret-context"}}
	event.Attachments = []*sentry.Attachment{{Filename: "secret-file", Payload: []byte("secret-config")}}
	event.Exception = []sentry.Exception{{Type: "error", Value: "secret-error", Stacktrace: &sentry.Stacktrace{Frames: []sentry.Frame{{Filename: "/home/secret/file.go", AbsPath: "secret-path", Vars: map[string]any{"password": "secret"}, ContextLine: "secret-source", Function: "main.run", Lineno: 42}}}}}
	clean := r.filterEvent(event, nil)
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
			r, err := newReporter(settings, "test", transport)
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
	r, err := newReporter(value.Telemetry, "test", &recordingTransport{})
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
	r, err := newReporter(testSettings(), "test", transport)
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
	r, err := newReporter(testSettings(), "test", transport)
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
	r, err := newReporter(settings, "test", transport)
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
