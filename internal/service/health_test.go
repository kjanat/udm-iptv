package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
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

func TestHealthRechecksBeforeSuccess(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"healthy", "disabled", "masked", "inactive", "proxy-exit", "proxy-restart", "daemon-restart", "restart-counter", "read-error"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			err := observeServiceHealth(context.Background(), time.Second, 0, time.Millisecond, func(context.Context) (healthSample, error) {
				calls++
				properties := healthyProperties()
				state := RuntimeState{ProxyPID: 101}
				alive := true
				if calls > 1 {
					switch scenario {
					case "disabled":
						properties["UnitFileState"] = "disabled"
					case "masked":
						properties["LoadState"] = "masked"
					case "inactive":
						properties["ActiveState"] = "inactive"
					case "proxy-exit":
						alive = false
					case "proxy-restart":
						state.ProxyPID++
					case "daemon-restart":
						properties["MainPID"] = uint32(200)
					case "restart-counter":
						properties["NRestarts"] = uint32(3)
					case "read-error":
						return healthSample{}, errors.New("D-Bus unavailable")
					}
				}

				return checkedHealthSample(properties, state, alive)
			})
			if calls != 2 {
				t.Fatalf("expected final recheck, got %d reads", calls)
			}
			if (err == nil) != (scenario == "healthy") {
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
			return healthSample{}, errors.New("start waiting")
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
		return healthSample{}, errors.New("service is masked")
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
