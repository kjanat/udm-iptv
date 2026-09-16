package diagnostics

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRenderCompletedEvent(t *testing.T) {
	t.Parallel()
	output := RenderEvent(Event{Type: "completed", Message: "Capture reached its deadline."})
	if !strings.Contains(output, "Capture completed") {
		t.Fatalf("unexpected completion output: %q", output)
	}
}

func TestCollectorsShareCaptureDeadline(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	release := make(chan struct{})
	started := time.Now()
	_, err := collectWithin(ctx, func() (string, error) {
		<-release

		return "late", nil
	})
	close(release)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("collector error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("collector exceeded shared deadline by %s", elapsed)
	}
}

func TestJournalCollectionIsBoundedAtSource(t *testing.T) {
	t.Parallel()
	arguments := journalArguments("s=cursor", 10_000)
	want := []string{"-n", "10000", "--after-cursor", "s=cursor"}
	for _, value := range want {
		if !slices.Contains(arguments, value) {
			t.Fatalf("journal arguments %q do not contain %q", arguments, value)
		}
	}
}
