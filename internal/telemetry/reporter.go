// Package telemetry sends explicitly selected, bounded operational data.
// Configuration research uses an explicit allowlist; raw logs and credentials
// are never attached to events.
package telemetry

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/getsentry/sentry-go/attribute"
	"github.com/kjanat/udm-iptv/internal/config"
)

// DSN is injected into official releases using -ldflags -X. Unstamped builds
// have no telemetry destination, even when SENTRY_DSN is set at runtime.
var DSN string

type Reporter struct {
	client     *sentry.Client
	hub        *sentry.Hub
	settings   config.Telemetry
	configPath string
	stateDir   string
	release    string
	mu         sync.Mutex
	window     time.Time
	counts     map[string]int
	metadata   map[string]string
}

func New(settings config.Telemetry, version, configPath, stateDir string) (*Reporter, error) {
	transport := sentry.NewHTTPTransport()
	transport.BufferSize = 32
	transport.Timeout = 2 * time.Second
	r, err := newReporter(settings, version, transport, DSN)
	if err == nil {
		r.configPath, r.stateDir = configPath, stateDir
	}

	return r, err
}

func newReporter(settings config.Telemetry, version string, transport sentry.Transport, dsn string) (*Reporter, error) {
	r := &Reporter{settings: settings, release: "udm-iptv@" + version, counts: make(map[string]int)}
	if !settings.Enabled || (!settings.Errors && !settings.Logs && !settings.Metrics && !settings.Tracing && !settings.Presets && !settings.NetworkIdentity) {
		return r, nil
	}
	if dsn == "" {
		return nil, errors.New("this build has no telemetry endpoint")
	}
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn: dsn, Release: r.release, Environment: "production", ServerName: "udm-iptv",
		Transport: transport, HTTPClient: &http.Client{Timeout: 2 * time.Second},
		EnableTracing: settings.Tracing, TracesSampleRate: settings.TraceRate,
		DataCollection: &sentry.DataCollection{
			UserInfo: sentry.Set(false), HTTPBodies: []sentry.BodyType{},
			Cookies:     &sentry.KeyValueCollectionBehavior{Mode: sentry.CollectionOff},
			QueryParams: &sentry.KeyValueCollectionBehavior{Mode: sentry.CollectionOff},
			HTTPHeaders: &sentry.HeaderCollectionConfig{
				Request:  &sentry.KeyValueCollectionBehavior{Mode: sentry.CollectionOff},
				Response: &sentry.KeyValueCollectionBehavior{Mode: sentry.CollectionOff},
			},
		},
		MaxBreadcrumbs: -1, MaxSpans: 32, DisableClientReports: true,
		Integrations: func([]sentry.Integration) []sentry.Integration { return nil },
		BeforeSend:   r.filterEvent, BeforeSendTransaction: r.filterEvent,
		BeforeSendLog: r.filterLog, BeforeSendMetric: r.filterMetric,
	})
	if err != nil {
		return nil, err
	}
	r.client, r.hub = client, sentry.NewHub(client, sentry.NewScope())

	return r, nil
}

// allow bounds each signal independently; the key space is fixed by this package.
func (r *Reporter) allow(kind string, maximum int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.configPath != "" {
		value, err := config.Load(r.configPath)
		if err != nil || !value.Telemetry.Enabled {
			return false
		}
		permitted := map[string]bool{"errors": value.Telemetry.Errors, "logs": value.Telemetry.Logs, "metrics": value.Telemetry.Metrics, "traces": value.Telemetry.Tracing, "presets": value.Telemetry.Presets, "network": value.Telemetry.NetworkIdentity}
		if !permitted[kind] {
			return false
		}
	}
	if time.Since(r.window) >= time.Minute {
		r.window = time.Now()
		clear(r.counts)
	}
	if r.counts[kind] >= maximum {
		return false
	}
	if r.stateDir != "" && !allowPersisted(r.stateDir, kind, maximum, time.Now()) {
		return false
	}
	r.counts[kind]++

	return true
}

func (r *Reporter) Close() {
	if r == nil || r.client == nil {
		return
	}
	r.client.Flush(2 * time.Second)
	r.client.Close()
}

var operations = map[string]bool{
	"install": true, "upgrade": true, "configure": true, "restart": true, "uninstall": true,
	"daemon": true, "daemon.startup": true, "dhcp.bound": true, "dhcp.renew": true,
	"dhcp.deconfig": true, "dhcp.leasefail": true, "dhcp.nak": true,
	"dhcp.acquire": true, "service.health": true,
}

var firmwareVersion = regexp.MustCompile(`^[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}$`)

// SetMetadata accepts only known hardware/profile names and numeric firmware.
// It must be called before running operations or starting metric collection.
func (r *Reporter) SetMetadata(model, firmware, proxy, profile string) {
	r.metadata = make(map[string]string)
	switch model {
	case "UDM", "UDMPRO", "UDMPROSE", "UDMPROMAX", "UDMEA4C", "UXGPRO", "UXG":
		r.metadata["model"] = model
	}
	if firmwareVersion.MatchString(firmware) {
		r.metadata["firmware"] = firmware
	}
	if proxy == "improxy" || proxy == "igmpproxy" {
		r.metadata["proxy"] = proxy
	}
	if _, ok := config.ProfileByID(profile); ok {
		r.metadata["profile"] = profile
	}
}

func (r *Reporter) Run(ctx context.Context, operation string, run func(context.Context) error) (err error) {
	if r == nil || r.client == nil || !operations[operation] {
		return run(ctx)
	}
	ctx = sentry.SetHubOnContext(ctx, r.hub)
	start := time.Now()
	var span *sentry.Span
	if r.settings.Tracing && operation != "daemon" {
		span = sentry.StartSpan(ctx, operation, sentry.WithTransactionName(operation))
		ctx = span.Context() //nolint:contextcheck // Sentry derives this context from the supplied parent.
	}
	if r.settings.Logs {
		sentry.NewLogger(ctx).Info().Emit(operation + " started")
	}
	defer func() {
		panicked := recover()
		if panicked != nil {
			err = errors.New("panic")
			r.failure(ctx, operation, err, true)
		} else if err != nil && !errors.Is(err, context.Canceled) {
			// Error text can include tokens, IP addresses and local file paths.
			// Report the error class and call stack; keep its text local.
			r.failure(ctx, operation, err, false)
		}
		if r.settings.Logs {
			logger := sentry.NewLogger(ctx)
			if errors.Is(err, context.Canceled) {
				logger.Info().Emit(operation + " cancelled")
			} else if err != nil {
				logger.Error().Emit(operation + " failed")
			} else {
				logger.Info().Emit(operation + " completed")
			}
		}
		if r.settings.Metrics {
			meter := sentry.NewMeter(ctx)
			meter.SetAttributes(attribute.String("operation", operation))
			meter.Count("operation.completed", 1)
			if err != nil && !errors.Is(err, context.Canceled) {
				meter.Count("operation.failed", 1)
			}
			if operation != "daemon" {
				meter.Distribution("operation.duration", time.Since(start).Seconds(), sentry.WithUnit(sentry.UnitSecond))
			}
		}
		if span != nil {
			switch {
			case errors.Is(err, context.Canceled):
				span.Status = sentry.SpanStatusCanceled
			case errors.Is(err, context.DeadlineExceeded):
				span.Status = sentry.SpanStatusDeadlineExceeded
			case err != nil:
				span.Status = sentry.SpanStatusInternalError
			default:
				span.Status = sentry.SpanStatusOK
			}
			span.Finish()
		}
		if panicked != nil {
			panic(panicked)
		}
	}()

	return run(ctx)
}

func (r *Reporter) failure(ctx context.Context, operation string, err error, panicked bool) {
	if !r.settings.Errors {
		return
	}
	event := sentry.NewEvent()
	event.Level = sentry.LevelError
	event.Transaction = operation
	if span := sentry.SpanFromContext(ctx); span != nil {
		event.Contexts["trace"] = sentry.Context{"trace_id": span.TraceID, "span_id": span.SpanID, "parent_span_id": span.ParentSpanID}
	}
	// Let the SDK preserve wrapped and joined errors and any embedded stacks.
	// BeforeSend removes raw messages and stack details before transport.
	event.SetException(err, 16)
	if panicked {
		event.Exception = []sentry.Exception{{Type: "panic", Stacktrace: sentry.NewStacktrace(), Mechanism: &sentry.Mechanism{Type: "generic"}}}
		event.Exception[0].Mechanism.SetUnhandled()
	}
	r.hub.CaptureEvent(event)
}

func (r *Reporter) Gauge(ctx context.Context, name string, value float64) {
	if r == nil || r.client == nil || !r.settings.Metrics {
		return
	}
	sentry.NewMeter(sentry.SetHubOnContext(ctx, r.hub)).Gauge(name, value)
}

func (r *Reporter) MetricsEnabled() bool {
	if r == nil || r.client == nil || !r.settings.Metrics {
		return false
	}
	if r.configPath == "" {
		return true
	}
	value, err := config.Load(r.configPath)

	return err == nil && value.Telemetry.Enabled && value.Telemetry.Metrics
}

func (r *Reporter) filterEvent(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
	if event.Transaction == "installation.report" {
		return r.filterResearch(event)
	}
	if !operations[event.Transaction] {
		return nil
	}
	trace := event.Type == "transaction"
	if trace && (!r.settings.Tracing || !r.allow("traces", 20)) {
		return nil
	}
	if !trace && (!r.settings.Errors || !r.allow("errors", 5)) {
		return nil
	}
	clean := &sentry.Event{
		EventID: event.EventID, Timestamp: event.Timestamp, Platform: "go", Release: r.release,
		Level: event.Level, Transaction: event.Transaction, Type: event.Type, StartTime: event.StartTime,
	}
	clean.Tags = r.metadata
	clean.User = sentry.User{ID: r.installationID()}
	if source := event.Contexts["trace"]; source != nil {
		context := sentry.Context{}
		for _, key := range []string{"trace_id", "span_id", "parent_span_id", "status"} {
			if value, ok := source[key]; ok {
				context[key] = value
			}
		}
		clean.Contexts = map[string]sentry.Context{"trace": context}
	}
	if trace {
		for _, span := range event.Spans {
			if span != nil && operations[span.Op] {
				clean.Spans = append(clean.Spans, &sentry.Span{TraceID: span.TraceID, SpanID: span.SpanID, ParentSpanID: span.ParentSpanID, Op: span.Op, Name: span.Op, Status: span.Status, StartTime: span.StartTime, EndTime: span.EndTime})
			}
		}
	} else {
		for _, exception := range event.Exception {
			value := sentry.Exception{Type: exception.Type, Value: event.Transaction + " failed"}
			if mechanism := exception.Mechanism; mechanism != nil {
				value.Mechanism = &sentry.Mechanism{Type: "generic", Handled: mechanism.Handled, ParentID: mechanism.ParentID, ExceptionID: mechanism.ExceptionID, IsExceptionGroup: mechanism.IsExceptionGroup}
				if mechanism.Type == "chained" {
					value.Mechanism.Type = "chained"
				}
			}
			if exception.Stacktrace != nil {
				value.Stacktrace = &sentry.Stacktrace{}
				for _, frame := range exception.Stacktrace.Frames {
					value.Stacktrace.Frames = append(value.Stacktrace.Frames, sentry.Frame{Function: frame.Function, Module: frame.Module, Filename: filepath.Base(frame.Filename), Lineno: frame.Lineno, InApp: frame.InApp})
				}
			}
			clean.Exception = append(clean.Exception, value)
		}
	}

	return clean
}

func (r *Reporter) attributes() map[string]attribute.Value {
	result := map[string]attribute.Value{"sentry.release": attribute.StringValue(r.release)}
	if id := r.installationID(); id != "" {
		result["installation_id"] = attribute.StringValue(id)
	}
	for key, value := range r.metadata {
		result[key] = attribute.StringValue(value)
	}

	return result
}

func (r *Reporter) filterLog(log *sentry.Log) *sentry.Log {
	if !r.settings.Logs {
		return nil
	}
	valid := false
	for op := range operations {
		if log.Body == op+" failed" || log.Body == op+" completed" || log.Body == op+" started" || log.Body == op+" cancelled" {
			valid = true

			break
		}
	}
	if !valid || !r.allow("logs", 30) {
		return nil
	}

	return &sentry.Log{Timestamp: log.Timestamp, TraceID: log.TraceID, SpanID: log.SpanID, Level: log.Level, Severity: log.Severity, Body: log.Body, Attributes: r.attributes()}
}

func (r *Reporter) filterMetric(metric *sentry.Metric) *sentry.Metric {
	if !r.settings.Metrics {
		return nil
	}
	switch metric.Name {
	case "operation.completed", "operation.failed", "operation.duration", "daemon.uptime", "daemon.restarts", "multicast.routes", "multicast.packets":
	default:
		return nil
	}
	if !r.allow("metrics", 60) {
		return nil
	}
	attributes := r.attributes()
	if op, ok := metric.Attributes["operation"].AsInterface().(string); ok && operations[op] {
		attributes["operation"] = attribute.StringValue(op)
	}

	return &sentry.Metric{Timestamp: metric.Timestamp, TraceID: metric.TraceID, SpanID: metric.SpanID, Type: metric.Type, Name: metric.Name, Value: metric.Value, Unit: metric.Unit, Attributes: attributes}
}
