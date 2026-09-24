package cli

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/getsentry/sentry-go"
	"github.com/spf13/cobra"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/config/configtest"
	"github.com/kjanat/udm-iptv/internal/telemetry"
)

const privateFailureDetail = "private failure detail"

var errPrivateFailure = errors.New(privateFailureDetail)

type telemetryCapture struct {
	mu       sync.Mutex
	received strings.Builder
}

func (c *telemetryCapture) collect(data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.received.Write(data)
}

func (c *telemetryCapture) output() string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.received.String()
}

func captureTelemetry(t *testing.T) *telemetryCapture {
	t.Helper()
	capture := &telemetryCapture{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var reader io.Reader = request.Body
		if request.Header.Get("Content-Encoding") == "gzip" {
			decoded, err := gzip.NewReader(request.Body)
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
		capture.collect(data)
		writer.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	previous := telemetry.DSN
	telemetry.DSN = strings.Replace(server.URL, "http://", "http://public@", 1) + "/1"
	t.Cleanup(func() { telemetry.DSN = previous })

	return capture
}

type commandInvocationCase struct {
	name             string
	invocation       string
	command          string
	args             []string
	saveConfig       bool
	telemetryEnabled bool
	result           error
	wantLogs         []string
}

func commandInvocationCases() []commandInvocationCase {
	return []commandInvocationCase{
		{name: "install/completed", invocation: "install", command: commandInstall, saveConfig: true, telemetryEnabled: true, wantLogs: []string{"install started", "install completed"}},
		{name: "install/failed", invocation: "install", command: commandInstall, saveConfig: true, telemetryEnabled: true, result: errPrivateFailure, wantLogs: []string{"install started", "install failed"}},
		{name: "install/wrapped-failure", invocation: "install", command: commandInstall, saveConfig: true, telemetryEnabled: true, result: fmt.Errorf("private wrapper: %w", errPrivateFailure), wantLogs: []string{"install started", "install failed"}},
		{name: "install/cancelled", invocation: "install", command: commandInstall, saveConfig: true, telemetryEnabled: true, result: context.Canceled, wantLogs: []string{"install started", "install cancelled"}},
		{name: "install/opt-out", invocation: "install", command: commandInstall, saveConfig: true, telemetryEnabled: false},
		{name: "install/missing-consent", invocation: "install", command: commandInstall, saveConfig: false, telemetryEnabled: true},
		{name: "install/dry-run", invocation: "install", command: commandInstall, args: []string{"--dry-run"}, saveConfig: true, telemetryEnabled: true},

		{name: "configure/completed", invocation: "configure", command: commandConfigure, saveConfig: true, telemetryEnabled: true, wantLogs: []string{"configure started", "configure completed"}},
		{name: "configure/failed", invocation: "configure", command: commandConfigure, saveConfig: true, telemetryEnabled: true, result: errPrivateFailure, wantLogs: []string{"configure started", "configure failed"}},
		{name: "configure/cancelled", invocation: "configure", command: commandConfigure, saveConfig: true, telemetryEnabled: true, result: context.Canceled, wantLogs: []string{"configure started", "configure cancelled"}},
		{name: "configure/opt-out", invocation: "configure", command: commandConfigure, saveConfig: true, telemetryEnabled: false},
		{name: "configure/missing-consent", invocation: "configure", command: commandConfigure, saveConfig: false, telemetryEnabled: true},
		{name: "configure/disable-flag", invocation: "configure", command: commandConfigure, args: []string{"--telemetry=false"}, saveConfig: true, telemetryEnabled: true},

		{name: "reconfigure/completed", invocation: "reconfigure", command: commandConfigure, saveConfig: true, telemetryEnabled: true, wantLogs: []string{"configure started", "configure completed"}},
		{name: "reconfigure/failed", invocation: "reconfigure", command: commandConfigure, saveConfig: true, telemetryEnabled: true, result: errPrivateFailure, wantLogs: []string{"configure started", "configure failed"}},
		{name: "reconfigure/cancelled", invocation: "reconfigure", command: commandConfigure, saveConfig: true, telemetryEnabled: true, result: context.Canceled, wantLogs: []string{"configure started", "configure cancelled"}},
		{name: "reconfigure/opt-out", invocation: "reconfigure", command: commandConfigure, saveConfig: true, telemetryEnabled: false},
		{name: "reconfigure/missing-consent", invocation: "reconfigure", command: commandConfigure, saveConfig: false, telemetryEnabled: true},
		{name: "reconfigure/disable-flag", invocation: "reconfigure", command: commandConfigure, args: []string{"--telemetry=false"}, saveConfig: true, telemetryEnabled: true},
	}
}

type invocationFailure struct {
	Transaction string             `json:"transaction"`
	Fingerprint []string           `json:"fingerprint"`
	Exception   []sentry.Exception `json:"exception"`
}

func assertFailureReported(t *testing.T, output, operation string, cause error) {
	t.Helper()
	failures := invocationFailures(t, output)
	if len(failures) != 1 {
		t.Fatalf("expected one structured failure, got %d", len(failures))
	}
	failure := failures[0]
	if failure.Transaction != operation || !slices.Contains(failure.Fingerprint, operation) {
		t.Fatalf("failure lost operation identity: %+v", failure)
	}
	wantExceptions := 0
	for current := cause; current != nil; current = errors.Unwrap(current) {
		if !slices.ContainsFunc(failure.Exception, func(exception sentry.Exception) bool {
			return exception.Value == current.Error()
		}) {
			t.Fatalf("failure lost error text: %q", current.Error())
		}
		wantExceptions++
	}
	if len(failure.Exception) != wantExceptions {
		t.Fatalf("failure lost wrappers: got %d exceptions, want %d", len(failure.Exception), wantExceptions)
	}
	assertInvocationExceptionRelationships(t, failure.Exception)
}

func invocationFailures(t *testing.T, output string) []invocationFailure {
	t.Helper()
	var failures []invocationFailure
	decoder := json.NewDecoder(strings.NewReader(output))
	for {
		var item invocationFailure
		if err := decoder.Decode(&item); err != nil {
			if errors.Is(err, io.EOF) {
				return failures
			}
			t.Fatalf("decode telemetry envelope: %v", err)
		}
		if len(item.Exception) > 0 {
			failures = append(failures, item)
		}
	}
}

func assertInvocationExceptionRelationships(t *testing.T, exceptions []sentry.Exception) {
	t.Helper()
	foundCause := false
	for _, exception := range exceptions {
		if exception.Value == "" || exception.Type == "" {
			t.Fatalf("failure lost its type or message: %+v", exception)
		}
		foundCause = foundCause || exception.Type == fmt.Sprintf("%T", errPrivateFailure)
	}
	if !foundCause {
		t.Fatal("failure lost its original error type")
	}
	if len(exceptions) == 1 {
		return // A leaf error has no parent relationship to encode.
	}
	ids := invocationExceptionIDs(t, exceptions)
	assertInvocationExceptionParents(t, exceptions, ids)
}

func invocationExceptionIDs(t *testing.T, exceptions []sentry.Exception) map[int]bool {
	t.Helper()
	ids := make(map[int]bool)
	for _, exception := range exceptions {
		if exception.Mechanism == nil {
			t.Fatal("wrapped exception lost relationship metadata")
		}
		ids[exception.Mechanism.ExceptionID] = true
	}
	if len(ids) != len(exceptions) {
		t.Fatal("exception identities are not distinct")
	}
	return ids
}

func assertInvocationExceptionParents(t *testing.T, exceptions []sentry.Exception, ids map[int]bool) {
	t.Helper()
	parents := 0
	for _, exception := range exceptions {
		if parent := exception.Mechanism.ParentID; parent != nil {
			parents++
			if !ids[*parent] {
				t.Fatal("exception parent points outside the reported chain")
			}
		}
	}
	if parents != len(exceptions)-1 {
		t.Fatalf("exception chain relationships lost: %d parents for %d errors", parents, len(exceptions))
	}
}

func TestReportRunUsesSavedConfigWhenInstallHadNoMonitor(t *testing.T) {
	capture := captureTelemetry(t)
	directory := t.TempDir()
	path := filepath.Join(directory, "config.json")
	value := configtest.Custom()
	value.Telemetry.Logs = true
	if err := config.Save(path, value); err != nil {
		t.Fatal(err)
	}
	application := &Application{ConfigPath: path, StateDir: directory, Out: io.Discard, Err: io.Discard}
	err := application.reportRun(t.Context(), "service.health", func(context.Context) error {
		return errPrivateFailure
	})
	if !errors.Is(err, errPrivateFailure) {
		t.Fatalf("result = %v", err)
	}
	output := capture.output()
	if !strings.Contains(output, "service.health failed") {
		t.Fatalf("missing health failure: %s", output)
	}
	assertFailureReported(t, output, "service.health", errPrivateFailure)
}

func TestCommandInvocationTelemetry(t *testing.T) {
	for _, testCase := range commandInvocationCases() {
		t.Run(testCase.name, func(t *testing.T) { runCommandInvocationCase(t, testCase) })
	}
}

func invocationCommand(application *Application, name string, run cobraRun) *cobra.Command {
	child := &cobra.Command{Use: name}
	if name == commandConfigure {
		child.Aliases = []string{"reconfigure"}
		child.Flags().Bool("telemetry", true, "")
		child.RunE = application.reportingSaved(commandConfigure, reportingTurnedOff, run)
	}
	if name == commandInstall {
		child.Flags().Bool("dry-run", false, "")
		child.RunE = application.reportingSaved(commandInstall, previewOnly, run)
	}

	return child
}

func runCommandInvocationCase(t *testing.T, testCase commandInvocationCase) {
	t.Helper()
	capture := captureTelemetry(t)
	directory := t.TempDir()
	path := filepath.Join(directory, "config.json")
	if testCase.saveConfig {
		value := configtest.Custom()
		value.Telemetry.Enabled = testCase.telemetryEnabled
		value.Telemetry.Logs = true
		if err := config.Save(path, value); err != nil {
			t.Fatal(err)
		}
	}
	application := &Application{ConfigPath: path, StateDir: directory, Out: io.Discard, Err: io.Discard}
	called := false
	run := func(_ *cobra.Command, _ []string) error {
		called = true

		return testCase.result
	}
	root := &cobra.Command{Use: "test", SilenceErrors: true, SilenceUsage: true}
	root.AddCommand(invocationCommand(application, testCase.command, run))
	root.SetArgs(append([]string{testCase.invocation}, testCase.args...))
	err := root.Execute()
	if !errors.Is(err, testCase.result) || !called {
		t.Fatalf("command result changed: %v", err)
	}
	assertInvocationReport(t, testCase, capture.output())
}

func assertInvocationReport(t *testing.T, testCase commandInvocationCase, output string) {
	t.Helper()
	if errors.Is(testCase.result, errPrivateFailure) {
		assertFailureReported(t, output, testCase.command, testCase.result)
	}
	if len(testCase.wantLogs) == 0 {
		if output != "" {
			t.Fatalf("telemetry sent without consent or during dry-run: %s", output)
		}

		return
	}
	for _, message := range testCase.wantLogs {
		if !strings.Contains(output, message) {
			t.Fatalf("missing lifecycle log %q", message)
		}
	}
}
