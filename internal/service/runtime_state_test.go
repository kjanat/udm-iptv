package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteRuntimeState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	want := RuntimeState{StartedAt: time.Now().UTC(), Proxy: "improxy", ProxyPID: 42}
	if err := writeRuntimeState(path, want); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got RuntimeState
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("runtime state: got %+v, want %+v", got, want)
	}
}

func TestWriteRuntimeStateFailures(t *testing.T) {
	for _, test := range []struct {
		name  string
		state RuntimeState
	}{
		{"encode", RuntimeState{StartedAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}},
		{"save", RuntimeState{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := writeRuntimeState(t.TempDir(), test.state)
			if err == nil || !strings.Contains(err.Error(), test.name+" daemon runtime state") {
				t.Fatalf("missing state failure context: %v", err)
			}
		})
	}
}
