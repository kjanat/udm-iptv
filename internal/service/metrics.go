package service

import (
	"context"
	"encoding/json"
	"time"

	systemd "github.com/coreos/go-systemd/v22/dbus"

	"github.com/kjanat/udm-iptv/internal/mroute"
	"github.com/kjanat/udm-iptv/internal/telemetry"
)

const (
	// telemetrySampleInterval keeps healthy metrics below the daily backstop.
	telemetrySampleInterval = 5 * time.Minute
	// systemdSampleTimeout bounds each per-tick systemd property query.
	systemdSampleTimeout = 2 * time.Second
	// snapshotTimeout bounds the diagnostics collected for an observation.
	snapshotTimeout = 10 * time.Second
)

func (application *Daemon) startTelemetryMetrics(parent context.Context) func() {
	if !application.Monitor.MetricsEnabled() && !application.Monitor.ResearchEnabled() {
		return func() {}
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	started := time.Now()
	state := metricsState{}
	go func() {
		defer close(done)
		ticker := time.NewTicker(telemetrySampleInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !application.Monitor.MetricsEnabled() && !application.Monitor.ResearchEnabled() {
					continue
				}
				application.Monitor.Gauge(ctx, "daemon.uptime", time.Since(started).Seconds())
				application.meterMulticast(ctx)
				application.sampleSystemd(ctx, started, &state)
			}
		}
	}()

	return func() { cancel(); <-done }
}

type metricsState struct {
	lastObservation time.Time
	restarts        uint64
}

// sampleSystemd reports the restart count and records an hourly observation.
func (application *Daemon) sampleSystemd(ctx context.Context, started time.Time, state *metricsState) {
	sampleContext, stop := context.WithTimeout(ctx, systemdSampleTimeout)
	defer stop()
	connection, err := systemd.NewSystemConnectionContext(sampleContext)
	if err != nil {
		return
	}
	defer connection.Close()
	properties, err := connection.GetAllPropertiesContext(sampleContext, "udm-iptv.service")
	if err != nil {
		return
	}
	if restarts, ok := properties["NRestarts"]; ok {
		application.Monitor.Gauge(ctx, "daemon.restarts", float64(ParseCounter(restarts)))
	}
	if time.Since(state.lastObservation) < time.Hour {
		return
	}
	active := properties["ActiveState"] == "active"
	restarts := ParseCounter(properties["NRestarts"])
	observation := application.serviceObservation(ctx, started, state.restarts, restarts, active)
	err = application.Monitor.RecordObservation(ctx, observation)
	if err != nil {
		return
	}
	application.Monitor.ObservationCheckIn(active)

	state.lastObservation, state.restarts = time.Now(), restarts
}

// serviceObservation includes expensive diagnostics only for unhealthy service
// state or an increase in the restart counter since the previous observation.
func (application *Daemon) serviceObservation(ctx context.Context, started time.Time, previousRestarts, restarts uint64, active bool) telemetry.Observation {
	observation := telemetry.Observation{
		UptimeSeconds: uint64(time.Since(started).Seconds()), Restarts: restarts, Active: active,
	}
	if !active || restarts > previousRestarts {
		observation.Snapshot = application.diagnostics(ctx)
	}
	return observation
}

// meterMulticast reports totals independent of route count. Detailed route
// counters remain in local diagnostics and failure snapshots.
func (application *Daemon) meterMulticast(ctx context.Context) {
	table, err := mroute.Read()
	if err != nil {
		return
	}
	reportMulticast(ctx, application.Monitor, table)
}

type metricReporter interface {
	Gauge(context.Context, string, float64, ...telemetry.Attribute)
}

func reportMulticast(ctx context.Context, reporter metricReporter, table mroute.Table) {
	reporter.Gauge(ctx, "multicast.routes", float64(len(table.Routes)))
	reporter.Gauge(ctx, "multicast.unresolved", float64(table.Unresolved))
	reporter.Gauge(ctx, "multicast.packets", float64(table.Packets()))
	reporter.Gauge(ctx, "multicast.bytes", float64(table.Bytes()))
	var wrong uint64
	for _, route := range table.Routes {
		wrong += route.Wrong
	}
	reporter.Gauge(ctx, "multicast.wrong", float64(wrong))
}

// diagnostics collects the snapshot the observation carries, when the
// daemon was given a collector.
func (application *Daemon) diagnostics(ctx context.Context) json.RawMessage {
	if application.Diagnostics == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, snapshotTimeout)
	defer cancel()
	snapshot, err := application.Diagnostics(ctx)
	if err != nil {
		return nil
	}

	return snapshot
}
