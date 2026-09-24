// Package proxyinventory discovers proxy executables without launching daemons.
package proxyinventory

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/runtimebundle"
)

const unknownStatus = "unknown"

var errUnavailable = errors.New("unavailable")

// Proxy records executable discovery, not a successful runtime health check.
// Source is system, offline, missing or unknown (a lookup failed).
type Proxy struct {
	Name           string             `json:"name"`
	Available      bool               `json:"available"`
	Source         string             `json:"source"`
	Path           string             `json:"path,omitempty"`
	Version        string             `json:"version,omitempty"`
	VersionSource  string             `json:"versionSource,omitempty"`
	Revision       string             `json:"revision,omitempty"`
	BuildID        string             `json:"buildID,omitempty"`
	Architecture   string             `json:"architecture,omitempty"`
	Package        string             `json:"package,omitempty"`
	PackageVersion string             `json:"packageVersion,omitempty"`
	Features       map[string]Feature `json:"features,omitempty"`
	MetadataErrors []string           `json:"metadataErrors,omitempty"`
	Reason         string             `json:"reason,omitempty"`
}

// Inventory contains one observation for each supported proxy. A nil inventory
// means discovery was not performed, as with older saved diagnostic captures.
type Inventory []Proxy

// Discover checks PATH first, then the preserved runtime used by startup.
func Discover(stateDir string) Inventory {
	return discover(stateDir, exec.LookPath, runtimebundle.Command)
}

func discover(stateDir string, lookup func(string) (string, error), offline func(string, string, []string) (string, []string, error)) Inventory {
	result := make(Inventory, 0, 2)
	for _, program := range []string{config.ProxyImproxy, config.ProxyIgmpproxy} {
		result = append(result, inspect(stateDir, program, lookup, offline))
	}
	return result
}

func inspect(stateDir, program string, lookup func(string) (string, error), offline func(string, string, []string) (string, []string, error)) Proxy {
	item := Proxy{Name: program, Features: familyFeatures(program)}
	path, systemErr := lookup(program)
	if systemErr == nil {
		item.Available, item.Source, item.Path = true, "system", path
		return item
	}
	path, args, offlineErr := offline(stateDir, program, nil)
	if offlineErr == nil {
		// Command may return the preserved ELF loader rather than the program.
		path, offlineErr = lookup(path)
		if offlineErr == nil {
			item.Available, item.Source, item.Path = true, "offline", path
			if len(args) >= 3 && args[0] == "--library-path" {
				item.Path = filepath.Join(filepath.Dir(path), "program")
			}
			return item
		}
	}
	item.Source = "missing"
	item.Reason = "not found in PATH or the offline runtime"
	if !errors.Is(systemErr, exec.ErrNotFound) || !errors.Is(offlineErr, os.ErrNotExist) {
		item.Source = unknownStatus
		item.Reason = fmt.Sprintf("system: %v; offline: %v", systemErr, offlineErr)
	}
	return item
}

// Find returns an observed proxy. Unknown names and absent observations are
// explicitly unknown rather than treated as installed or missing.
func (inventory Inventory) Find(program string) Proxy {
	for _, item := range inventory {
		if item.Name == program {
			return item
		}
	}
	return Proxy{Name: program, Source: unknownStatus, Reason: "availability not checked"}
}

// Validate prevents choosing a proxy the observed host cannot currently launch.
func (inventory Inventory) Validate(program string) error {
	item := inventory.Find(program)
	if item.Available {
		return nil
	}
	return fmt.Errorf("%s %w: %s", program, errUnavailable, item.Reason)
}
