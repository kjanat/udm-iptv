package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

var errNoSnapshot = errors.New("no snapshot")

func TestDiagnosticsAreOptionalAndBounded(t *testing.T) {
	t.Parallel()
	var daemon Daemon
	if got := daemon.diagnostics(context.Background()); got != nil {
		t.Fatalf("snapshot without a collector: %s", got)
	}
	daemon.Diagnostics = func(context.Context) (json.RawMessage, error) { return nil, errNoSnapshot }
	if got := daemon.diagnostics(context.Background()); got != nil {
		t.Fatalf("snapshot from a failing collector: %s", got)
	}
	daemon.Diagnostics = func(ctx context.Context) (json.RawMessage, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("collector ran without a deadline")
		}

		return json.RawMessage(`{"version":"test"}`), nil
	}
	if got := daemon.diagnostics(context.Background()); string(got) != `{"version":"test"}` {
		t.Fatalf("snapshot = %s", got)
	}
}
