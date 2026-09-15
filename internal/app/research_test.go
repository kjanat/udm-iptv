package app

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/telemetry"
)

func TestFirstConfigurationResearchAndOptOut(t *testing.T) {
	for _, mode := range []string{"enabled", "disabled", "no-network", "no-presets"} {
		t.Run(mode, func(t *testing.T) {
			var mu sync.Mutex
			var received strings.Builder
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				var reader io.Reader = request.Body
				if request.Header.Get("Content-Encoding") == "gzip" {
					decoder, err := gzip.NewReader(reader)
					if err != nil {
						t.Error(err)
						return
					}
					defer func() { _ = decoder.Close() }()
					reader = decoder
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
			lookups := 0
			application := &Application{
				ConfigPath: filepath.Join(dir, "config.json"), StateDir: dir, Out: io.Discard, Err: io.Discard,
				networkIdentity: func(context.Context) telemetry.NetworkIdentity {
					lookups++
					return telemetry.NetworkIdentity{IP: "11.22.33.44", PTR: "example.kpn.net."}
				},
			}
			root := application.root()
			application.instrumentCommands(root)
			args := []string{"configure", "--non-interactive", "--profile=kpn"}
			switch mode {
			case "disabled":
				args = append(args, "--telemetry=false")
			case "no-network":
				args = append(args, "--telemetry-network-identity=false")
			case "no-presets":
				args = append(args, "--telemetry-presets=false")
			}
			root.SetArgs(args)
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			output := received.String()
			mu.Unlock()
			wantReport := mode == "enabled" || mode == "no-network"
			if strings.Contains(output, "installation.report") != wantReport {
				t.Fatal("first configuration reporting mismatch")
			}
			if (lookups == 1) != (mode == "enabled") {
				t.Fatal("network lookup preference ignored")
			}
			if wantReport && (!strings.Contains(output, `"applied":false`) || !strings.Contains(output, `"revision":1`)) {
				t.Fatal("uninstalled configuration marked applied")
			}
			value, err := config.Load(application.ConfigPath)
			if err != nil || value.Profile != "kpn" {
				t.Fatal("reporting broke configuration")
			}
		})
	}
}
