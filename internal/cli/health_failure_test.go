package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/getsentry/sentry-go"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/config/configtest"
	"github.com/kjanat/udm-iptv/internal/diagnostics"
	"github.com/kjanat/udm-iptv/internal/installer"
	"github.com/kjanat/udm-iptv/internal/telemetry"
)

const failureJournalMessage = "DHCP bound on iptv: address 10.207.79.141, routers 10.207.79.1; vendorclass=IPTV_RG; multicast report from 192.168.10.152"

var errHealthFailureOutput = errors.New("diagnostic terminal closed")

type healthFailureOutput struct{}

func (healthFailureOutput) Write([]byte) (int, error) { return 0, errHealthFailureOutput }

func TestActivationFailureRetainsLocalAndReportedDiagnostics(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*Application, context.Context) error
	}{
		{"install", func(application *Application, ctx context.Context) error {
			return application.installBackend().Activate(ctx, installer.Plan{})
		}},
		{"start", (*Application).start},
		{"restart", func(application *Application, ctx context.Context) error {
			return application.restart(ctx, true)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			installFailureJournal(t)
			t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", "unix:path="+filepath.Join(t.TempDir(), "missing-bus"))
			capture := captureTelemetry(t)
			var local bytes.Buffer
			application := &Application{ConfigPath: filepath.Join(t.TempDir(), "missing.json"), Err: &local}
			err := runHealthFailureReport(t, config.Telemetry{Enabled: true, Errors: true}, func(ctx context.Context) error {
				return test.run(application, ctx)
			})
			if err == nil || !strings.Contains(err.Error(), "connect to systemd") {
				t.Fatalf("lost activation failure: %v", err)
			}
			if strings.Count(local.String(), "=== udm-iptv failure diagnostics ===") != 1 || !strings.Contains(local.String(), failureJournalMessage) {
				t.Fatalf("activation failure omitted or duplicated diagnostics: %s", local.String())
			}
			if !bytes.Equal(healthFailureAttachment(t, capture.output()), local.Bytes()) {
				t.Fatal("reported activation failure lost diagnostic evidence")
			}
		})
	}
}

func TestFreshInstallActivationFailureUsesSavedTelemetry(t *testing.T) {
	installFailureJournal(t)
	directory := t.TempDir()
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", "unix:path="+filepath.Join(directory, "missing-bus"))
	capture := captureTelemetry(t)
	var local bytes.Buffer
	application := &Application{ConfigPath: filepath.Join(directory, "config.json"), StateDir: directory, Err: &local}
	value := configtest.Custom()
	value.Telemetry = config.Telemetry{Enabled: true, Errors: true}
	plan := installer.Plan{ConfigPath: application.ConfigPath, Config: value}
	backend := application.installBackend()
	if err := backend.SaveConfig(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	err := backend.Activate(t.Context(), plan)
	if err == nil || !strings.Contains(err.Error(), "connect to systemd") {
		t.Fatalf("lost activation failure: %v", err)
	}
	if !strings.Contains(local.String(), failureJournalMessage) {
		t.Fatal("activation failure omitted local diagnostics")
	}
	output := capture.output()
	var failure invocationFailure
	if err := json.Unmarshal(healthFailureItem(t, output, "event"), &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Transaction != "service.activate" || !slices.ContainsFunc(failure.Exception, func(exception sentry.Exception) bool {
		return strings.Contains(exception.Value, "connect to systemd")
	}) {
		t.Fatalf("first installation lost its activation error: %+v", failure)
	}
	if !bytes.Equal(healthFailureAttachment(t, output), local.Bytes()) {
		t.Fatal("first installation did not report its diagnostic evidence")
	}
}

func TestHealthFailurePreservesSnapshotLeaseOptions(t *testing.T) {
	capture := captureTelemetry(t)
	var snapshot diagnostics.Snapshot
	const input = `{"network":{"target":"iptv","addresses":["10.207.79.141/20"]},"lease":{"received":"2026-09-22T22:17:40Z","applied":true,"lease":{"address":"10.207.79.141","mask":"255.255.240.0","options":{"vendorclass":"IPTV_RG","staticroutes":"10.0.0.0/8 10.207.79.1"}}}}`
	if err := json.Unmarshal([]byte(input), &snapshot); err != nil {
		t.Fatal(err)
	}
	report := diagnostics.RenderSnapshot(snapshot) + failureJournalMessage + "\n"
	var local bytes.Buffer
	application := &Application{Err: &local}
	_ = runHealthFailureReport(t, config.Telemetry{Enabled: true, Errors: true}, func(ctx context.Context) error {
		return errors.Join(errPrivateFailure, application.publishFailureDiagnostics(ctx, []byte(report)))
	})
	attachment := string(healthFailureAttachment(t, capture.output()))
	if attachment != report || local.String() != report {
		t.Fatal("local or outbound snapshot differs from collected report")
	}
	for _, value := range []string{"10.207.79.141/20", "vendorclass=IPTV_RG", "staticroutes=10.0.0.0/8 10.207.79.1", failureJournalMessage} {
		if !strings.Contains(attachment, value) {
			t.Fatalf("missing diagnostic evidence %q", value)
		}
	}
}

func TestHealthFailureAttachmentMatchesLocalReport(t *testing.T) {
	installFailureJournal(t)
	capture := captureTelemetry(t)
	var local bytes.Buffer
	application := &Application{ConfigPath: filepath.Join(t.TempDir(), "missing.json"), Err: &local}
	err := runHealthFailureReport(t, config.Telemetry{Enabled: true, Errors: true}, func(ctx context.Context) error {
		return application.reportHealthFailure(ctx, errPrivateFailure)
	})
	if !errors.Is(err, errPrivateFailure) {
		t.Fatalf("lost original failure: %v", err)
	}
	attachment := healthFailureAttachment(t, capture.output())
	if !bytes.Equal(attachment, local.Bytes()) {
		t.Fatalf("attachment differs from local report:\n%s\nlocal:\n%s", attachment, local.Bytes())
	}
	if !strings.Contains(string(attachment), failureJournalMessage) {
		t.Fatal("lost original journal message, addresses or DHCP option values")
	}
}

func TestHealthFailureLocalWriteErrorDoesNotStarveAttachment(t *testing.T) {
	installFailureJournal(t)
	capture := captureTelemetry(t)
	application := &Application{ConfigPath: filepath.Join(t.TempDir(), "missing.json"), Err: healthFailureOutput{}}
	err := runHealthFailureReport(t, config.Telemetry{Enabled: true, Errors: true}, func(ctx context.Context) error {
		return application.reportHealthFailure(ctx, errPrivateFailure)
	})
	if !errors.Is(err, errPrivateFailure) || !errors.Is(err, errHealthFailureOutput) {
		t.Fatalf("lost health or output error: %v", err)
	}
	if !strings.Contains(string(healthFailureAttachment(t, capture.output())), failureJournalMessage) {
		t.Fatal("local output failure starved the diagnostic attachment")
	}
}

func TestHealthFailureRespectsDisabledErrorReporting(t *testing.T) {
	installFailureJournal(t)
	capture := captureTelemetry(t)
	var local bytes.Buffer
	application := &Application{ConfigPath: filepath.Join(t.TempDir(), "missing.json"), Err: &local}
	_ = runHealthFailureReport(t, config.Telemetry{Enabled: true, Errors: false}, func(ctx context.Context) error {
		return application.reportHealthFailure(ctx, errPrivateFailure)
	})
	if !strings.Contains(local.String(), failureJournalMessage) || capture.output() != "" {
		t.Fatal("local diagnostics or explicit reporting choice changed")
	}
}

func TestHealthFailureCollectionErrorKeepsPartialReport(t *testing.T) {
	capture := captureTelemetry(t)
	directory := t.TempDir()
	t.Setenv("PATH", directory)
	var local bytes.Buffer
	application := &Application{ConfigPath: filepath.Join(directory, "missing.json"), Err: &local}
	err := runHealthFailureReport(t, config.Telemetry{Enabled: true, Errors: true}, func(ctx context.Context) error {
		return application.reportHealthFailure(ctx, errPrivateFailure)
	})
	if !errors.Is(err, errPrivateFailure) || !strings.Contains(err.Error(), "collect failure journal") {
		t.Fatalf("lost original or collection failure: %v", err)
	}
	attachment := healthFailureAttachment(t, capture.output())
	if !bytes.Equal(attachment, local.Bytes()) || !bytes.Contains(attachment, []byte("Snapshot unavailable:")) {
		t.Fatal("collection error lost the partial diagnostic report")
	}
}

func installFailureJournal(t *testing.T) {
	t.Helper()
	directory := t.TempDir()
	data, err := json.Marshal(map[string]string{
		"__REALTIME_TIMESTAMP": "1790115460000000", "SYSLOG_IDENTIFIER": "udm-iptv", "MESSAGE": failureJournalMessage,
	})
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '%s\\n' '" + string(data) + "'\n"
	if err := atomicfile.Write(filepath.Join(directory, "journalctl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
}

func runHealthFailureReport(t *testing.T, settings config.Telemetry, run func(context.Context) error) error {
	t.Helper()
	reporter, err := telemetry.New(settings, "test", "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer reporter.Close()
	if err := reporter.Run(t.Context(), "service.health", run); err != nil {
		return fmt.Errorf("report health failure: %w", err)
	}
	return nil
}

func healthFailureAttachment(t *testing.T, envelope string) []byte {
	t.Helper()
	return healthFailureItem(t, envelope, "attachment")
}

func healthFailureItem(t *testing.T, envelope, kind string) []byte {
	t.Helper()
	reader := bufio.NewReader(strings.NewReader(envelope))
	var found []byte
	for {
		line, err := reader.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		var header struct {
			Type   string `json:"type"`
			Length int    `json:"length"`
		}
		if json.Unmarshal(line, &header) != nil || header.Type == "" {
			continue
		}
		payload := make([]byte, header.Length)
		if _, err := io.ReadFull(reader, payload); err != nil {
			t.Fatal(err)
		}
		if header.Type == kind {
			if found != nil {
				t.Fatalf("duplicate diagnostic %s", kind)
			}
			found = payload
		}
	}
	if found == nil {
		t.Fatalf("missing diagnostic %s", kind)
	}
	return found
}
