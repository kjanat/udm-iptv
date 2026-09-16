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

type recordingBackend struct {
	actions []Action
	fail    Action
}

func (b *recordingBackend) Apply(_ context.Context, action Action, _ Plan) error {
	b.actions = append(b.actions, action)
	if action == b.fail {
		return errors.New("injected failure")
	}

	return nil
}

func testPlan() Plan {
	return Plan{Config: config.Default(), ConfigPath: "/data/config.json", StateDir: "/data/iptv", Executable: "/tmp/iptv", SaveConfig: true}
}

func TestExecutionOrderAndEveryFailureBoundary(t *testing.T) {
	p := testPlan()
	want := []Action{Preflight, PreserveRuntime, SaveConfig, RemoveLegacy, CopyBinary, WriteFiles, Activate, CheckHealth, Cleanup}
	if !reflect.DeepEqual(p.Actions(), want) {
		t.Fatal("unexpected plan order")
	}
	for i, action := range want {
		t.Run(string(action), func(t *testing.T) {
			backend := &recordingBackend{fail: action}
			err := p.Execute(context.Background(), backend)
			if err == nil || !strings.Contains(err.Error(), string(action)) {
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
	for _, action := range p.Actions() {
		if action == SaveConfig {
			t.Fatal("existing config would be rewritten")
		}
	}
}
