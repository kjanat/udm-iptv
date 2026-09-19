package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/device"
	"github.com/kjanat/udm-iptv/internal/filemode"
	"github.com/kjanat/udm-iptv/internal/installer"
	"github.com/kjanat/udm-iptv/internal/ui"
)

// legacyCandidates lists the legacy configurations an install may import, in
// the order they are trusted.
func legacyCandidates(stateDir string) []string {
	return []string{
		"/etc/udm-iptv.conf",
		filepath.Join(stateDir, "legacy.conf"),
		filepath.Join(stateDir, "udm-iptv.conf"),
	}
}

// loadOrImportConfig loads the saved configuration, falls back to importing a
// legacy config file with the console's ports filled in, and otherwise
// reports fresh so the caller can suggest a provider profile.
func loadOrImportConfig(path, stateDir string, seed func(config.Config) config.Config) (config.Config, bool, error) {
	current, err := config.Load(path)
	if err == nil {
		return current, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return config.Config{}, false, fmt.Errorf("load configuration from %s: %w", path, err)
	}
	legacy, found, err := config.ImportFirstLegacy(legacyCandidates(stateDir))
	if err != nil {
		return config.Config{}, false, fmt.Errorf("import the legacy configuration: %w", err)
	}
	if found {
		return seed(legacy), false, nil
	}

	return seed(config.DefaultKPN()), true, nil
}

// startingPoint keeps an unconfigured console off the KPN defaults when the
// user asked for the custom profile. Applying "custom" relabels the draft it
// is given, which is the right answer for a saved configuration and the wrong
// one for a fresh console, where that draft is the KPN profile.
func startingPoint(command *cobra.Command, value config.Config, fresh bool, seed func(config.Config) config.Config) config.Config {
	if !fresh || !command.Flags().Changed("profile") || command.Flags().Lookup("profile").Value.String() != config.ProfileCustom {
		return value
	}

	return seed(config.Default())
}

// ErrNotConfigured reports a console with neither a saved nor an importable
// configuration.
var ErrNotConfigured = errors.New("udm-iptv is not configured; run udm-iptv install or udm-iptv configure set")

var (
	errNothingToSet   = errors.New("no setting given; pass at least one flag")
	errUnknownSetting = errors.New("unknown setting")
)

const (
	flagDebug     = "debug"
	flagTelemetry = "telemetry"
)

// override pairs one flag with the configuration field it reads and writes
// and the wizard field that asks for it.
type override struct {
	name  string
	field string
	set   func()
	get   func() any
}

// configureFlags holds what the configure flags write into. Every flag but
// --profile and --telemetry maps to exactly one field, so they share one
// table instead of a branch each.
type configureFlags struct {
	profile, wanInterface, iptvInterface string
	vlanMAC, staticAddress, proxy        string
	vlan, igmpVersion                    int
	dhcpOptions, natDestinations         []string
	proxySources, lanInterfaces          []string
	dhcpRoutes                           string
	dhcp                                 bool
	quickLeave, debug                    bool
	telemetry                            config.Telemetry
	seed                                 func(config.Config) config.Config
}

func (f *configureFlags) overrides(value *config.Config) []override {
	return []override{
		{"wan-interface", "wan-port", func() { value.WAN.Interface = f.wanInterface }, func() any { return value.WAN.Interface }},
		{"wan-vlan", "vlan", func() { value.WAN.VLAN = f.vlan }, func() any { return value.WAN.VLAN }},
		{"iptv-interface", "vlan-interface", func() { value.WAN.VLANInterface = f.iptvInterface }, func() any { return value.WAN.VLANInterface }},
		{"vlan-mac", "vlan-mac", func() { value.WAN.VLANMAC = f.vlanMAC }, func() any { return value.WAN.VLANMAC }},
		{"dhcp", "dhcp", func() { value.WAN.DHCP = f.dhcp }, func() any { return value.WAN.DHCP }},
		{"dhcp-option", "dhcp-options", func() { value.WAN.DHCPOptions = f.dhcpOptions }, func() any { return value.WAN.DHCPOptions }},
		{"static-address", "static-address", func() { value.WAN.StaticAddress = f.staticAddress }, func() any { return value.WAN.StaticAddress }},
		{"dhcp-routes", "dhcp-routes", func() { value.WAN.DHCPRoutes = config.RoutePolicy(f.dhcpRoutes) }, func() any { return value.WAN.DHCPRoutes }},
		{"nat-destination", "nat", func() { value.WAN.NATDestinations = f.natDestinations }, func() any { return value.WAN.NATDestinations }},
		{"proxy-source", "proxy-sources", func() { value.Proxy.SourceRanges = f.proxySources }, func() any { return value.Proxy.SourceRanges }},
		{"lan-interface", "lan", func() { value.LAN.Interfaces = f.lanInterfaces }, func() any { return value.LAN.Interfaces }},
		{"proxy", "proxy", func() { value.Proxy.Program = f.proxy }, func() any { return value.Proxy.Program }},
		{"igmp-version", "igmp", func() { value.Proxy.IGMPVersion = f.igmpVersion }, func() any { return value.Proxy.IGMPVersion }},
		{"quickleave", "quickleave", func() { value.Proxy.QuickLeave = f.quickLeave }, func() any { return value.Proxy.QuickLeave }},
		{flagDebug, flagDebug, func() { value.Proxy.Debug = f.debug }, func() any { return value.Proxy.Debug }},
		{"telemetry-errors", "", func() { value.Telemetry.Errors = f.telemetry.Errors }, func() any { return value.Telemetry.Errors }},
		{"telemetry-logs", "", func() { value.Telemetry.Logs = f.telemetry.Logs }, func() any { return value.Telemetry.Logs }},
		{"telemetry-metrics", "", func() { value.Telemetry.Metrics = f.telemetry.Metrics }, func() any { return value.Telemetry.Metrics }},
		{"telemetry-tracing", "", func() { value.Telemetry.Tracing = f.telemetry.Tracing }, func() any { return value.Telemetry.Tracing }},
		{"telemetry-presets", "", func() { value.Telemetry.Presets = f.telemetry.Presets }, func() any { return value.Telemetry.Presets }},
		{"telemetry-network-identity", "", func() { value.Telemetry.NetworkIdentity = f.telemetry.NetworkIdentity }, func() any { return value.Telemetry.NetworkIdentity }},
		{"telemetry-trace-rate", "", func() { value.Telemetry.TraceRate = f.telemetry.TraceRate }, func() any { return value.Telemetry.TraceRate }},
	}
}

// settings lists every readable setting by flag name, including the two
// flags apply handles itself.
func (f *configureFlags) settings(value *config.Config) []override {
	return append([]override{
		{"profile", "profile", nil, func() any { return value.Profile }},
		{flagTelemetry, flagTelemetry, nil, func() any { return value.Telemetry.Enabled }},
	}, f.overrides(value)...)
}

func (f *configureFlags) setting(value *config.Config, name string) (any, error) {
	for _, field := range f.settings(value) {
		if field.name == name {
			return field.get(), nil
		}
	}

	return nil, fmt.Errorf("%w %q", errUnknownSetting, name)
}

func (f *configureFlags) settingNames(value *config.Config) []string {
	fields := f.settings(value)
	names := make([]string, 0, len(fields))
	for _, field := range fields {
		names = append(names, field.name)
	}

	return names
}

// answered lists the wizard fields the given flags already filled in.
func (f *configureFlags) answered(command *cobra.Command, value *config.Config) ui.Answered {
	var keys ui.Answered
	for _, field := range f.settings(value) {
		if field.field != "" && command.Flags().Changed(field.name) {
			keys = append(keys, field.field)
		}
	}

	return keys
}

func formatSetting(value any) string {
	if list, ok := value.([]string); ok {
		return strings.Join(list, ",")
	}

	return fmt.Sprint(value)
}

// apply folds the flags the user actually set into value, profile first
// because it replaces the whole configuration.
func (f *configureFlags) apply(command *cobra.Command, value *config.Config) error {
	flags := command.Flags()
	if flags.Changed("profile") {
		selected, err := config.FromProfile(f.profile, *value)
		if err != nil {
			return fmt.Errorf("apply --profile: %w", err)
		}
		*value = selected
	}
	f.applyReporting(command, value)
	for _, field := range f.overrides(value) {
		if flags.Changed(field.name) {
			field.set()
		}
	}
	if !flags.Changed("static-address") {
		config.NormalizeAddressing(value)
	}

	return nil
}

// applyReporting runs before the individual product flags so that turning
// reporting back on restores the defaults those flags then refine.
func (f *configureFlags) applyReporting(command *cobra.Command, value *config.Config) {
	if !command.Flags().Changed(flagTelemetry) {
		return
	}
	if f.telemetry.Enabled && reportingFullyOff(value.Telemetry) {
		value.Telemetry = config.Default().Telemetry
	}
	value.Telemetry.Enabled = f.telemetry.Enabled
}

func reportingFullyOff(value config.Telemetry) bool {
	return !value.Enabled && !value.Errors && !value.Logs && !value.Metrics && !value.Tracing
}

func (f *configureFlags) bind(command *cobra.Command) {
	flags := command.Flags()
	flags.StringVar(&f.profile, "profile", "", "provider profile ID")
	flags.StringVar(&f.wanInterface, "wan-interface", "", "physical WAN interface")
	flags.IntVar(&f.vlan, "wan-vlan", 0, "IPTV VLAN ID; zero disables VLAN creation")
	flags.StringVar(&f.iptvInterface, "iptv-interface", "", "name of the IPTV VLAN interface")
	flags.StringVar(&f.vlanMAC, "vlan-mac", "", "custom MAC address for the IPTV VLAN")
	flags.BoolVar(&f.dhcp, "dhcp", false, "obtain the IPTV address through DHCP")
	flags.StringSliceVar(&f.dhcpOptions, "dhcp-option", nil, "argument passed to udhcpc; repeatable")
	flags.StringVar(&f.staticAddress, "static-address", "", "static IPTV address in CIDR notation")
	flags.StringVar(&f.dhcpRoutes, "dhcp-routes", "", "routes to accept from a lease: no-default, allow-default or none")
	flags.StringSliceVar(&f.natDestinations, "nat-destination", nil, "destination prefix to masquerade; repeatable")
	flags.StringSliceVar(&f.proxySources, "proxy-source", nil, "allowed multicast source prefix; repeatable")
	flags.StringSliceVar(&f.lanInterfaces, "lan-interface", nil, "downstream LAN interface; repeatable")
	flags.StringVar(&f.proxy, "proxy", "", "multicast proxy: improxy or igmpproxy")
	flags.IntVar(&f.igmpVersion, "igmp-version", 0, "IGMP version: 2 or 3")
	flags.BoolVar(&f.quickLeave, "quickleave", false, "enable quickleave")
	flags.BoolVar(&f.debug, flagDebug, false, "enable verbose proxy logging")
	flags.BoolVar(&f.telemetry.Enabled, flagTelemetry, false, "send diagnostic data")
	flags.BoolVar(&f.telemetry.Errors, "telemetry-errors", true, "report software failures when telemetry is enabled")
	flags.BoolVar(&f.telemetry.Logs, "telemetry-logs", true, "send structured lifecycle logs; never raw proxy logs")
	flags.BoolVar(&f.telemetry.Metrics, "telemetry-metrics", true, "send bounded operational counters")
	flags.BoolVar(&f.telemetry.Tracing, "telemetry-tracing", true, "send sampled operation timings")
	flags.BoolVar(&f.telemetry.Presets, "telemetry-presets", true, "share selected settings, changes and a random installation ID")
	flags.BoolVar(&f.telemetry.NetworkIdentity, "telemetry-network-identity", true, "include public IP and reverse-DNS hostname in research")
	flags.Float64Var(&f.telemetry.TraceRate, "telemetry-trace-rate", config.DefaultTraceRate, "fraction of operations traced, from 0 to 1")
	for _, name := range []string{
		"telemetry-errors", "telemetry-logs", "telemetry-metrics", "telemetry-tracing",
		"telemetry-presets", "telemetry-network-identity", "telemetry-trace-rate",
	} {
		_ = flags.MarkHidden(name)
	}
	_ = command.RegisterFlagCompletionFunc("profile", completeProfiles)
	_ = command.RegisterFlagCompletionFunc("proxy", completeValues("improxy\trecommended on current UniFi OS", "igmpproxy\tlegacy proxy with a source allowlist"))
	_ = command.RegisterFlagCompletionFunc("igmp-version", completeValues("3\trecommended for current receivers", "2\tlegacy receivers"))
}

func completeProfiles(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	profiles := config.Profiles()
	values := make([]string, 0, len(profiles))
	for _, value := range profiles {
		values = append(values, value.ID+"\t"+value.Name)
	}

	return values, cobra.ShellCompDirectiveNoFileComp
}

// seedInterfaces replaces a configuration's placeholder interfaces with the
// ones detected on this console.
func (application *Application) seedInterfaces() func(config.Config) config.Config {
	if application.seed != nil {
		return application.seed
	}

	return device.WithInterfaces
}

func (application *Application) configureCommand() *cobra.Command {
	flags := &configureFlags{telemetry: config.Default().Telemetry, seed: application.seedInterfaces()}
	command := &cobra.Command{
		Use:     commandConfigure,
		Aliases: []string{"reconfigure"},
		Short:   "Configure IPTV interactively",
		Long:    "Flags answer questions in advance; fully answered pages are skipped.",
		Args:    cobra.NoArgs,
		RunE: application.reportingSaved(commandConfigure, reportingTurnedOff, func(command *cobra.Command, _ []string) error {
			application.providerSuggestion = ""
			value, fresh, err := application.draft(command, flags)
			if err != nil {
				return err
			}
			if err := application.askForConfiguration(command, &value, fresh, flags.answered(command, &value)); err != nil {
				return err
			}

			return application.saveConfiguration(command, value)
		}),
	}
	flags.bind(command)
	command.AddCommand(application.configureSetCommand(), application.configureGetCommand())

	return command
}

func (application *Application) configureSetCommand() *cobra.Command {
	flags := &configureFlags{telemetry: config.Default().Telemetry, seed: application.seedInterfaces()}
	command := &cobra.Command{
		Use:   "set",
		Short: "Write settings from flags without opening the form",
		Args:  cobra.NoArgs,
		RunE: application.reportingSaved(commandConfigureSet, reportingTurnedOff, func(command *cobra.Command, _ []string) error {
			if command.Flags().NFlag() == 0 {
				return errNothingToSet
			}
			value, _, err := application.draft(command, flags)
			if err != nil {
				return err
			}

			return application.saveConfiguration(command, value)
		}),
	}
	flags.bind(command)

	return command
}

func (application *Application) configureGetCommand() *cobra.Command {
	flags := &configureFlags{}
	command := &cobra.Command{
		Use:   "get [setting]",
		Short: "Print the configuration, or one setting by flag name",
		Args:  cobra.MaximumNArgs(1),
		ValidArgsFunction: func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			return flags.settingNames(&config.Config{}), cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(_ *cobra.Command, args []string) error {
			value, fresh, err := loadOrImportConfig(application.ConfigPath, application.StateDir, application.seedInterfaces())
			if err != nil {
				return err
			}
			if fresh {
				return ErrNotConfigured
			}
			if len(args) == 0 {
				data, err := json.MarshalIndent(value, "", "  ")
				if err != nil {
					return fmt.Errorf("encode the configuration: %w", err)
				}

				return writef(application.Out, "%s\n", data)
			}
			setting, err := flags.setting(&value, args[0])
			if err != nil {
				return err
			}

			return writef(application.Out, "%s\n", formatSetting(setting))
		},
	}

	return command
}

// draft loads the saved configuration and folds the given flags into it.
func (application *Application) draft(command *cobra.Command, flags *configureFlags) (config.Config, bool, error) {
	value, fresh, err := loadOrImportConfig(application.ConfigPath, application.StateDir, application.seedInterfaces())
	if err != nil {
		return config.Config{}, false, err
	}
	value = startingPoint(command, value, fresh, flags.seed)
	if err := flags.apply(command, &value); err != nil {
		return config.Config{}, false, err
	}

	return value, fresh, nil
}

// askForConfiguration opens the wizard. A fresh configuration asks the
// reporting question first and looks the provider up from the answer, so an
// explicitly named profile needs no lookup at all.
func (application *Application) askForConfiguration(command *cobra.Command, value *config.Config, fresh bool, answered ui.Answered) error {
	if fresh && !command.Flags().Changed("profile") {
		return application.configureFreshForm(command.Context(), value, answered)
	}

	return application.configureForm(command.Context(), value, answered)
}

// saveConfiguration validates and persists the configuration, then restarts an
// installed service, recording both for the telemetry report.
func (application *Application) saveConfiguration(command *cobra.Command, value config.Config) (result error) {
	if err := value.Validate(); err != nil {
		return fmt.Errorf("check the configuration: %w", err)
	}
	release, err := installer.AcquireLock(application.StateDir)
	if err != nil {
		return fmt.Errorf("start the configuration change: %w", err)
	}
	defer func() { result = errors.Join(result, release()) }()
	installed, previous, err := application.persistConfiguration(value)
	if err != nil {
		return err
	}
	if !installed {
		return writeString(application.Out, "The service is not installed; udm-iptv install applies the configuration.\n")
	}
	err = application.restart(command.Context(), true)
	application.reportApplied = err == nil
	if err == nil || previous == nil {
		return err
	}

	return errors.Join(err, application.restoreConfiguration(command.Context(), previous))
}

// persistConfiguration writes value and returns whether the service is
// installed and the file it replaced, nil when there was none.
func (application *Application) persistConfiguration(value config.Config) (bool, []byte, error) {
	installed := installer.Installed(application.StateDir)
	if installed {
		if err := installer.PreserveProxy(application.StateDir, value.Proxy.Program); err != nil {
			return false, nil, fmt.Errorf("snapshot the proxy into %s: %w", application.StateDir, err)
		}
	}
	previous, err := os.ReadFile(application.ConfigPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, nil, fmt.Errorf("read the configuration being replaced: %w", err)
	}
	if err := config.Save(application.ConfigPath, value); err != nil {
		return false, nil, fmt.Errorf("save configuration to %s: %w", application.ConfigPath, err)
	}
	application.reportConfig = &value
	if err := writef(application.Out, "Configuration saved to %s.\n", application.ConfigPath); err != nil {
		return false, nil, err
	}

	return installed, previous, nil
}

// rejectedSuffix names the copy a configuration keeps when the service
// would not run with it.
const rejectedSuffix = ".rejected"

// restoreConfiguration puts the configuration the service was running back
// after a change it would not start with, keeps the rejected one beside it,
// and restarts the service on the restored one.
func (application *Application) restoreConfiguration(ctx context.Context, previous []byte) error {
	rejected, err := rollbackConfiguration(application.ConfigPath, previous)
	if err != nil {
		return err
	}
	if err := writef(application.Out, "The service did not come up with the new configuration. The previous configuration is back at %s; the rejected one is kept at %s.\n", application.ConfigPath, rejected); err != nil {
		return err
	}
	if err := application.restart(ctx, true); err != nil {
		return fmt.Errorf("restart on the previous configuration: %w", err)
	}

	return nil
}

// rollbackConfiguration moves the saved file to its rejected copy and writes
// previous in its place. It returns the rejected copy's path.
func rollbackConfiguration(path string, previous []byte) (string, error) {
	rejected := path + rejectedSuffix
	current, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read the rejected configuration: %w", err)
	}
	if err := atomicfile.Write(rejected, current, filemode.PrivateFile); err != nil {
		return "", fmt.Errorf("keep the rejected configuration at %s: %w", rejected, err)
	}
	if err := atomicfile.Write(path, previous, filemode.PrivateFile); err != nil {
		return "", fmt.Errorf("restore the previous configuration to %s: %w", path, err)
	}

	return rejected, nil
}
