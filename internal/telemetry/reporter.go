// Package telemetry sends bounded operational data to Sentry.
package telemetry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/getsentry/sentry-go/attribute"
	sentryhttpclient "github.com/getsentry/sentry-go/httpclient"
	sentryslog "github.com/getsentry/sentry-go/slog"

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
	// maxBreadcrumbs bounds the operation trail a failure carries.
	maxBreadcrumbs = 50
	// attachmentLimit bounds the diagnostics attached to a failure.
	attachmentLimit = 256 << 10
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
	client      *sentry.Client
	hub         *sentry.Hub
	settings    config.Telemetry
	configPath  string
	stateDir    string
	release     string
	environment string
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
		settings: settings, release: "udm-iptv@" + version, environment: environmentFor(version),
		counts: make(map[string]int),
		dist:   stamp.revision, vcsModified: stamp.modified, goVersion: stamp.toolchain,
	}
	if !settings.Enabled || (!settings.Errors && !settings.Logs && !settings.Metrics && !settings.Tracing && !settings.Presets && !settings.NetworkIdentity) {
		return r, nil
	}
	if dsn == "" {
		return nil, errNoTelemetryEndpoint
	}
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn: dsn, Release: r.release, Dist: r.dist, Environment: r.environment, ServerName: "udm-iptv",
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
		MaxBreadcrumbs: maxBreadcrumbs, MaxSpans: maxTraceSpans, DisableClientReports: true,
		BeforeSend: r.filterEvent, BeforeSendTransaction: r.filterEvent,
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
	OperationDaemon: true, "daemon.startup": true, "dhcp.bound": true, "dhcp.renew": true,
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
	if proxy == config.ProxyImproxy || proxy == config.ProxyIgmpproxy {
		r.metadata["proxy"] = proxy
	}
	if _, ok := config.ProfileByID(profile); ok {
		r.metadata["profile"] = profile
	}
}

// The installation tag says who installed the executable that reports.
const (
	installationStandalone = "standalone"
	installationPackage    = "package"
	installationStale      = "package-stale"
)

var packageVersionPattern = regexp.MustCompile(`^[0-9][0-9A-Za-z.+~-]{0,63}$`)

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
			err = errPanic
		}
		r.breadcrumb(outcomeMessage(operation, err))
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
	if store, ok := ctx.Value(attachmentKey{}).(*attachmentStore); ok {
		event.Attachments = store.attachments()
	}
	if span := sentry.SpanFromContext(ctx); span != nil {
		event.Contexts["trace"] = sentry.Context{"trace_id": span.TraceID, "span_id": span.SpanID, "parent_span_id": span.ParentSpanID}
	}
	event.SetException(err, exceptionChainLimit)
	if panicked {
		event.Exception = []sentry.Exception{{Type: "panic", Stacktrace: sentry.NewStacktrace(), Mechanism: &sentry.Mechanism{Type: "generic"}}}
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

	return err == nil && value.Telemetry.Enabled && value.Telemetry.Metrics
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
	if len(diagnostics) > attachmentLimit {
		diagnostics = diagnostics[:attachmentLimit]
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.items = append(store.items, &sentry.Attachment{Filename: attachmentName, ContentType: "text/plain", Payload: diagnostics})
}

// HTTPTransport wraps base so every outgoing request becomes an http.client
// span under the operation running in the request's context, with the
// trace headers Sentry needs to link it.
func HTTPTransport(base http.RoundTripper) http.RoundTripper {
	return sentryhttpclient.NewSentryRoundTripper(base)
}

// LineWriter returns a writer that records every complete line written to
// it as a Sentry log tagged with source, through the slog integration.
func (r *Reporter) LineWriter(ctx context.Context, source string) io.Writer {
	if r == nil || r.client == nil || !r.settings.Logs {
		return io.Discard
	}
	ctx = sentry.SetHubOnContext(ctx, r.hub)

	return &lineWriter{logger: slog.New(sentryslog.Option{}.NewSentryHandler(ctx)).With("source", source)}
}

type lineWriter struct {
	mu     sync.Mutex
	logger *slog.Logger
	buffer []byte
}

func (w *lineWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buffer = append(w.buffer, data...)
	for {
		index := bytes.IndexByte(w.buffer, '\n')
		if index < 0 {
			break
		}
		line := strings.TrimSpace(string(w.buffer[:index]))
		w.buffer = w.buffer[index+1:]
		if line != "" {
			w.logger.Info(line)
		}
	}
	if len(w.buffer) > lineLimit {
		w.buffer = w.buffer[len(w.buffer)-lineLimit:]
	}

	return len(data), nil
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
