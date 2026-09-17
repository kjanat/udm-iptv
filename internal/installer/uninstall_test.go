package installer

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

var errInjectedUninstallStep = errors.New("injected uninstall failure")

func TestUninstallFailureBoundaries(t *testing.T) {
	all := []string{"stop", "disable", "NAT", "files", "state", "reload"}
	for _, test := range []struct {
		name, fail string
		want       []string
		fails      bool
	}{
		{"success", "", all, false},
		{"stop", "stop", all[:1], true},
		{"disable", "disable", all[:2], true},
		{"NAT", "NAT", all, true},
		{"files", "files", all[:4], true},
		{"state", "state", all[:5], true},
		{"reload", "reload", all[:6], true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			cause := errInjectedUninstallStep
			step := func(name string) func(context.Context) error {
				return func(context.Context) error {
					calls = append(calls, name)
					if name == test.fail {
						return cause
					}

					return nil
				}
			}
			err := executeUninstall(t.Context(), uninstallActions{
				stop: step("stop"), disable: step("disable"), removeNAT: step("NAT"),
				removeFiles: step("files"), removeState: step("state"), reload: step("reload"),
			})
			if (err != nil) != test.fails || (test.fails && !errors.Is(err, cause)) {
				t.Fatalf("uninstall error: %v", err)
			}
			var warning CleanupError
			if got := errors.As(err, &warning); got != (test.fail == "NAT") {
				t.Fatalf("%s reported as a cleanup warning: %v", test.fail, got)
			}
			if !reflect.DeepEqual(calls, test.want) {
				t.Fatalf("calls=%v want=%v", calls, test.want)
			}
		})
	}
}

func TestCancelledUninstallDoesNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := executeUninstall(ctx, uninstallActions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled uninstall: %v", err)
	}
}
