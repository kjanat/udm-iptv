package installer

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestUninstallFailureBoundaries(t *testing.T) {
	want := []string{"stop", "disable", "NAT", "files", "state", "reload"}
	for boundary := -1; boundary < len(want); boundary++ {
		var calls []string
		cause := errors.New("injected uninstall failure")
		step := func(name string) func(context.Context) error {
			return func(context.Context) error {
				calls = append(calls, name)
				if boundary >= 0 && name == want[boundary] {
					return cause
				}
				return nil
			}
		}
		err := executeUninstall(t.Context(), uninstallActions{
			stop: step("stop"), disable: step("disable"), removeNAT: step("NAT"),
			removeFiles: step("files"), removeState: step("state"), reload: step("reload"),
		})
		switch {
		case boundary < 0:
			if err != nil || !reflect.DeepEqual(calls, want) {
				t.Fatalf("successful uninstall: %v %v", calls, err)
			}
		case want[boundary] == "NAT":
			if !errors.Is(err, cause) || !reflect.DeepEqual(calls, want) {
				t.Fatalf("NAT failure must not block cleanup: %v %v", calls, err)
			}
		default:
			if !errors.Is(err, cause) || !reflect.DeepEqual(calls, want[:boundary+1]) {
				t.Fatalf("boundary %d: %v %v", boundary, calls, err)
			}
		}
	}
}

func TestCancelledUninstallDoesNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := executeUninstall(ctx, uninstallActions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled uninstall: %v", err)
	}
}
