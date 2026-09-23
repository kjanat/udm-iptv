package telemetry

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/getsentry/sentry-go/attribute"
	"golang.org/x/sys/unix"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/filemode"
)

const (
	// researchIdentitySize is the byte length of a random installation ID.
	researchIdentitySize = 16
	// researchStateLimit bounds the persisted research state file.
	researchStateLimit = 65536
	// researchTransaction marks an event filterEvent drops.
	researchTransaction = "installation.report"
)

var (
	errInvalidResearchConfig = errors.New("invalid research configuration")
	errFeedbackAnswer        = errors.New("choose working, problems or not-using")
	errUnknownProvider       = errors.New("unknown provider; use a provider or profile ID")
	errResearchUnavailable   = errors.New("preset research is disabled or unavailable")
	errFeedbackNotQueued     = errors.New("feedback was not queued: reporting disabled or rate limit reached")
	errResearchNotQueued     = errors.New("report was not queued: reporting disabled or rate limit reached")
	errResearchNotFlushed    = errors.New("report queued but telemetry did not drain before the deadline")
	errStateTooLarge         = errors.New("telemetry state exceeds size limit")
	errInvalidIdentity       = errors.New("invalid telemetry identity")
	errMissingStateDir       = errors.New("telemetry state directory is missing")
)

type researchState struct {
	ID                 string        `json:"id"`
	Revision           uint64        `json:"revision"`
	Fingerprint        string        `json:"fingerprint"`
	AppliedFingerprint string        `json:"applied_fingerprint"`
	AppliedRevision    uint64        `json:"applied_revision"`
	Changed            time.Time     `json:"last_saved_change"`
	AppliedChanged     time.Time     `json:"last_applied_change"`
	Settings           config.Config `json:"settings"`
}

type researchReport struct {
	Schema            int              `json:"schema"`
	InstallationID    string           `json:"installation_id"`
	Kind              string           `json:"kind"`
	Revision          uint64           `json:"revision"`
	PreviousRevision  uint64           `json:"previous_revision"`
	AppliedRevision   uint64           `json:"applied_revision"`
	ChangedFields     []string         `json:"changed_fields,omitempty"`
	LastSavedChange   time.Time        `json:"last_saved_change"`
	LastAppliedChange time.Time        `json:"last_applied_change"`
	Settings          *config.Config   `json:"settings,omitempty"`
	Applied           bool             `json:"applied"`
	Feedback          string           `json:"feedback,omitempty"`
	ConfirmedProvider string           `json:"user_confirmed_provider,omitempty"`
	Network           *NetworkIdentity `json:"network,omitempty"`
	Observation       *Observation     `json:"observation,omitempty"`
}

// Observation describes measurements, never an inferred customer satisfaction.
type Observation struct {
	UptimeSeconds uint64          `json:"process_uptime_seconds"`
	Restarts      uint64          `json:"systemd_restarts"`
	Active        bool            `json:"service_active"`
	Snapshot      json.RawMessage `json:"snapshot,omitempty"`
}

func (r *Reporter) researchEnabled() bool {
	if r == nil || r.client == nil || !r.settings.Enabled || !r.settings.Presets {
		return false
	}
	if r.configPath == "" {
		return true
	}
	value, err := config.Load(r.configPath)
	if err != nil {
		r.deliveryIssue("presets", "read reporting settings: "+err.Error())
		return false
	}
	return value.Telemetry.Enabled && value.Telemetry.Presets
}

// ResearchEnabled checks the saved master switch before each observation.
func (r *Reporter) ResearchEnabled() bool { return r.researchEnabled() }

// normalized sorts the list fields so equal configurations compare equal.
func normalized(value config.Config) config.Config {
	value.WAN.NATDestinations = sorted(value.WAN.NATDestinations)
	value.Proxy.SourceRanges = sorted(value.Proxy.SourceRanges)
	value.LAN.Interfaces = sorted(value.LAN.Interfaces)
	value.WAN.StaticRoutes = sorted(value.WAN.StaticRoutes)

	return value
}

// The fingerprint is keyed per installation and leaves the telemetry
// preferences out, so changing them is not a configuration change.
func reportable(value config.Config) config.Config {
	value = normalized(value)
	value.Telemetry = config.Telemetry{}

	return value
}

func fingerprintConfig(id string, value config.Config) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode configuration fingerprint: %w", err)
	}
	hash := hmac.New(sha256.New, []byte(id))
	_, _ = hash.Write(data)

	return hex.EncodeToString(hash.Sum(nil)), nil
}

func changedFields(before, after config.Config) []string {
	old, current := flatten("", before), flatten("", after)
	changed := []string{}
	for key := range current {
		if _, ok := old[key]; !ok || old[key] != current[key] {
			changed = append(changed, key)
		}
	}
	for key := range old {
		if _, ok := current[key]; !ok {
			changed = append(changed, key)
		}
	}
	slices.Sort(changed)
	if len(changed) == 0 {
		return []string{"unreported_settings"}
	}

	return changed
}

func flatten(prefix string, value config.Config) map[string]string {
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var tree map[string]any
	if err := json.Unmarshal(data, &tree); err != nil {
		return nil
	}
	result := map[string]string{}
	flattenInto(result, prefix, tree)

	return result
}

func flattenInto(result map[string]string, prefix string, value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			name := key
			if prefix != "" {
				name = prefix + "." + key
			}
			flattenInto(result, name, child)
		}
	default:
		result[prefix] = fmt.Sprint(typed)
	}
}

func (state *researchState) recordSave(current config.Config, fingerprint string) []string {
	if fingerprint == state.Fingerprint {
		state.Settings = current

		return []string{}
	}
	changed := changedFields(state.Settings, current)
	state.Revision++
	state.Changed = time.Now().UTC()
	state.Settings, state.Fingerprint = current, fingerprint

	return changed
}

// hasSettings is false before the first save and for a state file whose
// settings did not decode as a configuration.
func (state *researchState) hasSettings() bool {
	return state.Revision > 0 && state.Settings.Profile != ""
}

func (state *researchState) recordApply(fingerprint string) {
	if state.AppliedFingerprint != fingerprint {
		state.AppliedChanged = time.Now().UTC()
		state.AppliedFingerprint = fingerprint
	}
	state.AppliedRevision = state.Revision
}

func (r *Reporter) attachNetwork(ctx context.Context, report *researchReport, lookup func(context.Context) NetworkIdentity) {
	if lookup == nil || !r.networkEnabled() {
		return
	}
	r.mu.Lock()
	cached, observed := r.networkIdentity, r.networkIdentityAt
	r.mu.Unlock()
	if cached != nil && time.Since(observed) < time.Minute {
		identity := *cached
		report.Network = &identity
		return
	}
	if !r.allow("network", 1) {
		report.Network = &NetworkIdentity{Status: "rate-limited", LookupError: "network identity lookup budget unavailable"}
		if cached != nil {
			identity := *cached
			report.Network = &identity
		}
		return
	}
	identity := lookup(ctx)
	identity.ObservedAt = time.Now().UTC()
	r.mu.Lock()
	r.networkIdentity, r.networkIdentityAt = &identity, identity.ObservedAt
	r.mu.Unlock()
	report.Network = &identity
}

// RecordConfiguration records only saved settings; applied means the caller has
// also completed its service health check. Merely reopening a form is not a change.
func (r *Reporter) RecordConfiguration(ctx context.Context, value config.Config, applied bool, lookup func(context.Context) NetworkIdentity) error {
	if !r.researchEnabled() {
		return nil
	}
	if err := value.Validate(); err != nil {
		return errInvalidResearchConfig
	}
	var report researchReport
	err := withResearchState(r.stateDir, func(state *researchState) error {
		before := state.Revision
		fingerprint, err := fingerprintConfig(state.ID, reportable(value))
		if err != nil {
			return err
		}
		changed := state.recordSave(normalized(value), fingerprint)
		if applied {
			state.recordApply(fingerprint)
		}
		report = reportFromState(*state, "configuration")
		report.PreviousRevision, report.ChangedFields, report.Applied = before, changed, applied

		return nil
	})
	if err != nil {
		return err
	}
	r.attachNetwork(ctx, &report, lookup)
	return r.sendResearch(ctx, report)
}

func sorted(values []string) []string {
	result := slices.Clone(values)
	slices.Sort(result)

	return slices.Compact(result)
}

func reportFromState(state researchState, kind string) researchReport {
	return researchReport{
		Schema: 1, InstallationID: state.ID, Kind: kind, Revision: state.Revision,
		AppliedRevision: state.AppliedRevision,
		LastSavedChange: state.Changed, LastAppliedChange: state.AppliedChanged, Settings: &state.Settings,
	}
}

// RecordObservation reports uptime and restart counts, at most once an hour.
func (r *Reporter) RecordObservation(ctx context.Context, observation Observation) error {
	if !r.researchEnabled() {
		return nil
	}
	var report researchReport
	err := withResearchState(r.stateDir, func(state *researchState) error {
		report = reportFromState(*state, "observation")
		report.Observation = &observation
		if !state.hasSettings() {
			report.Settings = nil
		}

		return nil
	})
	if err != nil {
		return err
	}
	return r.sendResearch(ctx, report)
}

var feedbackAnswers = map[string]bool{"working": true, "problems": true, "not-using": true}

func knownProvider(provider string) bool {
	if provider == "" {
		return true
	}
	if _, ok := config.ProfileByID(provider); ok {
		return true
	}
	_, ok := config.DefaultCatalog().ProviderByID(provider)

	return ok
}

// Feedback records the user's answer to the confirmed-provider prompt.
func (r *Reporter) Feedback(ctx context.Context, answer, provider string) error {
	if !feedbackAnswers[answer] {
		return errFeedbackAnswer
	}
	if !knownProvider(provider) {
		return errUnknownProvider
	}
	if !r.researchEnabled() {
		return errResearchUnavailable
	}
	var report researchReport
	err := withResearchState(r.stateDir, func(state *researchState) error {
		report = reportFromState(*state, "feedback")
		report.Feedback = answer
		report.ConfirmedProvider = provider
		if !state.hasSettings() {
			report.Settings = nil
		}

		return nil
	})
	if err != nil {
		return err
	}
	if err := r.sendResearch(ctx, report); err != nil {
		return errors.Join(errFeedbackNotQueued, err)
	}

	return nil
}

func (r *Reporter) sendResearch(ctx context.Context, report researchReport) error {
	if r.client == nil || r.hub == nil || !r.researchEnabled() || !r.allow("presets", presetsPerMinute) {
		return fmt.Errorf("queue %s: %w", report.Kind, errResearchNotQueued)
	}
	payload, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("encode %s report: %w", report.Kind, err)
	}
	ctx = sentry.SetHubOnContext(ctx, r.hub)
	sentry.NewLogger(ctx).Info().String("research.report", string(payload)).Emit("installation " + report.Kind)
	if !r.Flush() {
		return errResearchNotFlushed
	}
	return nil
}

func (r *Reporter) filterResearchLog(log *sentry.Log) *sentry.Log {
	raw, ok := log.Attributes["research.report"]
	if !ok {
		r.deliveryIssue("presets", "research log lacks its report payload")
		return nil
	}
	text, _ := raw.AsInterface().(string)
	var report researchReport
	if err := json.Unmarshal([]byte(text), &report); err != nil {
		r.deliveryIssue("presets", "decode research payload: "+err.Error())
		return nil
	}
	if !r.networkEnabled() {
		report.Network = nil
	} else if report.Network != nil {
		clean := cleanIdentity(*report.Network)
		report.Network = &clean
	}
	payload, err := json.Marshal(report)
	if err != nil {
		r.deliveryIssue("presets", "encode research payload: "+err.Error())
		return nil
	}
	attrs := r.attributes()
	attrs["research.report"] = attribute.StringValue(string(payload))

	return &sentry.Log{Timestamp: log.Timestamp, TraceID: log.TraceID, SpanID: log.SpanID, Level: sentry.LogLevelInfo, Severity: log.Severity, Body: log.Body, Attributes: attrs}
}

func (r *Reporter) installationID() string {
	if !r.correlationEnabled() || r.stateDir == "" {
		return ""
	}
	fd, err := unix.Open(filepath.Join(r.stateDir, "telemetry-research.json"), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, os.ErrNotExist) {
		return r.createInstallationID()
	}
	if err != nil {
		r.deliveryIssue("identity", "open installation identity: "+err.Error())
		return ""
	}
	file := os.NewFile(uintptr(fd), "research-state")
	defer func() { _ = file.Close() }()
	var state researchState
	if err := json.NewDecoder(io.LimitReader(file, researchStateLimit)).Decode(&state); err != nil {
		r.deliveryIssue("identity", "decode installation identity: "+err.Error())
		return ""
	}
	if !validIdentity(state.ID) {
		r.deliveryIssue("identity", errInvalidIdentity.Error())
		return ""
	}

	return state.ID
}

func operationalReporting(value config.Telemetry) bool {
	return value.Enabled && (value.Errors || value.Logs || value.Metrics || value.Tracing || value.Presets)
}

func (r *Reporter) correlationEnabled() bool {
	if r == nil || r.client == nil || !operationalReporting(r.settings) {
		return false
	}
	if r.configPath == "" {
		return true
	}
	value, err := config.Load(r.configPath)
	if err != nil {
		r.deliveryIssue("identity", "read reporting settings: "+err.Error())
		return false
	}
	return operationalReporting(value.Telemetry)
}

func (r *Reporter) createInstallationID() string {
	var id string
	if err := withResearchState(r.stateDir, func(state *researchState) error {
		id = state.ID
		return nil
	}); err != nil {
		r.deliveryIssue("identity", "persist installation identity: "+err.Error())
		return ""
	}
	return id
}

func validIdentity(value string) bool {
	id, err := hex.DecodeString(value)

	return err == nil && len(id) == researchIdentitySize
}

func newResearchState() (researchState, error) {
	id := make([]byte, researchIdentitySize)
	if _, err := rand.Read(id); err != nil {
		return researchState{}, fmt.Errorf("generate telemetry installation identity: %w", err)
	}

	return researchState{ID: hex.EncodeToString(id)}, nil
}

// ResetIdentity starts a new local history. It cannot delete already sent events.
func ResetIdentity(directory string) error {
	return withResearchState(directory, func(state *researchState) error {
		fresh, err := newResearchState()
		if err != nil {
			return err
		}
		*state = fresh

		return nil
	})
}

type researchLock struct {
	fd   int
	file *os.File
}

// A separate stable lock protects atomic replacement across CLI/daemon processes.
func acquireResearchLock(directory string) (researchLock, error) {
	fd, err := unix.Open(filepath.Join(directory, "telemetry-research.lock"), unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, filemode.PrivateFile)
	if err != nil {
		return researchLock{}, fmt.Errorf("open telemetry state lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), "research-lock")
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()

		return researchLock{}, fmt.Errorf("lock telemetry state: %w", err)
	}

	return researchLock{fd: fd, file: file}, nil
}

func (l researchLock) release() {
	_ = unix.Flock(l.fd, unix.LOCK_UN)
	_ = l.file.Close()
}

// Corruption fails closed; it must not silently manufacture a second installation.
func loadResearchState(path string) (researchState, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	switch {
	case err == nil:
	case errors.Is(err, os.ErrNotExist):
		return newResearchState()
	default:
		return researchState{}, fmt.Errorf("open telemetry state: %w", err)
	}
	file := os.NewFile(uintptr(fd), "research-state")
	data, readErr := io.ReadAll(io.LimitReader(file, researchStateLimit+1))
	_ = file.Close()
	if readErr != nil {
		return researchState{}, fmt.Errorf("read telemetry state: %w", readErr)
	}
	if len(data) > researchStateLimit {
		return researchState{}, errStateTooLarge
	}
	var state researchState
	if err := json.Unmarshal(data, &state); err != nil {
		return researchState{}, fmt.Errorf("parse telemetry state: %w", err)
	}
	if !validIdentity(state.ID) {
		return researchState{}, errInvalidIdentity
	}

	return state, nil
}

func writeResearchState(directory, path string, state researchState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode telemetry state: %w", err)
	}
	file, err := os.CreateTemp(directory, ".telemetry-research-*")
	if err != nil {
		return fmt.Errorf("create telemetry state file: %w", err)
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()

		return fmt.Errorf("write telemetry state: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()

		return fmt.Errorf("sync telemetry state: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close telemetry state: %w", err)
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return fmt.Errorf("replace telemetry state: %w", err)
	}

	return nil
}

func withResearchState(directory string, update func(*researchState) error) error {
	if directory == "" {
		return errMissingStateDir
	}
	if err := os.MkdirAll(directory, filemode.PrivateDir); err != nil {
		return fmt.Errorf("create telemetry state directory: %w", err)
	}
	lock, err := acquireResearchLock(directory)
	if err != nil {
		return err
	}
	defer lock.release()
	path := filepath.Join(directory, "telemetry-research.json")
	state, err := loadResearchState(path)
	if err != nil {
		return err
	}
	if err := update(&state); err != nil {
		return err
	}

	return writeResearchState(directory, path, state)
}
