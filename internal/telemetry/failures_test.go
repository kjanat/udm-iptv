package telemetry

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strconv"
	"testing"
)

func TestNestedFailuresPreserveDistinctErrors(t *testing.T) {
	for _, joined := range []bool{false, true} {
		t.Run(strconv.FormatBool(joined), func(t *testing.T) {
			r, transport := newRecordingReporter(t, testSettings())
			result := r.Run(t.Context(), "daemon", func(ctx context.Context) error {
				err := r.Run(ctx, "dhcp.acquire", func(context.Context) error { return errOperationFailed })
				if joined {
					err = errors.Join(err, errPrivateFailure)
				}
				return fmt.Errorf("acquire lease: %w", errors.Join(err))
			})
			r.Close()
			if !errors.Is(result, errOperationFailed) {
				t.Fatal("lost original error")
			}
			want := 1
			if joined {
				want = 2
			}
			failures := failureEvents(transport.events)
			assertEqual(t, "failures", len(failures), want)
			assertEqual(t, "origin", failures[0].Transaction, "dhcp.acquire")
		})
	}
}

func TestSiblingAndRepeatedFailuresAreIndependent(t *testing.T) {
	r, transport := newRecordingReporter(t, testSettings())
	for range 2 {
		_ = r.Run(t.Context(), "daemon", func(ctx context.Context) error {
			for range 2 {
				_ = r.Run(ctx, "dhcp.acquire", func(context.Context) error { return errOperationFailed })
			}
			return nil
		})
	}
	r.Close()
	assertEqual(t, "independent failures", len(failureEvents(transport.events)), 4)
}

type tracedError struct {
	stack []uintptr
}

func (*tracedError) Error() string           { return "origin failure" }
func (e *tracedError) StackTrace() []uintptr { return e.stack }

func TestFailureKeepsGenuineOriginStack(t *testing.T) {
	r, transport := newRecordingReporter(t, testSettings())
	pcs := make([]uintptr, 16)
	pcs = pcs[:runtime.Callers(1, pcs)]
	_ = r.Run(t.Context(), "daemon", func(context.Context) error {
		return &tracedError{stack: pcs}
	})
	r.Close()
	failures := failureEvents(transport.events)
	assertEqual(t, "failures", len(failures), 1)
	if failures[0].Exception[0].Stacktrace == nil {
		t.Fatal("genuine error stack was removed")
	}
}

func TestFailureGroupingUsesOperationAndErrorOrigin(t *testing.T) {
	r, transport := newRecordingReporter(t, testSettings())
	for _, operation := range []string{"upgrade", "daemon"} {
		_ = r.Run(t.Context(), operation, func(context.Context) error {
			return fmt.Errorf("operation: %w", errOperationFailed)
		})
	}
	r.Close()
	failures := failureEvents(transport.events)
	assertEqual(t, "failures", len(failures), 2)
	if slices.Equal(failures[0].Fingerprint, failures[1].Fingerprint) {
		t.Fatal("unrelated operations share a fingerprint")
	}
	for _, event := range failures {
		for _, exception := range event.Exception {
			if exception.Stacktrace != nil {
				t.Fatal("plain error has a synthetic reporting stack")
			}
		}
	}
}
