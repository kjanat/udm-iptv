package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	systemd "github.com/coreos/go-systemd/v22/dbus"
)

func WaitHealthy(ctx context.Context, startup, stable time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, startup+stable)
	defer cancel()
	connection, err := systemd.NewSystemConnectionContext(ctx)
	if err != nil {
		return err
	}
	defer connection.Close()

	return observeServiceHealth(ctx, startup, stable, 250*time.Millisecond, func(ctx context.Context) (healthSample, error) {
		properties, err := connection.GetAllPropertiesContext(ctx, "udm-iptv.service")
		if err != nil {
			return healthSample{}, fmt.Errorf("read service state: %w", err)
		}
		state, err := ReadRuntimeState()
		if err != nil {
			return healthSample{}, fmt.Errorf("read proxy state: %w", err)
		}

		return checkedHealthSample(properties, state, processExists(state.ProxyPID))
	})
}

type healthSample struct {
	mainPID   uint64
	proxyPID  int
	restarts  uint64
	startedAt time.Time
}

func checkedHealthSample(properties map[string]any, state RuntimeState, proxyAlive bool) (healthSample, error) {
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
		mainPID: ParseCounter(properties["MainPID"]), proxyPID: state.ProxyPID,
		restarts: ParseCounter(properties["NRestarts"]), startedAt: state.StartedAt,
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
		switch {
		case !readyAt.IsZero() && err != nil:
			return fmt.Errorf("service did not remain healthy: %w", err)
		case !readyAt.IsZero() && current != initial:
			return errors.New("service or proxy restarted during the health check")
		case !readyAt.IsZero() && time.Since(readyAt) >= stable:
			return nil
		case readyAt.IsZero() && err == nil:
			initial, readyAt = current, time.Now()
		case readyAt.IsZero() && !time.Now().Before(deadline):
			return fmt.Errorf("service did not become ready within %s: %w", startup, err)
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
