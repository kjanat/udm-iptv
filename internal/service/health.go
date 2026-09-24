package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	systemd "github.com/coreos/go-systemd/v22/dbus"
)

// healthCheckInterval is how often WaitHealthy samples the service's state.
const healthCheckInterval = 250 * time.Millisecond

var (
	errUnexpectedServiceState = errors.New("unexpected service state")
	errProxyNotRunning        = errors.New("proxy process is not running")
	errNoMainProcess          = errors.New("service has no main process")
	errServiceRestarted       = errors.New("service or proxy restarted during the health check")
)

// WaitHealthy waits for udm-iptv.service to become active and stay that way
// for stable, failing if it does not become ready within startup.
func WaitHealthy(ctx context.Context, startup, stable time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, startup+stable)
	defer cancel()
	connection, err := systemd.NewSystemConnectionContext(ctx)
	if err != nil {
		return fmt.Errorf("connect to systemd: %w", err)
	}
	defer connection.Close()

	return observeServiceHealth(ctx, startup, stable, healthCheckInterval, func(ctx context.Context) (healthSample, error) {
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
			return healthSample{}, fmt.Errorf("%w: %s is %v; expected %s", errUnexpectedServiceState, field.name, properties[field.name], field.want)
		}
	}
	if !proxyAlive || state.ProxyPID <= 0 {
		return healthSample{}, errProxyNotRunning
	}
	sample := healthSample{
		mainPID: ParseCounter(properties["MainPID"]), proxyPID: state.ProxyPID,
		restarts: ParseCounter(properties["NRestarts"]), startedAt: state.StartedAt,
	}
	if sample.mainPID == 0 {
		return healthSample{}, errNoMainProcess
	}

	return sample, nil
}

func observeServiceHealth(ctx context.Context, startup, stable, interval time.Duration, read func(context.Context) (healthSample, error)) error {
	deadline := time.Now().Add(startup)
	var initial healthSample
	var readyAt time.Time
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("wait for the service to become healthy: %w", err)
		}
		current, err := read(ctx)
		if ctx.Err() != nil {
			return fmt.Errorf("wait for the service to become healthy: %w", ctx.Err())
		}
		switch {
		case !readyAt.IsZero():
			if settled, verdict := stillHealthy(current, initial, err, time.Since(readyAt), stable); settled {
				return verdict
			}
		case err == nil:
			initial, readyAt = current, time.Now()
		case !time.Now().Before(deadline):
			return fmt.Errorf("service did not become ready within %s: %w", startup, err)
		}
		if err := waitOrCancel(ctx, healthInterval(interval, readyAt, deadline, stable)); err != nil {
			return err
		}
	}
}

// stillHealthy reports whether an observation settles the check, and how.
func stillHealthy(current, initial healthSample, err error, observed, stable time.Duration) (bool, error) {
	switch {
	case err != nil:
		return true, fmt.Errorf("service did not remain healthy: %w", err)
	case current != initial:
		return true, errServiceRestarted
	case observed >= stable:
		return true, nil
	}

	return false, nil
}

func healthInterval(interval time.Duration, readyAt, deadline time.Time, stable time.Duration) time.Duration {
	if readyAt.IsZero() {
		return min(interval, max(0, time.Until(deadline)))
	}

	return min(interval, max(0, stable-time.Since(readyAt)))
}

func waitOrCancel(ctx context.Context, wait time.Duration) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("wait for the service to become healthy: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}
