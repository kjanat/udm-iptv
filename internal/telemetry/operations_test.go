package telemetry

import (
	"context"
	"errors"
	"testing"
)

func TestMutatingCommandFailuresAreReported(t *testing.T) {
	for _, operation := range []string{"start", "stop", "configure.set", "service.activate"} {
		t.Run(operation, func(t *testing.T) {
			reporter, transport := newRecordingReporter(t, testSettings())
			err := reporter.Run(t.Context(), operation, func(context.Context) error { return errOperationFailed })
			reporter.Close()
			if !errors.Is(err, errOperationFailed) {
				t.Fatalf("operation failure changed: %v", err)
			}
			failures := failureEvents(transport.events)
			if len(failures) != 1 || failures[0].Transaction != operation {
				t.Fatalf("%s did not report exactly one failure: %+v", operation, failures)
			}
		})
	}
}
