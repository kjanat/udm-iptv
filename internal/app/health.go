package app

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type healthSample struct {
	mainPID   uint64
	proxyPID  int
	restarts  uint64
	startedAt time.Time
}

func checkedHealthSample(properties map[string]any, state runtimeState, proxyAlive bool) (healthSample, error) {
	for _, field := range []struct{ name, want string }{
		{"LoadState", "loaded"},
		{"UnitFileState", "enabled"},
		{"ActiveState", "active"},
		{"SubState", "running"},
	} {
		if properties[field.name] != field.want {
			return healthSample{}, fmt.Errorf("service %s is %v; expected %s", field.name, properties[field.name], field.want)
		}
	}
	if !proxyAlive || state.ProxyPID <= 0 {
		return healthSample{}, errors.New("proxy process is not running")
	}
	sample := healthSample{
		mainPID: parseUint(properties["MainPID"]), proxyPID: state.ProxyPID,
		restarts: parseUint(properties["NRestarts"]), startedAt: state.StartedAt,
	}
	if sample.mainPID == 0 {
		return healthSample{}, errors.New("service has no main process")
	}
	return sample, nil
}

func observeServiceHealth(ctx context.Context, startup, stable, interval time.Duration, read func(context.Context) (healthSample, error)) error {
	deadline := time.Now().Add(startup)
	var initial healthSample
	var readyAt time.Time
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, err := read(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if readyAt.IsZero() {
			if err == nil {
				initial, readyAt = current, time.Now()
			} else if !time.Now().Before(deadline) {
				return fmt.Errorf("service did not become ready within %s: %w", startup, err)
			}
		} else {
			if err != nil {
				return fmt.Errorf("service did not remain healthy: %w", err)
			}
			if current != initial {
				return errors.New("service or proxy restarted during the health check")
			}
			if time.Since(readyAt) >= stable {
				return nil
			}
		}
		wait := interval
		if !readyAt.IsZero() {
			wait = min(wait, max(0, stable-time.Since(readyAt)))
		} else {
			wait = min(wait, max(0, time.Until(deadline)))
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
