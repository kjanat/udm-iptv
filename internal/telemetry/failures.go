package telemetry

import (
	"context"
	"errors"
	"slices"
	"sync"
)

type failureKey struct{}

// operationFailures tracks child failures within one invocation. Siblings and
// later invocations still report independently, even for the same sentinel.
type operationFailures struct {
	mu     sync.Mutex
	errors []error
	parent *operationFailures
}

func (f *operationFailures) report(ctx context.Context, r *Reporter, operation string, err error, panicked bool) {
	if !f.contains(err) {
		r.reportOutcome(ctx, operation, err, panicked)
	}
	if f.parent != nil && err != nil && !errors.Is(err, context.Canceled) {
		f.parent.add(err)
	}
}

func (f *operationFailures) add(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errors = append(f.errors, err)
	if len(f.errors) > exceptionChainLimit {
		f.errors = append([]error(nil), f.errors[len(f.errors)-exceptionChainLimit:]...)
	}
}

func (f *operationFailures) contains(err error) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	return coveredFailure(err, f.errors)
}

// A joined cleanup error must still be reported unless every branch was already reported.
// Wrapping a reported error alone does not create a failure.
func coveredFailure(err error, reported []error) bool {
	if err == nil {
		return false
	}
	if slices.ContainsFunc(reported, func(previous error) bool { return errors.Is(previous, err) }) {
		return true
	}
	switch cause := err.(type) { //nolint:errorlint // Inspect each immediate unwrap branch, including joined errors.
	case interface{ Unwrap() []error }:
		children := cause.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if child != nil && !coveredFailure(child, reported) {
				return false
			}
		}
		return true
	case interface{ Unwrap() error }:
		return coveredFailure(cause.Unwrap(), reported)
	default:
		return false
	}
}
