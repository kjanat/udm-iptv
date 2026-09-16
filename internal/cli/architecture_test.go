package cli

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestPackageBoundaries(t *testing.T) {
	const prefix = "github.com/kjanat/udm-iptv/internal/"
	allowed := map[string][]string{
		"atomicfile":    {"filemode"},
		"filemode":      {},
		"firmware":      {"filemode"},
		"config":        {"filemode"},
		"device":        {"config"},
		"network":       {"config"},
		"runtimebundle": {"filemode"},
		"telemetry":     {"config", "filemode"},
		"ui":            {"config"},
		"service":       {"config", "network", "runtimebundle", "atomicfile", "telemetry", "filemode"},
		"diagnostics":   {"config", "network", "service"},
		"installer":     {"config", "network", "service", "runtimebundle", "atomicfile", "filemode"},
	}
	err := filepath.WalkDir("..", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		pkg := filepath.Base(filepath.Dir(path))
		if pkg == "cli" {
			return nil
		}
		dependencies, known := allowed[pkg]
		if !known {
			t.Errorf("declare ownership before adding package %s", pkg)

			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			dependency, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			if local, ok := strings.CutPrefix(dependency, prefix); ok && !slices.Contains(dependencies, local) {
				t.Errorf("%s must not depend on %s", path, local)
			}
			if dependency == "github.com/spf13/cobra" || dependency == "github.com/spf13/pflag" {
				t.Errorf("command dependency outside cli: %s", path)
			}
			if pkg != "ui" && strings.HasPrefix(dependency, "charm.land/") {
				t.Errorf("terminal dependency outside ui: %s", path)
			}
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
