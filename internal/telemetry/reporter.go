// Package telemetry sends explicitly selected, bounded operational data.
// Never pass command arguments, configuration values or raw logs to this package.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/getsentry/sentry-go/attribute"
	"github.com/kjanat/udm-iptv/internal/config"
)

const DSN = "https://531ba2e8eed91c8edac2e0737f87a4e6@o4511328451756032.ingest.de.sentry.io/4512086558179408"

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
	r, err := newReporter(settings, version, transport)
	if err == nil {
		r.configPath, r.stateDir = configPath, stateDir
	}
	return r, err
}

func newReporter(settings config.Telemetry, version string, transport sentry.Transport) (*Reporter, error) {
	r := &Reporter{settings: settings, release: "udm-iptv@" + version, counts: make(map[string]int)}
	if !settings.Enabled || (!settings.Errors && !settings.Logs && !settings.Metrics && !settings.Tracing) {
		return r, nil
	}
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn: DSN, Release: r.release, Environment: "production", ServerName: "udm-iptv",
		Transport: transport, HTTPClient: &http.Client{Timeout: 2 * time.Second},
		EnableTracing: settings.Tracing, TracesSampleRate: settings.TraceRate,
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
		permitted := map[string]bool{"errors": value.Telemetry.Errors, "logs": value.Telemetry.Logs, "metrics": value.Telemetry.Metrics, "traces": value.Telemetry.Tracing}
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
	if r.settings.Logs {
		sentry.NewLogger(ctx).Info().Emit(operation + " started")
	}
	start := time.Now()
	var span *sentry.Span
	if r.settings.Tracing && operation != "daemon" {
		span = sentry.StartSpan(ctx, operation, sentry.WithTransactionName(operation))
		ctx = span.Context()
	}
	defer func() {
		panicked := recover()
		if panicked != nil {
			err = errors.New("panic")
			r.failure(operation, "panic")
		} else if err != nil && !errors.Is(err, context.Canceled) {
			// Error text can include tokens, IP addresses and local file paths.
			// Report the error class and call stack; keep its text local.
			r.failure(operation, errorClass(err))
		}
		if r.settings.Logs {
			logger := sentry.NewLogger(ctx)
			if err != nil {
				logger.Error().Emit(operation + " failed")
			} else {
				logger.Info().Emit(operation + " completed")
			}
		}
		if r.settings.Metrics {
			meter := sentry.NewMeter(ctx)
			meter.SetAttributes(attribute.String("operation", operation))
			meter.Count("operation.completed", 1)
			if err != nil {
				meter.Count("operation.failed", 1)
			}
			if operation != "daemon" {
				meter.Distribution("operation.duration", time.Since(start).Seconds(), sentry.WithUnit(sentry.UnitSecond))
			}
		}
		if span != nil {
			if err != nil {
				span.Status = sentry.SpanStatusInternalError
			} else {
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

func errorClass(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, os.ErrPermission):
		return "permission_denied"
	case errors.Is(err, os.ErrNotExist):
		return "not_found"
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return fmt.Sprintf("process_exit_%d", exit.ExitCode())
	}
	for range 16 {
		cause := errors.Unwrap(err)
		if cause == nil {
			break
		}
		err = cause
	}
	return fmt.Sprintf("%T", err)
}

func (r *Reporter) failure(operation, class string) {
	if !r.settings.Errors {
		return
	}
	event := sentry.NewEvent()
	event.Level = sentry.LevelError
	event.Transaction = operation
	event.Exception = []sentry.Exception{{Type: class, Value: operation + " failed (details retained locally)", Stacktrace: sentry.NewStacktrace()}}
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
	if trace {
		for _, span := range event.Spans {
			if span != nil && operations[span.Op] {
				clean.Spans = append(clean.Spans, &sentry.Span{TraceID: span.TraceID, SpanID: span.SpanID, ParentSpanID: span.ParentSpanID, Op: span.Op, Name: span.Op, Status: span.Status, StartTime: span.StartTime, EndTime: span.EndTime})
			}
		}
		if source := event.Contexts["trace"]; source != nil {
			context := sentry.Context{}
			for _, key := range []string{"trace_id", "span_id", "parent_span_id", "status"} {
				context[key] = source[key]
			}
			clean.Contexts = map[string]sentry.Context{"trace": context}
		}
	} else {
		for _, exception := range event.Exception {
			value := sentry.Exception{Type: exception.Type, Value: event.Transaction + " failed (details retained locally)"}
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
		if log.Body == op+" failed" || log.Body == op+" completed" || log.Body == op+" started" {
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
