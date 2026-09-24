package proxyinventory

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
)

func TestDiscoverySeparatesSystemOfflineAndMissing(t *testing.T) {
	lookup := func(name string) (string, error) {
		if name == "improxy" {
			return "/usr/sbin/improxy", nil
		}
		if name == "/cache/loader" {
			return name, nil
		}
		return "", exec.ErrNotFound
	}
	offline := func(_, program string, _ []string) (string, []string, error) {
		if program != "igmpproxy" {
			t.Fatal("system binary unnecessarily resolved offline")
		}
		return "/cache/loader", []string{"--library-path", "/cache/lib", "/cache/program"}, nil
	}
	inventory := discover("/cache", lookup, offline)
	if got := inventory.Find("improxy"); !got.Available || got.Source != "system" || got.Path != "/usr/sbin/improxy" {
		t.Fatalf("system discovery = %+v", got)
	}
	if got := inventory.Find("igmpproxy"); !got.Available || got.Source != "offline" || got.Path != "/cache/program" {
		t.Fatalf("offline discovery = %+v", got)
	}
}

func TestDiscoveryDoesNotConfuseFailureWithAbsence(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		source string
	}{
		{"missing", exec.ErrNotFound, "missing"},
		{"permission", os.ErrPermission, "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			inventory := discover(t.TempDir(), func(string) (string, error) { return "", test.err },
				func(string, string, []string) (string, []string, error) { return "", nil, os.ErrNotExist })
			for _, item := range inventory {
				if item.Available || item.Source != test.source || inventory.Validate(item.Name) == nil {
					t.Fatalf("unexpected discovery = %+v", item)
				}
			}
		})
	}
}

func TestInspectDoesNotRunUnknownExecutable(t *testing.T) {
	directory := t.TempDir()
	marker := filepath.Join(directory, "started")
	binary := filepath.Join(directory, "improxy")
	if err := atomicfile.Write(binary, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	inventory := Inspect(context.Background(), t.TempDir())
	if !inventory.Find("improxy").Available {
		t.Fatal("executable not discovered")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unknown executable was launched")
	}
}
