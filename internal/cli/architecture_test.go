package cli

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// boundaryRule rejects one dependency of one package.
type boundaryRule struct {
	reason string
	banned func(pkg, dependency string) bool
}

func commandsStayInCLI(pkg, dependency string) bool {
	return pkg != "cli" && (dependency == "github.com/spf13/cobra" || dependency == "github.com/spf13/pflag")
}

// cli is the composition root: it starts the terminal session and translates
// its errors, but renders nothing itself.
func renderingStaysInUI(pkg, dependency string) bool {
	return pkg != "ui" && pkg != "cli" && strings.HasPrefix(dependency, "charm.land/")
}

func domainsIgnoreCommands(pkg, dependency string) bool {
	return pkg != "cli" && dependency == "github.com/kjanat/udm-iptv/internal/cli"
}

var boundaryRules = []boundaryRule{
	{reason: "commands belong to cli", banned: commandsStayInCLI},
	{reason: "terminal rendering belongs to ui", banned: renderingStaysInUI},
	{reason: "domain packages must not depend on the command layer", banned: domainsIgnoreCommands},
}

// TestPackageBoundaries guards the invariants that keep the layering honest.
// It deliberately does not enumerate which internal package may import which:
// a hand-maintained graph blocks reuse of shared primitives and goes stale
// faster than the code it describes.
func TestPackageBoundaries(t *testing.T) {
	t.Parallel()
	err := filepath.WalkDir("..", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		checkImports(t, path)

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func checkImports(t *testing.T, path string) {
	t.Helper()
	pkg := filepath.Base(filepath.Dir(path))
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for _, spec := range file.Imports {
		dependency := strings.Trim(spec.Path.Value, `"`)
		for _, rule := range boundaryRules {
			if rule.banned(pkg, dependency) {
				t.Errorf("%s imports %s: %s", path, dependency, rule.reason)
			}
		}
	}
}
