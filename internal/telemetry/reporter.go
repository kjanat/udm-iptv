// Package telemetry sends bounded operational data to Sentry.
package telemetry

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/getsentry/sentry-go"
	"github.com/getsentry/sentry-go/attribute"
	sentryhttpclient "github.com/getsentry/sentry-go/httpclient"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/device"
)

// DSN is injected into official releases using -ldflags -X. Unstamped builds
// have no telemetry destination, even when SENTRY_DSN is set at runtime.
var DSN string

var errNoTelemetryEndpoint = errors.New("this build has no telemetry endpoint")

const (
	transportBufferSize = 32
	// sentryRequestTimeout bounds both the HTTP transport and its client, so
	// a stalled telemetry request never blocks daemon shutdown for long.
	sentryRequestTimeout = 2 * time.Second
	// maxTraceSpans configures this client. Sentry Go v0.49.0's span recorder
	// instead reads the global hub's client, so this context hub does not enforce it.
	maxTraceSpans = 32
	// exceptionChainLimit bounds unwrap depth, not the number of joined errors.
	exceptionChainLimit = 16
	// maxBreadcrumbs bounds the operation trail a failure carries.
	maxBreadcrumbs = 50
	// attachmentCompressionThreshold preserves large reports in a gzip attachment.
	attachmentCompressionThreshold = 256 << 10
	// lineLimit bounds a partial output line held back until its newline.
	lineLimit = 4096
)

// OperationDaemon names the long-running service operation.
const OperationDaemon = "daemon"

const (
	attachmentName     = "udm-iptv-diagnostics.txt"
	breadcrumbCategory = "operation"
	stepOp             = "step"
)

// An installation's cron monitor expects one observation an hour; a router
// that falls silent raises a missed check-in instead of disappearing.
const (
	observationMonitorPrefix = "observation-"
	observationMarginMinutes = 15
	observationFailures      = 2
	observationRecovery      = 1
	monitorSlugIDLength      = 12
)

const (
	tracesPerMinute  = 20
	errorsPerMinute  = 5
	logsPerMinute    = 30
	metricsPerMinute = 60
	presetsPerMinute = 5
)

const (
	envProduction  = "production"
	envPreview     = "preview"
	envDevelopment = "development"
)

var previewIdentifiers = map[string]bool{"preview": true, "rc": true, "alpha": true, "beta": true}

func environmentFor(version string) string {
	v := strings.ToLower(version)
	switch {
	case v == "" || v == "dev" || strings.Contains(v, "snapshot") || strings.Contains(v, "-next"):
		return envDevelopment
	case hasPreviewIdentifier(v):
		return envPreview
	default:
		return envProduction
	}
}

func hasPreviewIdentifier(version string) bool {
	_, prerelease, ok := strings.Cut(version, "-")
	if !ok {
		return false
	}
	prerelease, _, _ = strings.Cut(prerelease, "+")
	for identifier := range strings.SplitSeq(prerelease, ".") {
		if previewIdentifiers[strings.TrimRight(identifier, "0123456789")] {
			return true
		}
	}

	return false
}

// Reporter sends bounded telemetry events to Sentry.
// It is safe to call its methods concurrently.
type Reporter struct {
	client            *sentry.Client
	hub               *sentry.Hub
	settings          config.Telemetry
	configPath        string
	stateDir          string
	release           string
	environment       string
	dist              string
	vcsModified       string
	goVersion         string
	mu                sync.Mutex
	window            time.Time
	counts            map[string]int
	breadcrumbCount   uint64
	metadata          map[string]string
	networkIdentity   *NetworkIdentity
	networkIdentityAt time.Time
	lineWriters       []*lineWriter
	deliveryMu        sync.Mutex
	deliveryOutput    io.Writer
	deliveryCounts    map[string]uint64
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
		settings: settings, release: "udm-iptv@" + version, environment: environmentFor(version),
		counts: make(map[string]int),
		dist:   stamp.revision, vcsModified: stamp.modified, goVersion: stamp.toolchain,
		deliveryOutput: os.Stderr, deliveryCounts: make(map[string]uint64),
	}
	if !reportingEnabled(settings) {
		return r, nil
	}
	if dsn == "" {
		return nil, errNoTelemetryEndpoint
	}
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn: dsn, Release: r.release, Dist: r.dist, Environment: r.environment, ServerName: "udm-iptv",
		Transport: transport, HTTPClient: &http.Client{Timeout: sentryRequestTimeout, Transport: deliveryTransport{reporter: r, base: http.DefaultTransport}},
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
		MaxBreadcrumbs: maxBreadcrumbs, MaxSpans: maxTraceSpans,
		BeforeSend: r.filterEvent, BeforeSendTransaction: r.filterEvent,
		BeforeBreadcrumb: r.countBreadcrumb,
		BeforeSendLog:    r.filterLog, BeforeSendMetric: r.filterMetric,
	})
	if err != nil {
		return nil, fmt.Errorf("create telemetry client: %w", err)
	}
	r.client, r.hub = client, sentry.NewHub(client, sentry.NewScope())

	return r, nil
}

func reportingEnabled(settings config.Telemetry) bool {
	return settings.Enabled && (settings.Errors || settings.Logs || settings.Metrics || settings.Tracing || settings.Presets || settings.NetworkIdentity)
}

// allow bounds each signal independently; the key space is fixed by this package.
func (r *Reporter) allow(kind string, maximum int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.configPath != "" {
		value, err := config.Load(r.configPath)
		if err != nil {
			r.deliveryIssue(kind, "cannot read telemetry settings: "+err.Error())
			return false
		}
		if !value.Telemetry.Enabled {
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
		r.deliveryIssue(kind, "per-minute telemetry budget exhausted")
		return false
	}
	if r.stateDir != "" {
		if err := allowPersistedReason(r.stateDir, kind, maximum, time.Now()); err != nil {
			r.deliveryIssue(kind, err.Error())
			return false
		}
	}
	r.counts[kind]++

	return true
}

// Close flushes buffered events and releases the underlying Sentry client.
func (r *Reporter) Close() {
	if r == nil || r.client == nil {
		return
	}
	r.mu.Lock()
	writers := append([]*lineWriter(nil), r.lineWriters...)
	r.lineWriters = nil
	r.mu.Unlock()
	for _, writer := range writers {
		writer.Flush()
	}
	r.Flush()
	r.client.Close()
	r.deliverySummary()
}

var operations = map[string]bool{
	"install": true, "upgrade": true, "configure": true, "configure.set": true,
	"start": true, "stop": true, "restart": true, "uninstall": true,
	OperationDaemon: true, "daemon.startup": true, "dhcp.bound": true, "dhcp.renew": true,
	"dhcp.deconfig": true, "dhcp.leasefail": true, "dhcp.nak": true,
	"dhcp.acquire": true, "service.activate": true, "service.health": true,
}

// SetMetadata retains printable hardware metadata, including newly released
// boards and custom profiles that this executable does not yet recognize.
func (r *Reporter) SetMetadata(model, firmware, discovery, sysid, proxy, profile string) {
	r.metadata = make(map[string]string)
	if id := device.NormalizeSysID(sysid); id != "" {
		sysid = id
	}
	for key, value := range map[string]string{"model": model, "firmware": firmware, "firmware_discovery": discovery, "sysid": sysid, "proxy": proxy, "profile": profile} {
		if value != "" && !strings.ContainsFunc(value, unicode.IsControl) {
			r.metadata[key] = value
		}
	}
}

// The installation tag says who installed the executable that reports.
const (
	installationStandalone = "standalone"
	installationPackage    = "package"
	installationStale      = "package-stale"
)

var packageVersionPattern = regexp.MustCompile(`^[0-9][0-9A-Za-z.+:~-]*$`)

// SetInstallation tags every report with the installation kind and, on a
// dpkg-tracked installation, the version dpkg holds. It reports whether that
// version trails the executable, which the tag then says as well.
func (r *Reporter) SetInstallation(packageVersion string) bool {
	if r.metadata == nil {
		r.metadata = make(map[string]string)
	}
	delete(r.metadata, "package_version")
	switch {
	case packageVersion == "":
		r.metadata["installation"] = installationStandalone

		return false
	case !packageVersionPattern.MatchString(packageVersion):
		r.metadata["installation"] = installationPackage

		return false
	case "udm-iptv@"+packageVersion == r.release:
		r.metadata["installation"] = installationPackage
		r.metadata["package_version"] = packageVersion

		return false
	default:
		r.metadata["installation"] = installationStale
		r.metadata["package_version"] = packageVersion

		return true
	}
}

// Warn records message as a warning log.
func (r *Reporter) Warn(ctx context.Context, message string) {
	if r == nil || r.client == nil || !r.settings.Logs {
		return
	}
	sentry.NewLogger(sentry.SetHubOnContext(ctx, r.hub)).Warn().Emit(message)
}

func (r *Reporter) instruments(operation string) bool {
	return r != nil && r.client != nil && operations[operation]
}

// reportOutcome preserves the error details for failed operations.
func (r *Reporter) reportOutcome(ctx context.Context, operation string, err error, panicked bool) {
	if panicked {
		r.failure(ctx, operation, err, true)

		return
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		r.failure(ctx, operation, err, false)
	}
}

func outcomeMessage(operation string, err error) (string, sentry.Level) {
	switch {
	case errors.Is(err, context.Canceled):
		return operation + " cancelled", sentry.LevelInfo
	case err != nil:
		return operation + " failed", sentry.LevelError
	default:
		return operation + " completed", sentry.LevelInfo
	}
}

func (r *Reporter) logOutcome(ctx context.Context, operation string, err error) {
	if !r.settings.Logs {
		return
	}
	message, level := outcomeMessage(operation, err)
	entry := sentry.NewLogger(ctx).Info()
	if level == sentry.LevelError {
		entry = sentry.NewLogger(ctx).Error()
	}
	entry.Emit(message)
}

func (r *Reporter) breadcrumb(message string, level sentry.Level) {
	r.hub.AddBreadcrumb(&sentry.Breadcrumb{Category: breadcrumbCategory, Message: message, Level: level, Timestamp: time.Now()}, nil)
}

// This reporter owns one scope and never clears it. Every accepted breadcrumb
// beyond the SDK's FIFO size therefore evicts exactly one older entry.
func (r *Reporter) countBreadcrumb(breadcrumb *sentry.Breadcrumb, _ *sentry.BreadcrumbHint) *sentry.Breadcrumb {
	r.mu.Lock()
	r.breadcrumbCount++
	evicted := r.breadcrumbCount > maxBreadcrumbs
	r.mu.Unlock()
	if evicted {
		r.deliveryIssue("breadcrumbs", "oldest breadcrumb evicted by SDK history limit")
	}
	return breadcrumb
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
	if operation != OperationDaemon {
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
	parentFailures, _ := ctx.Value(failureKey{}).(*operationFailures)
	failures := &operationFailures{parent: parentFailures}
	ctx = context.WithValue(ctx, failureKey{}, failures)
	if _, nested := ctx.Value(attachmentKey{}).(*attachmentStore); !nested {
		ctx = context.WithValue(ctx, attachmentKey{}, &attachmentStore{})
	}
	start := time.Now()
	var span *sentry.Span
	if r.settings.Tracing && operation != OperationDaemon {
		span = sentry.StartSpan(ctx, operation, sentry.WithTransactionName(operation))
		ctx = span.Context() //nolint:contextcheck // Sentry derives this context from the supplied parent.
	}
	r.breadcrumb(operation+" started", sentry.LevelInfo)
	if r.settings.Logs {
		sentry.NewLogger(ctx).Info().Emit(operation + " started")
	}
	defer func() {
		panicked := recover()
		if panicked != nil {
			err = panicError{value: fmt.Sprint(panicked), typeName: fmt.Sprintf("%T", panicked)}
		}
		r.breadcrumb(outcomeMessage(operation, err))
		failures.report(ctx, r, operation, err, panicked != nil)
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
	if store, ok := ctx.Value(attachmentKey{}).(*attachmentStore); ok {
		event.Attachments = store.attachments()
	}
	if span := sentry.SpanFromContext(ctx); span != nil {
		event.Contexts["trace"] = sentry.Context{"trace_id": span.TraceID, "span_id": span.SpanID, "parent_span_id": span.ParentSpanID}
	}
	event.SetException(err, exceptionChainLimit)
	// The SDK synthesizes a stack at this reporting call for plain Go errors.
	// That stack groups unrelated failures at Reporter.failure. Keep genuine
	// origin stacks, and let Sentry group plain errors by their type and value.
	if len(event.Exception) > 0 && sentry.ExtractStacktrace(err) == nil {
		event.Exception[len(event.Exception)-1].Stacktrace = nil
	}
	event.Fingerprint = []string{"{{ default }}", operation}
	if panicked {
		event.Exception = []sentry.Exception{{Type: "panic", Value: err.Error(), Stacktrace: sentry.NewStacktrace(), Mechanism: &sentry.Mechanism{Type: "generic"}}}
		if recovered, ok := errors.AsType[panicError](err); ok {
			event.Exception[0].Type = "panic(" + recovered.typeName + ")"
		}
		event.Exception[0].Mechanism.SetUnhandled()
	}
	r.hub.CaptureEvent(event)
}

// Attribute labels a metric sample.
type Attribute = attribute.Builder

// String is a string-valued metric attribute.
func String(key, value string) Attribute { return attribute.String(key, value) }

// Gauge records a metric sample, if metrics are enabled.
func (r *Reporter) Gauge(ctx context.Context, name string, value float64, attributes ...Attribute) {
	if r == nil || r.client == nil || !r.settings.Metrics {
		return
	}
	meter := sentry.NewMeter(sentry.SetHubOnContext(ctx, r.hub))
	if len(attributes) > 0 {
		meter.SetAttributes(attributes...)
	}
	meter.Gauge(name, value)
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

// MetricsEnabled checks the saved master switch before starting collection.
func (r *Reporter) MetricsEnabled() bool {
	if r == nil || r.client == nil || !r.settings.Metrics {
		return false
	}
	if r.configPath == "" {
		return true
	}
	value, err := config.Load(r.configPath)
	if err != nil {
		r.deliveryIssue("metrics", "cannot read telemetry settings: "+err.Error())
		return false
	}
	return value.Telemetry.Enabled && value.Telemetry.Metrics
}

func (r *Reporter) allowsEvent(event *sentry.Event) bool {
	if event.Type == "transaction" {
		return r.settings.Tracing && r.allow("traces", tracesPerMinute)
	}

	return r.settings.Errors && r.allow("errors", errorsPerMinute)
}

// filterEvent applies the reporting switches and stamps the release, the
// installation and the device tags on whatever the SDK collected.
func (r *Reporter) filterEvent(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
	if event.Transaction == researchTransaction || !r.allowsEvent(event) {
		return nil
	}
	event.Release, event.Dist, event.Environment = r.release, r.dist, r.environment
	event.User = sentry.User{ID: r.installationID()}
	if event.Tags == nil {
		event.Tags = map[string]string{}
	}
	maps.Copy(event.Tags, r.eventTags())

	return event
}

type attachmentKey struct{}

type attachmentStore struct {
	mu    sync.Mutex
	items []*sentry.Attachment
}

func (store *attachmentStore) attachments() []*sentry.Attachment {
	store.mu.Lock()
	defer store.mu.Unlock()

	return append([]*sentry.Attachment(nil), store.items...)
}

// Attach adds diagnostics to the failure report of the operation running in
// ctx. Outside a reported operation it does nothing.
func Attach(ctx context.Context, diagnostics []byte) {
	store, ok := ctx.Value(attachmentKey{}).(*attachmentStore)
	if !ok || len(diagnostics) == 0 {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	attachment := &sentry.Attachment{Filename: attachmentName, ContentType: "text/plain", Payload: append([]byte(nil), diagnostics...)}
	if len(diagnostics) > attachmentCompressionThreshold {
		var compressed strings.Builder
		writer := gzip.NewWriter(&compressed)
		_, writeErr := writer.Write(diagnostics)
		closeErr := writer.Close()
		if writeErr == nil && closeErr == nil {
			attachment.Filename += ".gz"
			attachment.ContentType = "application/gzip"
			attachment.Payload = []byte(compressed.String())
		}
	}
	store.items = append(store.items, attachment)
}

// HTTPTransport wraps base so every outgoing request becomes an http.client
// span under the operation running in the request's context, with the
// trace headers Sentry needs to link it.
func HTTPTransport(base http.RoundTripper) http.RoundTripper {
	return sentryhttpclient.NewSentryRoundTripper(base)
}

// Step times one named stage of the operation running in ctx as a child
// span. The returned context carries the stage; call finish with its error.
func Step(ctx context.Context, name string) (context.Context, func(error)) {
	parent := sentry.SpanFromContext(ctx)
	if parent == nil {
		return ctx, func(error) {}
	}
	child := parent.StartChild(stepOp, sentry.WithDescription(name))

	return child.Context(), func(err error) {
		child.Status = spanStatus(err)
		child.Finish()
	}
}

// ObservationCheckIn reports the hourly observation to this installation's
// cron monitor, when preset reporting is enabled.
func (r *Reporter) ObservationCheckIn(healthy bool) {
	if !r.researchEnabled() {
		return
	}
	id := r.installationID()
	if id == "" {
		return
	}
	status := sentry.CheckInStatusOK
	if !healthy {
		status = sentry.CheckInStatusError
	}
	r.hub.CaptureCheckIn(
		&sentry.CheckIn{MonitorSlug: observationMonitorPrefix + id[:monitorSlugIDLength], Status: status},
		&sentry.MonitorConfig{
			Schedule: sentry.IntervalSchedule(1, sentry.MonitorScheduleUnitHour), CheckInMargin: observationMarginMinutes,
			FailureIssueThreshold: observationFailures, RecoveryThreshold: observationRecovery,
		},
	)
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
	if r.environment != "" {
		result["sentry.environment"] = attribute.StringValue(r.environment)
	}
	if id := r.installationID(); id != "" {
		result["installation_id"] = attribute.StringValue(id)
	}
	for key, value := range r.eventTags() {
		result[key] = attribute.StringValue(value)
	}

	return result
}

func (r *Reporter) mergeAttributes(attributes map[string]attribute.Value) map[string]attribute.Value {
	result := r.attributes()
	maps.Copy(result, attributes)

	return result
}

func (r *Reporter) filterLog(log *sentry.Log) *sentry.Log {
	if researchLog(log.Body) {
		return r.filterResearchLog(log)
	}
	if !r.settings.Logs || !r.allow("logs", logsPerMinute) {
		return nil
	}
	log.Attributes = r.mergeAttributes(log.Attributes)

	return log
}

func researchLog(body string) bool {
	return body == "installation configuration" || body == "installation observation" || body == "installation feedback"
}

func (r *Reporter) filterMetric(metric *sentry.Metric) *sentry.Metric {
	if !r.settings.Metrics || !r.allow("metrics", metricsPerMinute) {
		return nil
	}
	metric.Attributes = r.mergeAttributes(metric.Attributes)

	return metric
}
