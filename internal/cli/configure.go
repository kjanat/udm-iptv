package cli

import (
	"errors"
	"os"

	"github.com/kjanat/udm-iptv/internal/device"
	"github.com/kjanat/udm-iptv/internal/installer"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/spf13/cobra"
)

func (application *Application) configureCommand() *cobra.Command {
	var nonInteractive bool
	var profile, wanInterface, iptvInterface, vlanMAC, staticAddress, proxy string
	var vlan, igmpVersion int
	var dhcpOptions, natDestinations, proxySources, lanInterfaces []string
	var dhcp, allowDefaultRoute, quickLeave, debug bool
	telemetryOptions := config.Default().Telemetry
	command := &cobra.Command{
		Use:     "configure",
		Aliases: []string{"reconfigure"},
		Short:   "Configure IPTV",
		Args:    cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			value := device.Defaults()
			fresh := false
			application.providerSuggestion = ""
			if current, err := config.Load(application.ConfigPath); err == nil {
				value = current
			} else {
				if !errors.Is(err, os.ErrNotExist) {
					return err
				}
				legacy, found, legacyErr := config.ImportFirstLegacy([]string{"/etc/udm-iptv.conf"})
				if legacyErr != nil {
					return legacyErr
				}
				if found {
					value = legacy
				} else {
					fresh = true
				}
			}
			flags := command.Flags()
			if flags.Changed("profile") {
				selected, err := config.FromProfile(profile, value)
				if err != nil {
					return err
				}
				value = device.WithInterfaces(selected)
			}
			if flags.Changed("wan-interface") {
				value.WAN.Interface = wanInterface
			}
			if flags.Changed("wan-vlan") {
				value.WAN.VLAN = vlan
			}
			if flags.Changed("iptv-interface") {
				value.WAN.VLANInterface = iptvInterface
			}
			if flags.Changed("vlan-mac") {
				value.WAN.VLANMAC = vlanMAC
			}
			if flags.Changed("dhcp") {
				value.WAN.DHCP = dhcp
			}
			if flags.Changed("dhcp-option") {
				value.WAN.DHCPOptions = dhcpOptions
			}
			if flags.Changed("static-address") {
				value.WAN.StaticAddress = staticAddress
			}
			if flags.Changed("allow-default-route") {
				value.WAN.AllowDefaultRoute = allowDefaultRoute
			}
			if flags.Changed("nat-destination") {
				value.WAN.NATDestinations = natDestinations
			}
			if flags.Changed("proxy-source") {
				value.Proxy.SourceRanges = proxySources
			}
			if flags.Changed("lan-interface") {
				value.LAN.Interfaces = lanInterfaces
			}
			if flags.Changed("proxy") {
				value.Proxy.Program = proxy
			}
			if flags.Changed("igmp-version") {
				value.Proxy.IGMPVersion = igmpVersion
			}
			if flags.Changed("quickleave") {
				value.Proxy.QuickLeave = quickLeave
			}
			if flags.Changed("debug") {
				value.Proxy.Debug = debug
			}
			if flags.Changed("telemetry") {
				if telemetryOptions.Enabled && !value.Telemetry.Enabled && !value.Telemetry.Errors && !value.Telemetry.Logs && !value.Telemetry.Metrics && !value.Telemetry.Tracing {
					value.Telemetry = config.Default().Telemetry
				}
				value.Telemetry.Enabled = telemetryOptions.Enabled
			}
			for _, option := range []struct {
				name string
				dst  *bool
				src  bool
			}{
				{"telemetry-errors", &value.Telemetry.Errors, telemetryOptions.Errors},
				{"telemetry-logs", &value.Telemetry.Logs, telemetryOptions.Logs},
				{"telemetry-metrics", &value.Telemetry.Metrics, telemetryOptions.Metrics},
				{"telemetry-tracing", &value.Telemetry.Tracing, telemetryOptions.Tracing},
				{"telemetry-presets", &value.Telemetry.Presets, telemetryOptions.Presets},
				{"telemetry-network-identity", &value.Telemetry.NetworkIdentity, telemetryOptions.NetworkIdentity},
			} {
				if flags.Changed(option.name) {
					*option.dst = option.src
				}
			}
			if flags.Changed("telemetry-trace-rate") {
				value.Telemetry.TraceRate = telemetryOptions.TraceRate
			}
			if !nonInteractive {
				if fresh && !flags.Changed("profile") {
					err := application.suggestProvider(command.Context(), value)
					if err != nil {
						return err
					}
				}
				err := application.configureForm(command.Context(), &value)
				if err != nil {
					return err
				}
			}
			err := value.Validate()
			if err != nil {
				return err
			}
			if installer.Installed(application.StateDir) {
				err := installer.PreserveProxy(application.StateDir, value.Proxy.Program)
				if err != nil {
					return err
				}
			}
			err = config.Save(application.ConfigPath, value)
			if err != nil {
				return err
			}
			application.reportConfig = &value
			err = writef(application.Out, "Configuration saved to %s.\n", application.ConfigPath)
			if err != nil {
				return err
			}
			if installer.Installed(application.StateDir) {
				err := application.restart(command.Context(), true)
				application.reportApplied = err == nil

				return err
			}

			return nil
		},
	}
	flags := command.Flags()
	flags.BoolVar(&nonInteractive, "non-interactive", false, "write values supplied by flags without opening the form")
	flags.StringVar(&profile, "profile", "", "provider profile ID")
	flags.StringVar(&wanInterface, "wan-interface", "", "physical WAN interface")
	flags.IntVar(&vlan, "wan-vlan", 0, "IPTV VLAN ID; zero disables VLAN creation")
	flags.StringVar(&iptvInterface, "iptv-interface", "", "name of the IPTV VLAN interface")
	flags.StringVar(&vlanMAC, "vlan-mac", "", "custom MAC address for the IPTV VLAN")
	flags.BoolVar(&dhcp, "dhcp", false, "obtain the IPTV address through DHCP")
	flags.StringSliceVar(&dhcpOptions, "dhcp-option", nil, "argument passed to udhcpc; repeatable")
	flags.StringVar(&staticAddress, "static-address", "", "static IPTV address in CIDR notation")
	flags.BoolVar(&allowDefaultRoute, "allow-default-route", false, "allow DHCP router fallback without RFC3442 routes")
	flags.StringSliceVar(&natDestinations, "nat-destination", nil, "destination prefix to masquerade; repeatable")
	flags.StringSliceVar(&proxySources, "proxy-source", nil, "allowed multicast source prefix; repeatable")
	flags.StringSliceVar(&lanInterfaces, "lan-interface", nil, "downstream LAN interface; repeatable")
	flags.StringVar(&proxy, "proxy", "", "multicast proxy: improxy or igmpproxy")
	flags.IntVar(&igmpVersion, "igmp-version", 0, "IGMP version: 2 or 3")
	flags.BoolVar(&quickLeave, "quickleave", false, "enable quickleave")
	flags.BoolVar(&debug, "debug", false, "enable verbose proxy logging")
	flags.BoolVar(&telemetryOptions.Enabled, "telemetry", true, "send diagnostic data")
	flags.BoolVar(&telemetryOptions.Errors, "telemetry-errors", true, "report software failures when telemetry is enabled")
	flags.BoolVar(&telemetryOptions.Logs, "telemetry-logs", true, "send structured lifecycle logs; never raw proxy logs")
	flags.BoolVar(&telemetryOptions.Metrics, "telemetry-metrics", true, "send bounded operational counters")
	flags.BoolVar(&telemetryOptions.Tracing, "telemetry-tracing", true, "send sampled operation timings")
	flags.BoolVar(&telemetryOptions.Presets, "telemetry-presets", true, "share selected settings, changes and a random installation ID")
	flags.BoolVar(&telemetryOptions.NetworkIdentity, "telemetry-network-identity", true, "include public IP and reverse-DNS hostname in research")
	flags.Float64Var(&telemetryOptions.TraceRate, "telemetry-trace-rate", 0.1, "fraction of operations traced, from 0 to 1")
	for _, name := range []string{
		"telemetry-errors", "telemetry-logs", "telemetry-metrics", "telemetry-tracing",
		"telemetry-presets", "telemetry-network-identity", "telemetry-trace-rate",
	} {
		_ = flags.MarkHidden(name)
	}
	_ = command.RegisterFlagCompletionFunc("profile", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		profiles := config.Profiles()
		values := make([]string, 0, len(profiles))
		for _, value := range profiles {
			values = append(values, value.ID+"\t"+value.Name)
		}

		return values, cobra.ShellCompDirectiveNoFileComp
	})
	_ = command.RegisterFlagCompletionFunc("proxy", completeValues("improxy\trecommended on current UniFi OS", "igmpproxy\tlegacy proxy with a source allowlist"))
	_ = command.RegisterFlagCompletionFunc("igmp-version", completeValues("3\trecommended for current receivers", "2\tlegacy receivers"))

	return command
}
