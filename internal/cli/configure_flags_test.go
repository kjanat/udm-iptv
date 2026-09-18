package cli

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/ui"
)

// flagsHandledOutsideTable are applied by apply and applyReporting themselves.
var flagsHandledOutsideTable = map[string]bool{
	"profile":   true,
	"telemetry": true,
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

// A fresh console starts from the KPN profile so the wizard has something to
// show. Selecting custom must not turn that draft into a saved KPN setup.
func TestFreshCustomProfileDoesNotInheritKPN(t *testing.T) {
	t.Parallel()
	command := &cobra.Command{Use: "configure"}
	flags := &configureFlags{telemetry: config.Default().Telemetry}
	flags.bind(command)
	if err := command.Flags().Set("profile", "custom"); err != nil {
		t.Fatal(err)
	}
	kpn := config.DefaultKPN()
	if kpn.WAN.VLAN != config.DefaultKPNVLAN || len(kpn.WAN.NATDestinations) == 0 {
		t.Fatal("the KPN profile is empty, so this asserts nothing")
	}
	value := startingPoint(command, kpn, true)
	if err := flags.apply(command, &value); err != nil {
		t.Fatal(err)
	}
	if value.Profile != "custom" {
		t.Fatalf("profile = %q", value.Profile)
	}
	if value.WAN.VLAN == config.DefaultKPNVLAN || len(value.WAN.NATDestinations) != 0 || len(value.WAN.DHCPOptions) != 0 {
		t.Fatalf("fresh custom inherited KPN settings: %#v", value.WAN)
	}
}

// Custom on a configured console means "these settings, no provider profile",
// so it must keep what is already saved.
func TestExistingCustomProfileKeepsSavedSettings(t *testing.T) {
	t.Parallel()
	command := &cobra.Command{Use: "configure"}
	flags := &configureFlags{telemetry: config.Default().Telemetry}
	flags.bind(command)
	if err := command.Flags().Set("profile", "custom"); err != nil {
		t.Fatal(err)
	}
	saved := config.DefaultKPN()
	saved.WAN.VLAN = 101
	saved.WAN.NATDestinations = []string{"198.51.100.0/24"}
	value := startingPoint(command, saved, false)
	if err := flags.apply(command, &value); err != nil {
		t.Fatal(err)
	}
	if value.WAN.VLAN != 101 || !slices.Equal(value.WAN.NATDestinations, []string{"198.51.100.0/24"}) {
		t.Fatalf("custom wiped a saved configuration: %#v", value.WAN)
	}
}

func TestEveryFlagFieldIsAWizardField(t *testing.T) {
	t.Parallel()
	flags, command := boundConfigureFlags()
	value := config.Default()
	known := ui.FieldKeys()
	for _, field := range flags.settings(&value) {
		if field.field == "" {
			continue
		}
		if !slices.Contains(known, field.field) {
			t.Errorf("--%s points at wizard field %q, which the form never asks", field.name, field.field)
		}
	}
	for _, name := range []string{"dhcp", "wan-vlan", "profile"} {
		if err := command.Flags().Set(name, distinctValue(command.Flags().Lookup(name))); err != nil {
			t.Fatal(err)
		}
	}
	if got := flags.answered(command, &value); !slices.Equal(got, ui.Answered{"profile", "vlan", "dhcp"}) {
		t.Fatalf("answered = %q", got)
	}
}
