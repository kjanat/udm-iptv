//go:build linux && integration

package service

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestManagedProcessHelper(_ *testing.T) {
	if os.Args[len(os.Args)-1] != "process-helper" {
		return
	}
	time.Sleep(time.Hour)
}

func helperProcess(t *testing.T) *exec.Cmd {
	t.Helper()
	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestManagedProcessHelper$", "--", "process-helper")
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	configureGracefulStop(command)
	return command
}

func TestProxyStateFailureReapsProcess(t *testing.T) {
	for _, invalidTime := range []bool{false, true} {
		t.Run(map[bool]string{false: "write", true: "encode"}[invalidTime], func(t *testing.T) {
			command := helperProcess(t)
			state := RuntimeState{StartedAt: time.Now().UTC()}
			path := t.TempDir() // Cannot replace a directory with a state file.
			if invalidTime {
				state.StartedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
				path = filepath.Join(path, "state.json")
			}
			process, err := startProxy(command, path, state)
			if err == nil || process != nil || !strings.Contains(err.Error(), "daemon runtime state") {
				t.Fatalf("missing state failure: %v", err)
			}
			if command.ProcessState == nil {
				t.Fatal("child was not reaped")
			}
			if err := command.Process.Signal(syscall.Signal(0)); !errors.Is(err, os.ErrProcessDone) {
				t.Fatalf("child still running: %v", err)
			}
		})
	}
}

func TestProxyStateAndStop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	command := helperProcess(t)
	process, err := startProxy(command, path, RuntimeState{StartedAt: time.Now().UTC(), Proxy: "improxy"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := process.stop(); err != nil {
			t.Error(err)
		}
	})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state RuntimeState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if state.ProxyPID != command.Process.Pid || state.Proxy != "improxy" {
		t.Fatal("incorrect runtime state")
	}
	if err := process.stop(); err != nil {
		t.Fatal(err)
	}
	if command.ProcessState == nil {
		t.Fatal("child was not reaped")
	}
}
