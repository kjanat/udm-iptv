package cli

import (
	"encoding/json"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/kjanat/udm-iptv/internal/config"
)

// flagsHandledOutsideTable are applied by apply and applyReporting themselves.
var flagsHandledOutsideTable = map[string]bool{
	"non-interactive": true,
	"profile":         true,
	"telemetry":       true,
}

func boundConfigureFlags() (*configureFlags, *cobra.Command) {
	flags := &configureFlags{telemetry: config.Default().Telemetry}
	command := &cobra.Command{Use: commandConfigure}
	flags.bind(command)

	return flags, command
}

func TestEveryConfigureFlagReachesTheConfiguration(t *testing.T) {
	t.Parallel()
	flags, command := boundConfigureFlags()
	value := config.Default()
	table := map[string]bool{}
	for _, field := range flags.overrides(&value) {
		if table[field.name] {
			t.Errorf("override %q is listed twice", field.name)
		}
		table[field.name] = true
	}
	command.Flags().VisitAll(func(flag *pflag.Flag) {
		if !flagsHandledOutsideTable[flag.Name] && !table[flag.Name] {
			t.Errorf("--%s is bound but never written to the configuration", flag.Name)
		}
	})
	for name := range table {
		if command.Flags().Lookup(name) == nil {
			t.Errorf("override %q matches no bound flag", name)
		}
	}
}

// distinctValue returns a value that differs from the flag's own default, so
// setting it always registers as changed.
func distinctValue(flag *pflag.Flag) string {
	switch flag.Value.Type() {
	case "bool":
		if flag.DefValue == "false" {
			return "true"
		}

		return "false"
	case "int":
		return "7"
	case "float64":
		return "0.25"
	case "stringSlice":
		return "alpha,beta"
	default:
		return "distinct-" + flag.Name
	}
}

// applyOneFlag sets a single flag and returns the resulting configuration.
func applyOneFlag(t *testing.T, name string) string {
	t.Helper()
	flags, command := boundConfigureFlags()
	flag := command.Flags().Lookup(name)
	if err := command.Flags().Set(name, distinctValue(flag)); err != nil {
		t.Fatalf("set --%s: %v", name, err)
	}
	value := config.Default()
	if err := flags.apply(command, &value); err != nil {
		t.Fatalf("apply --%s: %v", name, err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

// TestConfigureOverridesWriteDistinctFields catches a table row wired to the
// wrong field: two rows writing one field produce the same configuration.
func TestConfigureOverridesWriteDistinctFields(t *testing.T) {
	t.Parallel()
	flags, _ := boundConfigureFlags()
	value := config.Default()
	baseline, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, field := range flags.overrides(&value) {
		result := applyOneFlag(t, field.name)
		if result == string(baseline) {
			t.Errorf("--%s changed nothing", field.name)

			continue
		}
		if owner, taken := seen[result]; taken {
			t.Errorf("--%s and --%s write the same field", owner, field.name)

			continue
		}
		seen[result] = field.name
	}
}
