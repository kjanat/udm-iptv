package service

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	systemd "github.com/coreos/go-systemd/v22/dbus"

	"github.com/kjanat/udm-iptv/internal/mroute"
	"github.com/kjanat/udm-iptv/internal/telemetry"
)

const (
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
	lastObservation := time.Time{}
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Minute)
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
				lastObservation = application.sampleSystemd(ctx, started, lastObservation)
			}
		}
	}()

	return func() { cancel(); <-done }
}

// sampleSystemd reports the restart count and, once an hour, records a
// telemetry observation. It returns the observation time to carry forward.
func (application *Daemon) sampleSystemd(ctx context.Context, started, lastObservation time.Time) time.Time {
	sampleContext, stop := context.WithTimeout(ctx, systemdSampleTimeout)
	defer stop()
	connection, err := systemd.NewSystemConnectionContext(sampleContext)
	if err != nil {
		return lastObservation
	}
	defer connection.Close()
	properties, err := connection.GetAllPropertiesContext(sampleContext, "udm-iptv.service")
	if err != nil {
		return lastObservation
	}
	if restarts, ok := properties["NRestarts"]; ok {
		application.Monitor.Gauge(ctx, "daemon.restarts", float64(ParseCounter(restarts)))
	}
	if time.Since(lastObservation) < time.Hour {
		return lastObservation
	}
	active := properties["ActiveState"] == "active"
	err = application.Monitor.RecordObservation(ctx, telemetry.Observation{
		UptimeSeconds: uint64(time.Since(started).Seconds()),
		Restarts:      ParseCounter(properties["NRestarts"]), Active: active,
		Snapshot: application.diagnostics(ctx),
	})
	if err != nil {
		return lastObservation
	}
	application.Monitor.ObservationCheckIn(active)

	return time.Now()
}

// meterMulticast reports every forwarded multicast route with its
// counters, and the totals, so a stream that stops is visible per group.
func (application *Daemon) meterMulticast(ctx context.Context) {
	table, err := mroute.Read()
	if err != nil {
		return
	}
	application.Monitor.Gauge(ctx, "multicast.routes", float64(len(table.Routes)))
	application.Monitor.Gauge(ctx, "multicast.unresolved", float64(table.Unresolved))
	application.Monitor.Gauge(ctx, "multicast.packets", float64(table.Packets()))
	application.Monitor.Gauge(ctx, "multicast.bytes", float64(table.Bytes()))
	for _, route := range table.Routes {
		attributes := []telemetry.Attribute{
			telemetry.String("group", route.Group.String()), telemetry.String("source", route.Source.String()),
			telemetry.String("input", route.Input), telemetry.String("outputs", strings.Join(route.Outputs, ",")),
		}
		application.Monitor.Gauge(ctx, "multicast.route.packets", float64(route.Packets), attributes...)
		application.Monitor.Gauge(ctx, "multicast.route.bytes", float64(route.Bytes), attributes...)
		application.Monitor.Gauge(ctx, "multicast.route.wrong", float64(route.Wrong), attributes...)
	}
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
