package cli

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/config/configtest"
	"github.com/kjanat/udm-iptv/internal/telemetry"
)

// reportedCommands are the invocations telemetry must cover.
var reportedCommands = []string{
	commandConfigure,
	commandInstall,
	"uninstall",
	"restart",
	"upgrade",
	telemetry.OperationDaemon,
	"dhcp-hook",
}

var commandNameConstants = map[string]string{
	"commandConfigure": commandConfigure,
	"commandInstall":   commandInstall,
	"OperationDaemon":  telemetry.OperationDaemon,
}

// TestReportedCommandsWrapTheirOwnHandler keeps every reported command opted
// in. Wrapping replaced a walk over the built tree, so a handler that forgets
// to wrap now reports nothing and nothing else notices.
func TestReportedCommandsWrapTheirOwnHandler(t *testing.T) {
	t.Parallel()
	handlers := map[string]string{}
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		collectCommandHandlers(t, path, handlers)
	}
	for _, name := range reportedCommands {
		handler, found := handlers[name]
		if !found {
			t.Errorf("no command named %q is constructed in this package", name)

			continue
		}
		if !strings.HasPrefix(handler, "reporting") {
			t.Errorf("command %q runs %s without a reporting wrapper", name, handler)
		}
	}
}

func collectCommandHandlers(t *testing.T, path string, into map[string]string) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	ast.Inspect(file, func(node ast.Node) bool {
		literal, ok := node.(*ast.CompositeLit)
		if !ok {
			return true
		}
		name, handler := commandLiteral(literal)
		if name != "" && handler != "" {
			into[name] = handler
		}

		return true
	})
}

// commandLiteral returns the command name and the handler expression of a
// cobra.Command literal, or two empty strings for any other literal.
func commandLiteral(literal *ast.CompositeLit) (string, string) {
	selector, ok := literal.Type.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Command" {
		return "", ""
	}
	name, handler := "", ""
	for _, element := range literal.Elts {
		pair, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := pair.Key.(*ast.Ident)
		if !ok {
			continue
		}
		switch key.Name {
		case "Use":
			name = commandName(pair.Value)
		case "RunE":
			handler = handlerName(pair.Value)
		}
	}

	return name, handler
}

func commandName(expr ast.Expr) string {
	switch value := expr.(type) {
	case *ast.BasicLit:
		text, err := strconv.Unquote(value.Value)
		if err != nil {
			return ""
		}
		fields := strings.Fields(text)
		if len(fields) == 0 {
			return ""
		}

		return fields[0]
	case *ast.Ident:
		return commandNameConstants[value.Name]
	case *ast.SelectorExpr:
		return commandNameConstants[value.Sel.Name]
	default:
		return ""
	}
}

func handlerName(expr ast.Expr) string {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return "an inline handler"
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "an inline handler"
	}

	return selector.Sel.Name
}

func TestTelemetryInitializationFailureIsVisibleAndNonfatal(t *testing.T) {
	previous := telemetry.DSN
	telemetry.DSN = ""
	t.Cleanup(func() { telemetry.DSN = previous })
	for _, entry := range []string{"command", "operation"} {
		t.Run(entry, func(t *testing.T) {
			var output bytes.Buffer
			application := &Application{ConfigPath: filepath.Join(t.TempDir(), "config.json"), Err: &output}
			value := configtest.Custom()
			value.Telemetry = config.Telemetry{Enabled: true, Errors: true}
			if err := config.Save(application.ConfigPath, value); err != nil {
				t.Fatal(err)
			}
			called := false
			run := func(context.Context) error {
				called = true
				return errPrivateFailure
			}
			var err error
			if entry == "operation" {
				err = application.reportRun(t.Context(), "install", run)
			} else {
				command := &cobra.Command{Use: "install"}
				command.SetContext(t.Context())
				err = application.reporting("install", func(command *cobra.Command, _ []string) error { return run(command.Context()) })(command, nil)
			}
			if !called || !errors.Is(err, errPrivateFailure) {
				t.Fatalf("telemetry failure replaced the operation: called=%v err=%v", called, err)
			}
			if !strings.Contains(output.String(), "Telemetry unavailable") || !strings.Contains(output.String(), "no telemetry endpoint") {
				t.Fatalf("missing initialization diagnosis: %q", output.String())
			}
		})
	}
}
