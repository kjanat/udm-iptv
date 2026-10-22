package cli

import (
	"errors"
	"io"
	"testing"
)

func TestStopRejectsANegativePause(t *testing.T) {
	t.Parallel()
	for _, duration := range []string{"-5m", "0", "1ns"} {
		application := &Application{Out: io.Discard, Err: io.Discard}
		root := application.root()
		root.SetArgs([]string{"stop", "--for", duration})
		if err := root.Execute(); !errors.Is(err, errNegativePause) {
			t.Fatalf("stop --for %s: %v", duration, err)
		}
	}
}
