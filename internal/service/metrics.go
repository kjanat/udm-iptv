package service

import (
	"bufio"
	"context"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	systemd "github.com/coreos/go-systemd/v22/dbus"
	"github.com/kjanat/udm-iptv/internal/telemetry"
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
				if file, err := os.Open("/proc/net/ip_mr_cache"); err == nil {
					routes, packets, err := multicastCounters(io.LimitReader(file, 1<<20))
					_ = file.Close()
					if err == nil {
						application.Monitor.Gauge(ctx, "multicast.routes", float64(routes))
						application.Monitor.Gauge(ctx, "multicast.packets", float64(packets))
					}
				}
				sampleContext, stop := context.WithTimeout(ctx, 2*time.Second)
				if connection, err := systemd.NewSystemConnectionContext(sampleContext); err == nil {
					if properties, err := connection.GetAllPropertiesContext(sampleContext, "udm-iptv.service"); err == nil {
						if restarts, ok := properties["NRestarts"]; ok {
							application.Monitor.Gauge(ctx, "daemon.restarts", float64(ParseCounter(restarts)))
						}
						if time.Since(lastObservation) >= time.Hour {
							err := application.Monitor.RecordObservation(telemetry.Observation{
								UptimeSeconds: uint64(time.Since(started).Seconds()),
								Restarts:      ParseCounter(properties["NRestarts"]), Active: properties["ActiveState"] == "active",
							})
							if err == nil {
								lastObservation = time.Now()
							}
						}
					}
					connection.Close()
				}
				stop()
			}
		}
	}()

	return func() { cancel(); <-done }
}

func multicastCounters(reader io.Reader) (int, uint64, error) {
	scanner := bufio.NewScanner(reader)
	if !scanner.Scan() {
		return 0, 0, io.ErrUnexpectedEOF
	}
	var routes int
	var packets uint64
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 {
			return 0, 0, io.ErrUnexpectedEOF
		}
		count, err := strconv.ParseUint(fields[3], 10, 64)
		if err != nil {
			return 0, 0, err
		}
		routes++
		packets += count
	}

	return routes, packets, scanner.Err()
}
