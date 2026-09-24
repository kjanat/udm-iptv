package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/kjanat/udm-iptv/internal/mroute"
	"github.com/kjanat/udm-iptv/internal/telemetry"
)

type metricSamples struct {
	values map[string]float64
	labels int
}

func (samples *metricSamples) Gauge(_ context.Context, name string, value float64, attributes ...telemetry.Attribute) {
	samples.values[name] = value
	samples.labels += len(attributes)
}

func TestMulticastTelemetryVolumeDoesNotGrowWithRoutes(t *testing.T) {
	table := mroute.Table{Unresolved: 2}
	for range 100 {
		table.Routes = append(table.Routes, mroute.Route{Packets: 10, Bytes: 200, Wrong: 1})
	}
	samples := &metricSamples{values: make(map[string]float64)}
	reportMulticast(t.Context(), samples, table)
	if len(samples.values) != 5 || samples.labels != 0 {
		t.Fatalf("route count added telemetry series: %+v", samples)
	}
	if samples.values["multicast.packets"] != 1000 || samples.values["multicast.bytes"] != 20000 || samples.values["multicast.wrong"] != 100 {
		t.Fatalf("aggregation lost route counters: %v", samples.values)
	}
	// Five multicast samples plus uptime and restarts remain below 7,200/day.
	if daily := 7 * (24 * time.Hour / telemetrySampleInterval); daily != 2016 {
		t.Fatalf("healthy daemon metric volume = %d/day", daily)
	}
}

func TestServiceObservationCollectsDiagnosticsOnlyWhenNeeded(t *testing.T) {
	for _, test := range []struct {
		name     string
		previous uint64
		restarts uint64
		active   bool
		want     bool
	}{
		{name: "healthy", active: true},
		{name: "new restart", restarts: 1, active: true, want: true},
		{name: "unchanged restarts", previous: 1, restarts: 1, active: true},
		{name: "unhealthy", want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			collected := false
			daemon := &Daemon{Diagnostics: func(context.Context) (json.RawMessage, error) {
				collected = true
				return []byte(`{"service":"evidence"}`), nil
			}}
			observation := daemon.serviceObservation(t.Context(), time.Now(), test.previous, test.restarts, test.active)
			if collected != test.want || (len(observation.Snapshot) > 0) != test.want {
				t.Fatal("diagnostic collection did not follow service state")
			}
		})
	}
}
