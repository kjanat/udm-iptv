package installer

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/kjanat/udm-iptv/internal/config"
)

var errInjectedPlanStep = errors.New("injected failure")

type recordingBackend struct {
	actions []string
	fail    string
}

func (b *recordingBackend) record(action string) error {
	b.actions = append(b.actions, action)
	if action == b.fail {
		return errInjectedPlanStep
	}

	return nil
}

func (b *recordingBackend) Preflight(context.Context, Plan) error {
	return b.record("Check installation prerequisites")
}

func (b *recordingBackend) PreserveRuntime(context.Context, Plan) error {
	return b.record("Preserve the proxy and shared libraries offline")
}

func (b *recordingBackend) SaveConfig(context.Context, Plan) error {
	return b.record("Save configuration")
}

func (b *recordingBackend) RemoveLegacy(context.Context, Plan) error {
	return b.record("Remove legacy Debian package if installed")
}

func (b *recordingBackend) CopyBinary(context.Context, Plan) error {
	return b.record("Install persistent executable")
}

func (b *recordingBackend) WriteFiles(context.Context, Plan) error {
	return b.record("Write service, links, tmpfiles rule and shell completion")
}

func (b *recordingBackend) Activate(context.Context, Plan) error {
	return b.record("Reload systemd, enable and restart service")
}

func (b *recordingBackend) CheckHealth(context.Context, Plan) error {
	return b.record("Wait for stable proxy readiness")
}

func (b *recordingBackend) Cleanup(context.Context, Plan) error {
	return b.record("Remove obsolete legacy recovery files after health verification")
}

func stepNames(p Plan) []string {
	names := make([]string, 0, len(p.steps()))
	for _, stage := range p.steps() {
		names = append(names, stage.name)
	}

	return names
}

func testPlan() Plan {
	return Plan{Config: config.Default(), ConfigPath: "/data/config.json", StateDir: "/data/iptv", Executable: "/tmp/iptv", SaveConfig: true}
}

func TestExecutionOrderAndEveryFailureBoundary(t *testing.T) {
	p := testPlan()
	want := []string{
		"Check installation prerequisites",
		"Preserve the proxy and shared libraries offline",
		"Save configuration",
		"Remove legacy Debian package if installed",
		"Install persistent executable",
		"Write service, links, tmpfiles rule and shell completion",
		"Reload systemd, enable and restart service",
		"Wait for stable proxy readiness",
		"Remove obsolete legacy recovery files after health verification",
	}
	if !reflect.DeepEqual(stepNames(p), want) {
		t.Fatal("unexpected plan order")
	}
	for i, action := range want {
		t.Run(action, func(t *testing.T) {
			backend := &recordingBackend{fail: action}
			err := p.Execute(context.Background(), backend)
			if err == nil || !strings.Contains(err.Error(), action) {
				t.Fatalf("missing action failure: %v", err)
			}
			if !reflect.DeepEqual(backend.actions, want[:i+1]) {
				t.Fatalf("continued after failure: %v", backend.actions)
			}
		})
	}
	backend := &recordingBackend{}
	err := p.Execute(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(backend.actions, want) {
		t.Fatal("incomplete execution")
	}
}

func TestInvalidPlanAndCancellationNeverApply(t *testing.T) {
	p := testPlan()
	p.Config.WAN.VLAN = -1
	backend := &recordingBackend{}
	err := p.Execute(context.Background(), backend)
	if err == nil {
		t.Fatal("invalid plan accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = testPlan().Execute(ctx, backend)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if len(backend.actions) != 0 {
		t.Fatal("invalid or cancelled plan applied")
	}
}

func TestPreviewDoesNotPrintConfiguration(t *testing.T) {
	p := testPlan()
	p.Config.WAN.DHCPOptions = []string{"-V", "private-identifier"}
	var output bytes.Buffer
	err := p.Preview(&output)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "private-identifier") || !strings.Contains(output.String(), "Preview complete") {
		t.Fatal(output.String())
	}
	p.SaveConfig = false
	for _, name := range stepNames(p) {
		if name == "Save configuration" {
			t.Fatal("existing config would be rewritten")
		}
	}
}
