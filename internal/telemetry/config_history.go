package telemetry

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/kjanat/udm-iptv/internal/config"
	"golang.org/x/sys/unix"
)

// SettingsSnapshot deliberately excludes interface names, addresses, MACs,
// DHCP arguments and arbitrary routes. Prefixes are shipped defaults or coarse
// public IPv4 networks; custom host routes and private networks are omitted.
type SettingsSnapshot struct {
	Profile       string   `json:"selected_profile"`
	VLAN          int      `json:"vlan"`
	DHCP          bool     `json:"dhcp"`
	StaticAddress bool     `json:"static_address_configured"`
	DefaultRoute  bool     `json:"allow_default_route"`
	Proxy         string   `json:"proxy"`
	IGMP          int      `json:"igmp_version"`
	QuickLeave    bool     `json:"quickleave"`
	Debug         bool     `json:"proxy_debug"`
	Downstreams   int      `json:"downstream_count"`
	NAT           []string `json:"nat_prefixes"`
	Sources       []string `json:"source_prefixes"`
	CustomNAT     int      `json:"omitted_nat_prefix_count"`
	CustomSources int      `json:"omitted_source_prefix_count"`
}

func snapshot(value config.Config) SettingsSnapshot {
	profile := "custom"
	if _, ok := config.ProfileByID(value.Profile); ok {
		profile = value.Profile
	}
	proxy := "unknown"
	if value.Proxy.Program == "improxy" || value.Proxy.Program == "igmpproxy" {
		proxy = value.Proxy.Program
	}
	result := SettingsSnapshot{
		Profile: profile, VLAN: value.WAN.VLAN, DHCP: value.WAN.DHCP,
		StaticAddress: value.WAN.StaticAddress != "", DefaultRoute: value.WAN.AllowDefaultRoute,
		Proxy: proxy, IGMP: value.Proxy.IGMPVersion, QuickLeave: value.Proxy.QuickLeave,
		Debug: value.Proxy.Debug, Downstreams: len(value.LAN.Interfaces),
	}
	known := map[string]bool{}
	for _, p := range config.Profiles() {
		for _, prefix := range append(slices.Clone(p.Config.WAN.NATDestinations), p.Config.Proxy.SourceRanges...) {
			known[prefix] = true
		}
	}
	selectPrefixes := func(values []string) ([]string, int) {
		var allowed []string
		var omitted int
		for _, prefix := range values {
			parsed, err := netip.ParsePrefix(prefix)
			if err == nil && (known[prefix] || safePublicPrefix(parsed)) {
				allowed = append(allowed, parsed.Masked().String())
			} else {
				omitted++
			}
		}
		slices.Sort(allowed)

		return slices.Compact(allowed), omitted
	}
	result.NAT, result.CustomNAT = selectPrefixes(value.WAN.NATDestinations)
	result.Sources, result.CustomSources = selectPrefixes(value.Proxy.SourceRanges)

	return result
}

func safePublicPrefix(prefix netip.Prefix) bool {
	// Keeping at least 256 IPv4 addresses avoids reporting custom host routes.
	// Small prefixes could span non-public blocks, so accept only /8 through /24
	// with no overlap with the excluded private/special-use ranges.
	if !prefix.Addr().Is4() || prefix.Bits() < 8 || prefix.Bits() > 24 {
		return false
	}
	start := prefix.Masked().Addr()
	for _, block := range []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4"} {
		reserved := netip.MustParsePrefix(block)
		if prefix.Masked().Contains(reserved.Addr()) || reserved.Contains(start) {
			return false
		}
	}

	return publicAddress(start)
}

type researchState struct {
	ID                 string           `json:"id"`
	Revision           uint64           `json:"revision"`
	Fingerprint        string           `json:"fingerprint"`
	AppliedFingerprint string           `json:"applied_fingerprint"`
	AppliedRevision    uint64           `json:"applied_revision"`
	Changed            time.Time        `json:"last_saved_change"`
	AppliedChanged     time.Time        `json:"last_applied_change"`
	Settings           SettingsSnapshot `json:"settings"`
}

// researchReport is private so ordinary SDK events cannot bypass the allowlist.
type researchReport struct {
	Schema            int               `json:"schema"`
	InstallationID    string            `json:"installation_id"`
	Kind              string            `json:"kind"`
	Revision          uint64            `json:"revision"`
	PreviousRevision  uint64            `json:"previous_revision"`
	AppliedRevision   uint64            `json:"applied_revision"`
	ChangedFields     []string          `json:"changed_fields,omitempty"`
	LastSavedChange   time.Time         `json:"last_saved_change"`
	LastAppliedChange time.Time         `json:"last_applied_change"`
	Settings          *SettingsSnapshot `json:"settings,omitempty"`
	Applied           bool              `json:"applied"`
	Feedback          string            `json:"feedback,omitempty"`
	ConfirmedProvider string            `json:"user_confirmed_provider,omitempty"`
	Network           *NetworkIdentity  `json:"network,omitempty"`
	Observation       *Observation      `json:"observation,omitempty"`
}

// Observation describes measurements, never an inferred customer satisfaction.
type Observation struct {
	UptimeSeconds uint64 `json:"process_uptime_seconds"`
	Restarts      uint64 `json:"systemd_restarts"`
	Active        bool   `json:"service_active"`
}

func (r *Reporter) researchEnabled() bool {
	if r == nil || r.client == nil || !r.settings.Enabled || !r.settings.Presets {
		return false
	}
	if r.configPath == "" {
		return true
	}
	value, err := config.Load(r.configPath)

	return err == nil && value.Telemetry.Enabled && value.Telemetry.Presets
}

// ResearchEnabled checks the saved master switch before each observation.
func (r *Reporter) ResearchEnabled() bool { return r.researchEnabled() }

// RecordConfiguration records only saved settings; applied means the caller has
// also completed its service health check. Merely reopening a form is not a change.
func (r *Reporter) RecordConfiguration(ctx context.Context, value config.Config, applied bool, lookup func(context.Context) NetworkIdentity) error {
	if !r.researchEnabled() {
		return nil
	}
	if err := value.Validate(); err != nil {
		return errors.New("invalid research configuration")
	}
	var report researchReport
	err := withResearchState(r.stateDir, func(state *researchState) error {
		before := state.Revision
		// The full fingerprint stays local, is keyed per installation and excludes
		// telemetry preferences. It detects changes to omitted settings safely.
		value.Telemetry = config.Telemetry{}
		value.WAN.NATDestinations = sorted(value.WAN.NATDestinations)
		value.Proxy.SourceRanges = sorted(value.Proxy.SourceRanges)
		value.LAN.Interfaces = sorted(value.LAN.Interfaces)
		value.WAN.StaticRoutes = sorted(value.WAN.StaticRoutes)
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		hash := hmac.New(sha256.New, []byte(state.ID))
		_, _ = hash.Write(data)
		fingerprint := hex.EncodeToString(hash.Sum(nil))
		current := snapshot(value)
		changed := []string{}
		if fingerprint != state.Fingerprint {
			oldFields, newFields := reflect.ValueOf(state.Settings), reflect.ValueOf(current)
			for i := 0; i < oldFields.NumField(); i++ {
				if !reflect.DeepEqual(oldFields.Field(i).Interface(), newFields.Field(i).Interface()) {
					changed = append(changed, oldFields.Type().Field(i).Tag.Get("json"))
				}
			}
			if len(changed) == 0 {
				changed = append(changed, "unreported_settings")
			}
			state.Revision++
			state.Changed = time.Now().UTC()
			state.Settings, state.Fingerprint = current, fingerprint
		}
		if applied && state.AppliedFingerprint != fingerprint {
			state.AppliedChanged = time.Now().UTC()
			state.AppliedFingerprint = fingerprint
		}
		if applied {
			state.AppliedRevision = state.Revision
		}
		report = reportFromState(*state, "configuration")
		report.PreviousRevision, report.ChangedFields, report.Applied = before, changed, applied

		return nil
	})
	if err != nil {
		return err
	}
	if lookup != nil && r.networkEnabled() && r.allow("network", 1) {
		identity := lookup(ctx)
		report.Network = &identity
	}
	r.sendResearch(report)

	return nil
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

func (r *Reporter) RecordObservation(observation Observation) error {
	if !r.researchEnabled() {
		return nil
	}
	var report researchReport
	err := withResearchState(r.stateDir, func(state *researchState) error {
		report = reportFromState(*state, "observation")
		report.Observation = &observation
		if state.Revision == 0 {
			report.Settings = nil
		}

		return nil
	})
	if err != nil {
		return err
	}
	r.sendResearch(report)

	return nil
}

func (r *Reporter) Feedback(answer, provider string) error {
	if answer != "working" && answer != "problems" && answer != "not-using" {
		return errors.New("choose working, problems or not-using")
	}
	if provider != "" && provider != "xs4all" && provider != "freedom" {
		if _, ok := config.ProfileByID(provider); !ok {
			return errors.New("unknown provider; use a profile ID, xs4all or freedom")
		}
	}
	if !r.researchEnabled() {
		return errors.New("preset research is disabled or unavailable")
	}
	var report researchReport
	err := withResearchState(r.stateDir, func(state *researchState) error {
		report = reportFromState(*state, "feedback")
		report.Feedback = answer
		report.ConfirmedProvider = provider
		if state.Revision == 0 {
			report.Settings = nil
		}

		return nil
	})
	if err != nil {
		return err
	}
	if !r.sendResearch(report) {
		return errors.New("feedback was not queued: reporting disabled or rate limit reached")
	}

	return nil
}

func (r *Reporter) sendResearch(report researchReport) bool {
	event := sentry.NewEvent()
	event.Transaction, event.Level = "installation.report", sentry.LevelInfo
	event.Contexts = map[string]sentry.Context{"research": {"report": report}}

	return r.hub.CaptureEvent(event) != nil
}

func (r *Reporter) filterResearch(event *sentry.Event) *sentry.Event {
	report, ok := event.Contexts["research"]["report"].(researchReport)
	if !ok || !r.researchEnabled() || !r.allow("presets", 5) {
		return nil
	}
	if !r.networkEnabled() {
		report.Network = nil
	} else if report.Network != nil {
		clean := cleanIdentity(*report.Network)
		report.Network = &clean
	}

	return &sentry.Event{
		EventID: event.EventID, Timestamp: event.Timestamp,
		Platform: "go", Release: r.release, Level: sentry.LevelInfo,
		Message: "Installation " + report.Kind, Transaction: "installation.report",
		User: sentry.User{ID: report.InstallationID},
		Tags: r.metadata, Contexts: map[string]sentry.Context{"research": {"report": report}},
	}
}

func (r *Reporter) installationID() string {
	if !r.researchEnabled() || r.stateDir == "" {
		return ""
	}
	fd, err := unix.Open(filepath.Join(r.stateDir, "telemetry-research.json"), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return ""
	}
	file := os.NewFile(uintptr(fd), "research-state")
	defer func() { _ = file.Close() }()
	var state researchState
	if err := json.NewDecoder(io.LimitReader(file, 65536)).Decode(&state); err != nil {
		return ""
	}
	id, err := hex.DecodeString(state.ID)
	if err != nil || len(id) != 16 {
		return ""
	}

	return state.ID
}

// ResetIdentity starts a new local history. It cannot delete already sent events.
func ResetIdentity(directory string) error {
	return withResearchState(directory, func(state *researchState) error {
		id := make([]byte, 16)
		if _, err := rand.Read(id); err != nil {
			return err
		}
		*state = researchState{ID: hex.EncodeToString(id)}

		return nil
	})
}

// A separate stable lock protects atomic replacement across CLI/daemon processes.
// Corruption fails closed; it must not silently manufacture a second installation.
func withResearchState(directory string, update func(*researchState) error) error {
	if directory == "" {
		return errors.New("telemetry state directory is missing")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	fd, err := unix.Open(filepath.Join(directory, "telemetry-research.lock"), unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	lock := os.NewFile(uintptr(fd), "research-lock")
	defer func() { _ = lock.Close() }()
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return err
	}
	defer func() { _ = unix.Flock(fd, unix.LOCK_UN) }()
	state := researchState{}
	path := filepath.Join(directory, "telemetry-research.json")
	fd, err = unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err == nil {
		file := os.NewFile(uintptr(fd), "research-state")
		data, readErr := io.ReadAll(io.LimitReader(file, 65537))
		_ = file.Close()
		if readErr != nil {
			return readErr
		}
		if len(data) > 65536 {
			return errors.New("telemetry state exceeds size limit")
		}
		if err := json.Unmarshal(data, &state); err != nil {
			return err
		}
		id, err := hex.DecodeString(state.ID)
		if err != nil || len(id) != 16 {
			return errors.New("invalid telemetry identity")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else {
		id := make([]byte, 16)
		if _, err := rand.Read(id); err != nil {
			return err
		}
		state.ID = hex.EncodeToString(id)
	}
	if err := update(&state); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".telemetry-research-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()

		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()

		return err
	}
	if err := file.Close(); err != nil {
		return err
	}

	return os.Rename(file.Name(), path)
}
