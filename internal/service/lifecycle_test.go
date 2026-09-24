package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	systemd "github.com/coreos/go-systemd/v22/dbus"
	"github.com/godbus/dbus/v5"
)

var errLifecycleTest = errors.New("injected lifecycle failure")

type lifecycleBus struct {
	active, timer           bool
	starts                  int
	calls                   []string
	failSchedule, failStart bool
	cancel                  context.CancelFunc
}

func (b *lifecycleBus) StartUnitContext(ctx context.Context, unit, _ string, result chan<- string) (int, error) {
	b.calls = append(b.calls, "start "+unit)
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("start: %w", err)
	}
	if b.failStart {
		return 0, errLifecycleTest
	}
	if !b.active {
		b.starts++
	}
	b.active = true
	result <- statusDone
	return 1, nil
}

func (b *lifecycleBus) StopUnitContext(_ context.Context, unit, _ string, result chan<- string) (int, error) {
	b.calls = append(b.calls, "stop "+unit)
	if strings.HasSuffix(unit, ".timer") {
		b.timer = false
	} else {
		b.active = false
	}
	result <- statusDone
	return 1, nil
}

func (b *lifecycleBus) RestartUnitContext(_ context.Context, unit, _ string, result chan<- string) (int, error) {
	b.calls = append(b.calls, "restart "+unit)
	b.active = true
	b.starts++
	result <- statusDone
	return 1, nil
}

func (b *lifecycleBus) StartTransientUnitContext(_ context.Context, unit, _ string, _ []systemd.Property, result chan<- string) (int, error) {
	b.calls = append(b.calls, "schedule "+unit)
	if b.cancel != nil {
		b.cancel()
	}
	if b.failSchedule {
		return 0, errLifecycleTest
	}
	b.timer = true
	result <- statusDone
	return 1, nil
}

func (b *lifecycleBus) GetUnitPropertiesContext(context.Context, string) (map[string]any, error) {
	if !b.timer {
		return nil, dbus.Error{Name: "org.freedesktop.systemd1.NoSuchUnit"}
	}
	return map[string]any{"ActiveState": "active", "SubState": "waiting"}, nil
}

func (b *lifecycleBus) GetUnitTypePropertiesContext(context.Context, string, string) (map[string]any, error) {
	return map[string]any{}, nil
}

func TestLifecycleOperationsCancelPause(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"start", "restart", "stop"} {
		t.Run(operation, func(t *testing.T) {
			bus := &lifecycleBus{active: true}
			lifecycle := Lifecycle{Connection: bus, Unit: Unit}
			if err := lifecycle.Pause(t.Context(), time.Minute); err != nil {
				t.Fatal(err)
			}
			if bus.active || !bus.timer {
				t.Fatal("pause did not stop and schedule")
			}
			run := map[string]func(context.Context) error{"start": lifecycle.Start, "restart": lifecycle.Restart, "stop": lifecycle.Stop}[operation]
			err := run(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if bus.timer || bus.active != (operation != "stop") {
				t.Fatalf("state: %+v", bus)
			}
			want := []string{"stop udm-iptv.timer", "stop udm-iptv.service", "schedule udm-iptv.timer", "stop udm-iptv.timer", operation + " udm-iptv.service"}
			if !reflect.DeepEqual(bus.calls, want) {
				t.Fatalf("calls = %v", bus.calls)
			}
		})
	}
}

func TestRepeatedStartDoesNotRestart(t *testing.T) {
	t.Parallel()
	bus := &lifecycleBus{}
	lifecycle := Lifecycle{Connection: bus, Unit: Unit}
	for range 2 {
		if err := lifecycle.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if bus.starts != 1 {
		t.Fatalf("service started %d times", bus.starts)
	}
}

func TestPauseFailureRestoresServiceAfterCancellation(t *testing.T) {
	t.Parallel()
	for _, failStart := range []bool{false, true} {
		t.Run(strconv.FormatBool(failStart), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			bus := &lifecycleBus{active: true, failSchedule: true, failStart: failStart, cancel: cancel}
			err := (Lifecycle{Connection: bus, Unit: Unit}).Pause(ctx, time.Minute)
			if !errors.Is(err, errLifecycleTest) {
				t.Fatalf("error = %v", err)
			}
			want := "started again"
			if failStart {
				want = "recovery incomplete"
			}
			if !strings.Contains(err.Error(), want) || bus.active == failStart || bus.timer {
				t.Fatalf("error=%v state=%+v", err, bus)
			}
		})
	}
}

func TestNoSuchUnitAcceptsDBusErrors(t *testing.T) {
	t.Parallel()
	value := dbus.Error{Name: "org.freedesktop.systemd1.NoSuchUnit"}
	for _, err := range []error{value, &value, fmt.Errorf("wrapped: %w", value)} {
		if !NoSuchUnit(err) {
			t.Fatalf("not recognized: %v", err)
		}
	}
	if NoSuchUnit(errLifecycleTest) || NoSuchUnit(nil) {
		t.Fatal("unrelated error accepted")
	}
}

func TestNoResumeTimerHasNoDeadline(t *testing.T) {
	t.Parallel()
	when, err := (Lifecycle{Connection: &lifecycleBus{}, Unit: Unit}).ResumeAt(t.Context())
	if err != nil || !when.IsZero() {
		t.Fatalf("deadline=%v err=%v", when, err)
	}
}
