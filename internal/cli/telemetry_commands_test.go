package cli

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/telemetry"
)

func TestCommandInvocationTelemetry(t *testing.T) {
	for _, invocation := range []string{"install", "configure", "reconfigure"} {
		for _, outcome := range []string{"completed", "failed", "cancelled", "opt-out", "missing-consent", "dry-run", "disable-flag"} {
			if outcome == "dry-run" && invocation != "install" || outcome == "disable-flag" && invocation == "install" {
				continue
			}
			t.Run(invocation+"/"+outcome, func(t *testing.T) {
				var mu sync.Mutex
				var received strings.Builder
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var reader io.Reader = r.Body
					if r.Header.Get("Content-Encoding") == "gzip" {
						decoded, err := gzip.NewReader(r.Body)
						if err != nil {
							t.Error(err)

							return
						}
						defer closeIgnoringError(decoded)
						reader = decoded
					}
					data, err := io.ReadAll(reader)
					if err != nil {
						t.Error(err)

						return
					}
					mu.Lock()
					received.Write(data)
					mu.Unlock()
					w.WriteHeader(http.StatusOK)
				}))
				defer server.Close()
				previous := telemetry.DSN
				telemetry.DSN = strings.Replace(server.URL, "http://", "http://public@", 1) + "/1"
				defer func() { telemetry.DSN = previous }()
				dir := t.TempDir()
				path := filepath.Join(dir, "config.json")
				value := config.Default()
				value.Telemetry.Enabled = outcome != "opt-out"
				value.Telemetry.Logs = true
				if outcome != "missing-consent" {
					err := config.Save(path, value)
					if err != nil {
						t.Fatal(err)
					}
				}
				application := &Application{ConfigPath: path, StateDir: dir, Out: io.Discard, Err: io.Discard}
				name := invocation
				if name == "reconfigure" {
					name = "configure"
				}
				var result error
				if outcome == "failed" {
					result = errors.New("private failure detail")
				}
				if outcome == "cancelled" {
					result = context.Canceled
				}
				called := false
				child := &cobra.Command{Use: name, RunE: func(_ *cobra.Command, _ []string) error {
					called = true
					return result
				}}
				if name == "configure" {
					child.Aliases = []string{"reconfigure"}
					child.Flags().Bool("telemetry", true, "")
				}
				if name == "install" {
					child.Flags().Bool("dry-run", false, "")
				}
				root := &cobra.Command{Use: "test", SilenceErrors: true, SilenceUsage: true}
				root.AddCommand(child)
				application.instrumentCommands(root)
				args := []string{invocation}
				if outcome == "dry-run" {
					args = append(args, "--dry-run")
				}
				if outcome == "disable-flag" {
					args = append(args, "--telemetry=false")
				}
				root.SetArgs(args)
				err := root.Execute()
				if !errors.Is(err, result) || !called {
					t.Fatalf("command result changed: %v", err)
				}
				mu.Lock()
				output := received.String()
				mu.Unlock()
				silent := outcome == "opt-out" || outcome == "missing-consent" || outcome == "dry-run" || outcome == "disable-flag"
				if silent {
					if output != "" {
						t.Fatal("telemetry sent without consent or during dry-run")
					}

					return
				}
				for _, message := range []string{name + " started", name + " " + outcome} {
					if !strings.Contains(output, message) {
						t.Fatalf("missing lifecycle log %q", message)
					}
				}
				if strings.Contains(output, "private failure detail") {
					t.Fatal("raw error leaked")
				}
			})
		}
	}
}
