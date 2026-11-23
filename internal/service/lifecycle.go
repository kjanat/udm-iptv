package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	systemd "github.com/coreos/go-systemd/v22/dbus"
	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
)

// Unit is the installed IPTV service.
const Unit = "udm-iptv.service"

const (
	recoveryTimeout = 30 * time.Second
	timerActive     = "active"
)

var (
	errJobNotDone      = errors.New("systemd job finished with status")
	errInvalidPause    = errors.New("pause must be at least one microsecond")
	errInvalidDeadline = errors.New("invalid resume timer deadline")
)

// LifecycleConnection is the systemd interface used to control a service and its timer.
type LifecycleConnection interface {
	StartUnitContext(context.Context, string, string, chan<- string) (int, error)
	StopUnitContext(context.Context, string, string, chan<- string) (int, error)
	RestartUnitContext(context.Context, string, string, chan<- string) (int, error)
	StartTransientUnitContext(context.Context, string, string, []systemd.Property, chan<- string) (int, error)
	GetUnitPropertiesContext(context.Context, string) (map[string]any, error)
	GetUnitTypePropertiesContext(context.Context, string, string) (map[string]any, error)
}

// Lifecycle keeps explicit service operations consistent with scheduled resumes.
type Lifecycle struct {
	Connection LifecycleConnection
	Unit       string
}

func (l Lifecycle) timer() string { return strings.TrimSuffix(l.Unit, ".service") + ".timer" }

type unitJob func(context.Context, string, string, chan<- string) (int, error)

func runUnitJob(ctx context.Context, job unitJob, unit string) error {
	result := make(chan string, 1)
	if _, err := job(ctx, unit, "replace", result); err != nil {
		return fmt.Errorf("submit job for %s: %w", unit, err)
	}
	return waitUnitJob(ctx, result, unit)
}

func waitUnitJob(ctx context.Context, result <-chan string, unit string) error {
	select {
	case status := <-result:
		if status != statusDone {
			return fmt.Errorf("%w %s for %s", errJobNotDone, status, unit)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for %s: %w", unit, ctx.Err())
	}
}

// CancelResume cancels a pending return, including before uninstalling the service.
func (l Lifecycle) CancelResume(ctx context.Context) error {
	err := runUnitJob(ctx, l.Connection.StopUnitContext, l.timer())
	if err == nil || NoSuchUnit(err) {
		return nil
	}
	return fmt.Errorf("cancel scheduled start: %w", err)
}

// Start cancels a pause and starts the service without interrupting an active one.
func (l Lifecycle) Start(ctx context.Context) error {
	if err := l.CancelResume(ctx); err != nil {
		return err
	}
	return runUnitJob(ctx, l.Connection.StartUnitContext, l.Unit)
}

// Restart cancels a pause and restarts the service.
func (l Lifecycle) Restart(ctx context.Context) error {
	if err := l.CancelResume(ctx); err != nil {
		return err
	}
	return runUnitJob(ctx, l.Connection.RestartUnitContext, l.Unit)
}

// Stop cancels a pause before stopping, so a removed service cannot be revived by a timer.
func (l Lifecycle) Stop(ctx context.Context) error {
	if err := l.CancelResume(ctx); err != nil {
		return err
	}
	return runUnitJob(ctx, l.Connection.StopUnitContext, l.Unit)
}

// Pause stops the service temporarily. Scheduling failure attempts to restore it,
// even when the caller canceled the operation after the service stopped.
func (l Lifecycle) Pause(ctx context.Context, after time.Duration) error {
	if after < time.Microsecond {
		return errInvalidPause
	}
	if err := l.Stop(ctx); err != nil {
		return err
	}
	if err := l.scheduleResume(ctx, after); err != nil {
		recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), recoveryTimeout)
		defer cancel()
		// Cancel any partially submitted timer, but still try to start if that fails.
		cancelErr := l.CancelResume(recovery)
		startErr := runUnitJob(recovery, l.Connection.StartUnitContext, l.Unit)
		if recoveryErr := errors.Join(cancelErr, startErr); recoveryErr != nil {
			return errors.Join(fmt.Errorf("schedule resume: %w", err), fmt.Errorf("recovery incomplete; check status and run 'udm-iptv start': %w", recoveryErr))
		}
		return fmt.Errorf("pause failed; %s started again: %w", l.Unit, err)
	}
	return nil
}

func (l Lifecycle) scheduleResume(ctx context.Context, after time.Duration) error {
	result := make(chan string, 1)
	if _, err := l.Connection.StartTransientUnitContext(ctx, l.timer(), "replace", resumeTimerProperties(after), result); err != nil {
		return fmt.Errorf("schedule %s in %s: %w", l.Unit, after, err)
	}
	return waitUnitJob(ctx, result, l.timer())
}

type monotonicTimer struct {
	Base string
	Usec uint64
}

func resumeTimerProperties(after time.Duration) []systemd.Property {
	return []systemd.Property{
		systemd.PropDescription("Resume IPTV after a temporary stop"),
		{Name: "AccuracyUSec", Value: dbus.MakeVariant(uint64(time.Second / time.Microsecond))},
		{Name: "RemainAfterElapse", Value: dbus.MakeVariant(false)},
		{Name: "TimersMonotonic", Value: dbus.MakeVariant([]monotonicTimer{{Base: "OnActiveSec", Usec: pauseMicroseconds(after)}})},
	}
}

func pauseMicroseconds(after time.Duration) uint64 {
	if after < 0 {
		return 0
	}
	return uint64(after / time.Microsecond)
}

// ResumeAt returns the pending timer's deadline, or zero when no start is scheduled.
func (l Lifecycle) ResumeAt(ctx context.Context) (time.Time, error) {
	unit, err := l.Connection.GetUnitPropertiesContext(ctx, l.timer())
	if NoSuchUnit(err) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("read resume timer: %w", err)
	}
	if unit["ActiveState"] != timerActive || unit["SubState"] != "waiting" {
		return time.Time{}, nil
	}
	properties, err := l.Connection.GetUnitTypePropertiesContext(ctx, l.timer(), "Timer")
	if err != nil {
		return time.Time{}, fmt.Errorf("read resume deadline: %w", err)
	}
	deadline, _ := properties["NextElapseUSecMonotonic"].(uint64)
	if deadline == 0 || deadline == ^uint64(0) {
		return time.Time{}, nil
	}
	if deadline > uint64(math.MaxInt64/int64(time.Microsecond)) {
		return time.Time{}, errInvalidDeadline
	}
	var now unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &now); err != nil {
		return time.Time{}, fmt.Errorf("read monotonic clock: %w", err)
	}
	remaining := time.Duration(deadline)*time.Microsecond - time.Duration(now.Nano())
	return time.Now().Add(remaining), nil
}
