package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/device"
	"github.com/kjanat/udm-iptv/internal/installer"
)

// legacyCandidates lists the legacy configurations an install may import, in
// the order they are trusted. udm-iptv persist wrote udm-iptv.conf into the
// state directory, and cleanup deletes it once an install succeeds.
func legacyCandidates(stateDir string) []string {
	return []string{
		"/etc/udm-iptv.conf",
		filepath.Join(stateDir, "legacy.conf"),
		filepath.Join(stateDir, "udm-iptv.conf"),
	}
}

// loadOrImportConfig loads the saved configuration, falls back to importing a
// legacy config file, and otherwise reports fresh so the caller can suggest a
// provider profile.
func loadOrImportConfig(path, stateDir string) (config.Config, bool, error) {
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
		return legacy, false, nil
	}

	return device.Defaults(), true, nil
}

// startingPoint keeps an unconfigured console off the KPN defaults when the
// user asked for the custom profile. Applying "custom" relabels the draft it
// is given, which is the right answer for a saved configuration and the wrong
// one for a fresh console, where that draft is the KPN profile.
func startingPoint(command *cobra.Command, value config.Config, fresh bool) config.Config {
	if !fresh || !command.Flags().Changed("profile") || command.Flags().Lookup("profile").Value.String() != "custom" {
		return value
	}

	return device.WithInterfaces(config.Default())
}

// override folds one flag into the draft configuration.
type override struct {
	name string
	set  func()
}

// configureFlags holds what the configure flags write into. Every flag but
// --profile and --telemetry maps to exactly one field, so they share one
// table instead of a branch each.
type configureFlags struct {
	nonInteractive                       bool
	profile, wanInterface, iptvInterface string
	vlanMAC, staticAddress, proxy        string
	vlan, igmpVersion                    int
	dhcpOptions, natDestinations         []string
	proxySources, lanInterfaces          []string
	dhcpRoutes                           string
	dhcp                                 bool
	quickLeave, debug                    bool
	telemetry                            config.Telemetry
}

func (f *configureFlags) overrides(value *config.Config) []override {
	return []override{
		{"wan-interface", func() { value.WAN.Interface = f.wanInterface }},
		{"wan-vlan", func() { value.WAN.VLAN = f.vlan }},
		{"iptv-interface", func() { value.WAN.VLANInterface = f.iptvInterface }},
		{"vlan-mac", func() { value.WAN.VLANMAC = f.vlanMAC }},
		{"dhcp", func() { value.WAN.DHCP = f.dhcp }},
		{"dhcp-option", func() { value.WAN.DHCPOptions = f.dhcpOptions }},
		{"static-address", func() { value.WAN.StaticAddress = f.staticAddress }},
		{"dhcp-routes", func() { value.WAN.DHCPRoutes = config.RoutePolicy(f.dhcpRoutes) }},
		{"nat-destination", func() { value.WAN.NATDestinations = f.natDestinations }},
		{"proxy-source", func() { value.Proxy.SourceRanges = f.proxySources }},
		{"lan-interface", func() { value.LAN.Interfaces = f.lanInterfaces }},
		{"proxy", func() { value.Proxy.Program = f.proxy }},
		{"igmp-version", func() { value.Proxy.IGMPVersion = f.igmpVersion }},
		{"quickleave", func() { value.Proxy.QuickLeave = f.quickLeave }},
		{"debug", func() { value.Proxy.Debug = f.debug }},
		{"telemetry-errors", func() { value.Telemetry.Errors = f.telemetry.Errors }},
		{"telemetry-logs", func() { value.Telemetry.Logs = f.telemetry.Logs }},
		{"telemetry-metrics", func() { value.Telemetry.Metrics = f.telemetry.Metrics }},
		{"telemetry-tracing", func() { value.Telemetry.Tracing = f.telemetry.Tracing }},
		{"telemetry-presets", func() { value.Telemetry.Presets = f.telemetry.Presets }},
		{"telemetry-network-identity", func() { value.Telemetry.NetworkIdentity = f.telemetry.NetworkIdentity }},
		{"telemetry-trace-rate", func() { value.Telemetry.TraceRate = f.telemetry.TraceRate }},
	}
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
		*value = device.WithInterfaces(selected)
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
	if !command.Flags().Changed("telemetry") {
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
	flags.BoolVar(&f.nonInteractive, "non-interactive", false, "write values supplied by flags without opening the form")
	flags.StringVar(&f.profile, "profile", "", "provider profile ID")
	flags.StringVar(&f.wanInterface, "wan-interface", "", "physical WAN interface")
	flags.IntVar(&f.vlan, "wan-vlan", 0, "IPTV VLAN ID; zero disables VLAN creation")
	flags.StringVar(&f.iptvInterface, "iptv-interface", "", "name of the IPTV VLAN interface")
	flags.StringVar(&f.vlanMAC, "vlan-mac", "", "custom MAC address for the IPTV VLAN")
	flags.BoolVar(&f.dhcp, "dhcp", false, "obtain the IPTV address through DHCP")
	flags.StringSliceVar(&f.dhcpOptions, "dhcp-option", nil, "argument passed to udhcpc; repeatable")
	flags.StringVar(&f.staticAddress, "static-address", "", "static IPTV address in CIDR notation")
	flags.StringVar(&f.dhcpRoutes, "dhcp-routes", string(config.RoutesNoDefault), "routes to accept from a lease: no-default, allow-default or none")
	flags.StringSliceVar(&f.natDestinations, "nat-destination", nil, "destination prefix to masquerade; repeatable")
	flags.StringSliceVar(&f.proxySources, "proxy-source", nil, "allowed multicast source prefix; repeatable")
	flags.StringSliceVar(&f.lanInterfaces, "lan-interface", nil, "downstream LAN interface; repeatable")
	flags.StringVar(&f.proxy, "proxy", "", "multicast proxy: improxy or igmpproxy")
	flags.IntVar(&f.igmpVersion, "igmp-version", 0, "IGMP version: 2 or 3")
	flags.BoolVar(&f.quickLeave, "quickleave", false, "enable quickleave")
	flags.BoolVar(&f.debug, "debug", false, "enable verbose proxy logging")
	flags.BoolVar(&f.telemetry.Enabled, "telemetry", true, "send diagnostic data")
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

func (application *Application) configureCommand() *cobra.Command {
	flags := &configureFlags{telemetry: config.Default().Telemetry}
	command := &cobra.Command{
		Use:     commandConfigure,
		Aliases: []string{"reconfigure"},
		Short:   "Configure IPTV",
		Args:    cobra.NoArgs,
		RunE: application.reportingSaved(commandConfigure, reportingTurnedOff, func(command *cobra.Command, _ []string) error {
			application.providerSuggestion = ""
			value, fresh, err := loadOrImportConfig(application.ConfigPath, application.StateDir)
			if err != nil {
				return err
			}
			value = startingPoint(command, value, fresh)
			if err := flags.apply(command, &value); err != nil {
				return err
			}
			if !flags.nonInteractive {
				if err := application.askForConfiguration(command, &value, fresh); err != nil {
					return err
				}
			}

			return application.saveConfiguration(command, value)
		}),
	}
	flags.bind(command)

	return command
}

// askForConfiguration opens the wizard. A fresh configuration asks the
// reporting question first and looks the provider up from the answer, so an
// explicitly named profile needs no lookup at all.
func (application *Application) askForConfiguration(command *cobra.Command, value *config.Config, fresh bool) error {
	if fresh && !command.Flags().Changed("profile") {
		return application.configureFreshForm(command.Context(), value)
	}

	return application.configureForm(command.Context(), value)
}

// saveConfiguration validates and persists the configuration, then restarts an
// installed service, recording both for the telemetry report.
func (application *Application) saveConfiguration(command *cobra.Command, value config.Config) error {
	if err := value.Validate(); err != nil {
		return fmt.Errorf("check the configuration: %w", err)
	}
	installed := installer.Installed(application.StateDir)
	if installed {
		if err := installer.PreserveProxy(application.StateDir, value.Proxy.Program); err != nil {
			return fmt.Errorf("snapshot the proxy into %s: %w", application.StateDir, err)
		}
	}
	if err := config.Save(application.ConfigPath, value); err != nil {
		return fmt.Errorf("save configuration to %s: %w", application.ConfigPath, err)
	}
	application.reportConfig = &value
	if err := writef(application.Out, "Configuration saved to %s.\n", application.ConfigPath); err != nil {
		return err
	}
	if !installed {
		return nil
	}
	err := application.restart(command.Context(), true)
	application.reportApplied = err == nil

	return err
}
