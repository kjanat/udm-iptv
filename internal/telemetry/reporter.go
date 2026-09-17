// Package telemetry sends explicitly selected, bounded operational data.
// Configuration research uses an explicit allowlist; raw logs and credentials
// are never attached to events.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"path/filepath"
	"runtime/debug"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/getsentry/sentry-go/attribute"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/device"
)

// DSN is injected into official releases using -ldflags -X. Unstamped builds
// have no telemetry destination, even when SENTRY_DSN is set at runtime.
var DSN string

var (
	errNoTelemetryEndpoint = errors.New("this build has no telemetry endpoint")
	errPanic               = errors.New("panic")
)

const (
	transportBufferSize = 32
	// sentryRequestTimeout bounds both the HTTP transport and its client, so
	// a stalled telemetry request never blocks daemon shutdown for long.
	sentryRequestTimeout = 2 * time.Second
	// maxTraceSpans bounds a transaction's recorded spans.
	maxTraceSpans = 32
	// exceptionChainLimit bounds how many wrapped/joined errors an event keeps.
	exceptionChainLimit = 16
)

const (
	tracesPerMinute  = 20
	errorsPerMinute  = 5
	logsPerMinute    = 30
	metricsPerMinute = 60
	presetsPerMinute = 5
)

// Reporter sends bounded telemetry events to Sentry.
// It is safe to call its methods concurrently.
type Reporter struct {
	client      *sentry.Client
	hub         *sentry.Hub
	settings    config.Telemetry
	configPath  string
	stateDir    string
	release     string
	dist        string
	vcsModified string
	goVersion   string
	mu          sync.Mutex
	window      time.Time
	counts      map[string]int
	metadata    map[string]string
}

// New returns a Reporter that sends events to Sentry, or
// nil if telemetry is disabled.
func New(settings config.Telemetry, version, configPath, stateDir string) (*Reporter, error) {
	transport := sentry.NewHTTPTransport()
	transport.BufferSize = transportBufferSize
	transport.Timeout = sentryRequestTimeout
	r, err := newReporter(settings, version, transport, DSN)
	if err == nil {
		r.configPath, r.stateDir = configPath, stateDir
	}

	return r, err
}

func newReporter(settings config.Telemetry, version string, transport sentry.Transport, dsn string) (*Reporter, error) {
	stamp := vcsStamp(debug.ReadBuildInfo())
	r := &Reporter{
		settings: settings, release: "udm-iptv@" + version, counts: make(map[string]int),
		dist: stamp.revision, vcsModified: stamp.modified, goVersion: stamp.toolchain,
	}
	if !settings.Enabled || (!settings.Errors && !settings.Logs && !settings.Metrics && !settings.Tracing && !settings.Presets && !settings.NetworkIdentity) {
		return r, nil
	}
	if dsn == "" {
		return nil, errNoTelemetryEndpoint
	}
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn: dsn, Release: r.release, Dist: r.dist, Environment: "production", ServerName: "udm-iptv",
		Transport: transport, HTTPClient: &http.Client{Timeout: sentryRequestTimeout},
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
		MaxBreadcrumbs: -1, MaxSpans: maxTraceSpans, DisableClientReports: true,
		Integrations: func([]sentry.Integration) []sentry.Integration { return nil },
		BeforeSend:   r.filterEvent, BeforeSendTransaction: r.filterEvent,
		BeforeSendLog: r.filterLog, BeforeSendMetric: r.filterMetric,
	})
	if err != nil {
		return nil, fmt.Errorf("create telemetry client: %w", err)
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

// Close flushes buffered events and releases the underlying Sentry client.
func (r *Reporter) Close() {
	if r == nil || r.client == nil {
		return
	}
	r.client.Flush(sentryRequestTimeout)
	r.client.Close()
}

var operations = map[string]bool{
	"install": true, "upgrade": true, "configure": true, "restart": true, "uninstall": true,
	"daemon": true, "daemon.startup": true, "dhcp.bound": true, "dhcp.renew": true,
	"dhcp.deconfig": true, "dhcp.leasefail": true, "dhcp.nak": true,
	"dhcp.acquire": true, "service.health": true,
}

// SetMetadata stores allowlisted board, firmware, proxy and profile tags.
func (r *Reporter) SetMetadata(model, firmware, discovery, sysid, proxy, profile string) {
	r.metadata = make(map[string]string)
	if device.KnownBoard(model) {
		r.metadata["model"] = model
	}
	if device.ValidFirmware(firmware) {
		r.metadata["firmware"] = firmware
	}
	if device.ValidDiscovery(discovery) {
		r.metadata["firmware_discovery"] = discovery
	}
	if id := device.NormalizeSysID(sysid); id != "" {
		r.metadata["sysid"] = id
	}
	if proxy == "improxy" || proxy == "igmpproxy" {
		r.metadata["proxy"] = proxy
	}
	if _, ok := config.ProfileByID(profile); ok {
		r.metadata["profile"] = profile
	}
}

func (r *Reporter) instruments(operation string) bool {
	return r != nil && r.client != nil && operations[operation]
}

// Error text can include tokens, IP addresses and local file paths.
// Report the error class and call stack; keep its text local.
func (r *Reporter) reportOutcome(ctx context.Context, operation string, err error, panicked bool) {
	if panicked {
		r.failure(ctx, operation, err, true)

		return
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		r.failure(ctx, operation, err, false)
	}
}

func (r *Reporter) logOutcome(ctx context.Context, operation string, err error) {
	if !r.settings.Logs {
		return
	}
	logger := sentry.NewLogger(ctx)
	switch {
	case errors.Is(err, context.Canceled):
		logger.Info().Emit(operation + " cancelled")
	case err != nil:
		logger.Error().Emit(operation + " failed")
	default:
		logger.Info().Emit(operation + " completed")
	}
}

func (r *Reporter) meterOutcome(ctx context.Context, operation string, err error, elapsed time.Duration) {
	if !r.settings.Metrics {
		return
	}
	meter := sentry.NewMeter(ctx)
	meter.SetAttributes(attribute.String("operation", operation))
	meter.Count("operation.completed", 1)
	if err != nil && !errors.Is(err, context.Canceled) {
		meter.Count("operation.failed", 1)
	}
	if operation != "daemon" {
		meter.Distribution("operation.duration", elapsed.Seconds(), sentry.WithUnit(sentry.UnitSecond))
	}
}

func spanStatus(err error) sentry.SpanStatus {
	switch {
	case errors.Is(err, context.Canceled):
		return sentry.SpanStatusCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return sentry.SpanStatusDeadlineExceeded
	case err != nil:
		return sentry.SpanStatusInternalError
	default:
		return sentry.SpanStatusOK
	}
}

// Run executes run under a trace and reports its outcome, if operation is
// enabled for telemetry; otherwise it runs run directly.
func (r *Reporter) Run(ctx context.Context, operation string, run func(context.Context) error) (err error) {
	if !r.instruments(operation) {
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
			err = errPanic
		}
		r.reportOutcome(ctx, operation, err, panicked != nil)
		r.logOutcome(ctx, operation, err)
		r.meterOutcome(ctx, operation, err, time.Since(start))
		if span != nil {
			span.Status = spanStatus(err)
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
	event.SetException(err, exceptionChainLimit)
	if panicked {
		event.Exception = []sentry.Exception{{Type: "panic", Stacktrace: sentry.NewStacktrace(), Mechanism: &sentry.Mechanism{Type: "generic"}}}
		event.Exception[0].Mechanism.SetUnhandled()
	}
	r.hub.CaptureEvent(event)
}

// Gauge records a metric sample, if metrics are enabled.
func (r *Reporter) Gauge(ctx context.Context, name string, value float64) {
	if r == nil || r.client == nil || !r.settings.Metrics {
		return
	}
	sentry.NewMeter(sentry.SetHubOnContext(ctx, r.hub)).Gauge(name, value)
}

// WizardEvent counts a configuration wizard interaction: the help overlay,
// the quit prompt, or an abort, keyed by the question that had focus.
func (r *Reporter) WizardEvent(ctx context.Context, event, question string) {
	if r == nil || r.client == nil || !r.settings.Metrics {
		return
	}
	meter := sentry.NewMeter(sentry.SetHubOnContext(ctx, r.hub))
	meter.SetAttributes(attribute.String("event", event), attribute.String("question", question))
	meter.Count("wizard.event", 1)
}

var wizardEvents = map[string]bool{"help": true, "quit.prompt": true, "abort": true}

func isQuestionKey(value string) bool {
	if value == "" || len(value) > 24 {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}

	return true
}

// MetricsEnabled checks the saved master switch before starting collection.
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

func (r *Reporter) allowsEvent(event *sentry.Event) bool {
	if event.Type == "transaction" {
		return r.settings.Tracing && r.allow("traces", tracesPerMinute)
	}

	return r.settings.Errors && r.allow("errors", errorsPerMinute)
}

func cleanTraceContext(contexts map[string]sentry.Context) map[string]sentry.Context {
	source := contexts["trace"]
	if source == nil {
		return nil
	}
	clean := sentry.Context{}
	for _, key := range []string{"trace_id", "span_id", "parent_span_id", "status"} {
		if value, ok := source[key]; ok {
			clean[key] = value
		}
	}

	return map[string]sentry.Context{"trace": clean}
}

func (r *Reporter) filterEvent(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
	if event.Transaction == researchTransaction {
		return r.filterResearch(event)
	}
	if !operations[event.Transaction] || !r.allowsEvent(event) {
		return nil
	}
	clean := &sentry.Event{
		EventID: event.EventID, Timestamp: event.Timestamp, Platform: "go", Release: r.release, Dist: r.dist,
		Level: event.Level, Transaction: event.Transaction, Type: event.Type, StartTime: event.StartTime,
	}
	clean.Tags = r.eventTags()
	clean.User = sentry.User{ID: r.installationID()}
	clean.Contexts = cleanTraceContext(event.Contexts)
	if event.Type == "transaction" {
		clean.Spans = cleanSpans(event.Spans)
	} else {
		clean.Exception = cleanExceptions(event.Exception, event.Transaction)
	}

	return clean
}

func cleanSpans(spans []*sentry.Span) []*sentry.Span {
	var result []*sentry.Span
	for _, span := range spans {
		if span != nil && operations[span.Op] {
			result = append(result, &sentry.Span{TraceID: span.TraceID, SpanID: span.SpanID, ParentSpanID: span.ParentSpanID, Op: span.Op, Name: span.Op, Status: span.Status, StartTime: span.StartTime, EndTime: span.EndTime})
		}
	}

	return result
}

func cleanExceptions(exceptions []sentry.Exception, transaction string) []sentry.Exception {
	result := make([]sentry.Exception, 0, len(exceptions))
	for _, exception := range exceptions {
		value := sentry.Exception{Type: exception.Type, Value: transaction + " failed"}
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
		result = append(result, value)
	}

	return result
}

func (r *Reporter) eventTags() map[string]string {
	tags := make(map[string]string, len(r.metadata))
	maps.Copy(tags, r.metadata)
	if r.dist != "" {
		tags["vcs.revision"] = r.dist
	}
	if r.vcsModified != "" {
		tags["vcs.modified"] = r.vcsModified
	}
	if r.goVersion != "" {
		tags["go"] = r.goVersion
	}
	if len(tags) == 0 {
		return nil
	}

	return tags
}

func (r *Reporter) attributes() map[string]attribute.Value {
	result := map[string]attribute.Value{"sentry.release": attribute.StringValue(r.release)}
	if id := r.installationID(); id != "" {
		result["installation_id"] = attribute.StringValue(id)
	}
	for key, value := range r.eventTags() {
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
	if !valid || !r.allow("logs", logsPerMinute) {
		return nil
	}

	return &sentry.Log{Timestamp: log.Timestamp, TraceID: log.TraceID, SpanID: log.SpanID, Level: log.Level, Severity: log.Severity, Body: log.Body, Attributes: r.attributes()}
}

var metricNames = map[string]bool{
	"operation.completed": true, "operation.failed": true, "operation.duration": true,
	"daemon.uptime": true, "daemon.restarts": true, "multicast.routes": true,
	"multicast.packets": true, "wizard.event": true,
}

var metricAttributes = map[string]func(string) bool{
	"operation": func(value string) bool { return operations[value] },
	"event":     func(value string) bool { return wizardEvents[value] },
	"question":  isQuestionKey,
}

func (r *Reporter) filterMetric(metric *sentry.Metric) *sentry.Metric {
	if !r.settings.Metrics || !metricNames[metric.Name] || !r.allow("metrics", metricsPerMinute) {
		return nil
	}
	attributes := r.attributes()
	for key, accepted := range metricAttributes {
		if value, ok := metric.Attributes[key].AsInterface().(string); ok && accepted(value) {
			attributes[key] = attribute.StringValue(value)
		}
	}

	return &sentry.Metric{Timestamp: metric.Timestamp, TraceID: metric.TraceID, SpanID: metric.SpanID, Type: metric.Type, Name: metric.Name, Value: metric.Value, Unit: metric.Unit, Attributes: attributes}
}
