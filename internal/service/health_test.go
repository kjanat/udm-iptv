package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

var (
	errDBusUnavailable = errors.New("D-Bus unavailable")
	errStartWaiting    = errors.New("start waiting")
	errServiceMasked   = errors.New("service is masked")
)

func healthyProperties() map[string]any {
	return map[string]any{
		"LoadState": "loaded", "UnitFileState": "enabled", "ActiveState": "active",
		"SubState": "running", "MainPID": uint32(100), "NRestarts": uint32(2),
	}
}

func TestHealthRequiresEnabledUnmaskedRunningService(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		field string
		value any
	}{
		{"LoadState", "masked"},
		{"LoadState", "not-found"},
		{"UnitFileState", "disabled"},
		{"UnitFileState", "masked"},
		{"UnitFileState", "enabled-runtime"},
		{"UnitFileState", nil},
		{"ActiveState", "failed"},
		{"ActiveState", "activating"},
		{"SubState", "exited"},
		{"MainPID", uint32(0)},
	} {
		properties := healthyProperties()
		properties[test.field] = test.value
		if _, err := checkedHealthSample(properties, RuntimeState{ProxyPID: 101}, true); err == nil {
			t.Errorf("accepted unhealthy %s=%v", test.field, test.value)
		}
	}
	if _, err := checkedHealthSample(healthyProperties(), RuntimeState{ProxyPID: 101}, false); err == nil {
		t.Fatal("accepted dead proxy")
	}
	sample, err := checkedHealthSample(healthyProperties(), RuntimeState{ProxyPID: 101}, true)
	if err != nil || sample.restarts != 2 {
		t.Fatalf("healthy service: %+v, %v", sample, err)
	}
}

// healthScenario is what a second reading of the service reports.
type healthScenario struct {
	name      string
	property  string
	value     any
	proxyExit bool
	proxyPID  int
	readError bool
	healthy   bool
}

func (scenario healthScenario) read() (healthSample, error) {
	if scenario.readError {
		return healthSample{}, errDBusUnavailable
	}
	properties := healthyProperties()
	if scenario.property != "" {
		properties[scenario.property] = scenario.value
	}
	state := RuntimeState{ProxyPID: 101}
	if scenario.proxyPID != 0 {
		state.ProxyPID = scenario.proxyPID
	}

	return checkedHealthSample(properties, state, !scenario.proxyExit)
}

func TestHealthRechecksBeforeSuccess(t *testing.T) {
	t.Parallel()
	for _, test := range []healthScenario{
		{name: "healthy", healthy: true},
		{name: "disabled", property: "UnitFileState", value: "disabled"},
		{name: "masked", property: "LoadState", value: "masked"},
		{name: "inactive", property: "ActiveState", value: "inactive"},
		{name: "proxy-exit", proxyExit: true},
		{name: "proxy-restart", proxyPID: 102},
		{name: "daemon-restart", property: "MainPID", value: uint32(200)},
		{name: "restart-counter", property: "NRestarts", value: uint32(3)},
		{name: "read-error", readError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			err := observeServiceHealth(context.Background(), time.Second, 0, time.Millisecond, func(context.Context) (healthSample, error) {
				calls++
				if calls == 1 {
					return checkedHealthSample(healthyProperties(), RuntimeState{ProxyPID: 101}, true)
				}

				return test.read()
			})
			if calls != 2 {
				t.Fatalf("expected final recheck, got %d reads", calls)
			}
			if (err == nil) != test.healthy {
				t.Fatalf("unexpected result: %v", err)
			}
		})
	}
}

func TestHealthWaitsForStartupAndObservation(t *testing.T) {
	t.Parallel()
	calls := 0
	start := time.Now()
	stable := 5 * time.Millisecond
	err := observeServiceHealth(context.Background(), time.Second, stable, time.Millisecond, func(context.Context) (healthSample, error) {
		calls++
		if calls == 1 {
			return healthSample{}, errStartWaiting
		}

		return checkedHealthSample(healthyProperties(), RuntimeState{ProxyPID: 101}, true)
	})
	if err != nil || calls < 3 || time.Since(start) < stable {
		t.Fatalf("observation ended early: calls=%d, err=%v", calls, err)
	}
}

func TestHealthTimeoutIncludesReason(t *testing.T) {
	t.Parallel()
	err := observeServiceHealth(context.Background(), 0, 0, time.Millisecond, func(context.Context) (healthSample, error) {
		return healthSample{}, errServiceMasked
	})
	if err == nil || !strings.Contains(err.Error(), "masked") {
		t.Fatalf("missing failure reason: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = observeServiceHealth(ctx, time.Second, time.Second, time.Millisecond, func(context.Context) (healthSample, error) {
		t.Fatal("read after cancellation")

		return healthSample{}, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}
