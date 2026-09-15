package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/telemetry"
	"github.com/spf13/cobra"
)

type Application struct {
	Version         string
	ConfigPath      string
	StateDir        string
	In              io.Reader
	Out             io.Writer
	Err             io.Writer
	monitor         *telemetry.Reporter
	networkIdentity func(context.Context) telemetry.NetworkIdentity
	reportConfig    *config.Config
	reportApplied   bool
}

func Execute(version string) error {
	application := &Application{
		Version:         version,
		ConfigPath:      env("UDM_IPTV_CONFIG", config.DefaultPath),
		StateDir:        env("UDM_IPTV_STATE_DIR", "/data/udm-iptv"),
		Out:             os.Stdout,
		In:              os.Stdin,
		Err:             os.Stderr,
		networkIdentity: telemetry.LookupNetwork,
	}
	root := application.root()
	application.instrumentCommands(root)
	return root.Execute()
}

func (application *Application) instrumentCommands(root *cobra.Command) {
	for _, command := range root.Commands() {
		application.instrumentCommands(command)
		switch command.Name() {
		case "configure", "install", "upgrade", "restart", "uninstall", "daemon", "dhcp-hook":
		default:
			continue
		}
		run := command.RunE
		if run == nil {
			continue
		}
		command.RunE = func(command *cobra.Command, args []string) error {
			if command.Name() == "configure" && command.Flags().Changed("telemetry") {
				if enabled, _ := command.Flags().GetBool("telemetry"); !enabled {
					return run(command, args)
				}
			}
			if command.Name() == "install" {
				if dryRun, _ := command.Flags().GetBool("dry-run"); dryRun {
					return run(command, args)
				}
			}
			// Reporting after the command also covers first installation and a
			// previously disabled user enabling reporting in the wizard.
			if command.Name() == "configure" || command.Name() == "install" {
				application.reportConfig, application.reportApplied = nil, false
				defer func() { application.reportSavedConfiguration(command) }()
			}
			value, err := config.Load(application.ConfigPath)
			if err != nil || !value.Telemetry.Enabled {
				return run(command, args)
			}
			reporter, err := telemetry.New(value.Telemetry, application.Version, application.ConfigPath, application.StateDir)
			if err != nil {
				return run(command, args)
			}
			defer reporter.Close()
			setTelemetryMetadata(reporter, value)
			application.monitor = reporter
			defer func() { application.monitor = nil }()
			operation := command.Name()
			if operation == "dhcp-hook" && len(args) == 1 {
				operation = "dhcp." + args[0]
			}
			return reporter.Run(command.Context(), operation, func(ctx context.Context) error {
				command.SetContext(ctx)
				return run(command, args)
			})
		}
	}
}

func (application *Application) root() *cobra.Command {
	command := &cobra.Command{
		Use:           "udm-iptv",
		Short:         "Routed IPTV for UniFi OS",
		Long:          "Configure, run, update, and diagnose routed IPTV on UniFi OS.",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	command.SetOut(application.Out)
	command.SetErr(application.Err)
	command.Version = application.Version
	command.SetVersionTemplate("udm-iptv {{.Version}}\n")
	command.PersistentFlags().StringVar(&application.ConfigPath, "config", application.ConfigPath, "configuration file")
	command.AddGroup(
		&cobra.Group{ID: "manage", Title: "Management Commands:"},
		&cobra.Group{ID: "observe", Title: "Observability Commands:"},
	)
	management := []*cobra.Command{
		application.configureCommand(), application.installCommand(), application.restartCommand(),
		application.uninstallCommand(), application.upgradeCommand(),
		application.previewCommand(),
	}
	for _, child := range management {
		child.GroupID = "manage"
		command.AddCommand(child)
	}
	observability := []*cobra.Command{application.statusCommand(), application.diagnoseCommand()}
	observability = append(observability, application.telemetryCommand())
	for _, child := range observability {
		child.GroupID = "observe"
		command.AddCommand(child)
	}
	versionCommand := &cobra.Command{
		Use: "version", Short: "Print the udm-iptv version", Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error { return writef(application.Out, "%s\n", application.Version) },
	}
	command.AddCommand(versionCommand, application.diagnoseWorkerCommand(), application.daemonCommand(), application.dhcpHookCommand())
	return command
}

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
			value := detectedDefaults()
			if current, err := config.Load(application.ConfigPath); err == nil {
				value = current
			} else if legacy, legacyErr := config.ImportLegacy("/etc/udm-iptv.conf"); legacyErr == nil {
				value = legacy
			}
			flags := command.Flags()
			if flags.Changed("profile") {
				selected, err := config.FromProfile(profile, value)
				if err != nil {
					return err
				}
				value = withDetectedInterfaces(selected)
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
				if err := application.configureForm(command.Context(), &value); err != nil {
					return err
				}
			}
			if err := config.Save(application.ConfigPath, value); err != nil {
				return err
			}
			application.reportConfig = &value
			if err := writef(application.Out, "Configuration saved to %s.\n", application.ConfigPath); err != nil {
				return err
			}
			if installed(application.StateDir) {
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

func completeValues(values ...string) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return values, cobra.ShellCompDirectiveNoFileComp
	}
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func installed(stateDir string) bool {
	info, err := os.Stat(filepath.Join(stateDir, "bin", "udm-iptv"))
	return err == nil && info.Mode().IsRegular()
}

func requireRoot() error {
	if os.Geteuid() != 0 {
		return errors.New("this command must be run as root")
	}
	if runtime.GOOS != "linux" {
		return errors.New("this command requires Linux")
	}
	return nil
}
