package app

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"charm.land/huh/v2"
	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/spf13/cobra"
)

type Application struct {
	Version    string
	ConfigPath string
	StateDir   string
	Out        io.Writer
	Err        io.Writer
}

func Execute(version string) error {
	application := &Application{
		Version:    version,
		ConfigPath: env("UDM_IPTV_CONFIG", config.DefaultPath),
		StateDir:   env("UDM_IPTV_STATE_DIR", "/data/udm-iptv"),
		Out:        os.Stdout,
		Err:        os.Stderr,
	}
	return application.root().Execute()
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
	}
	for _, child := range management {
		child.GroupID = "manage"
		command.AddCommand(child)
	}
	observability := []*cobra.Command{application.statusCommand(), application.diagnoseCommand()}
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
			if !nonInteractive {
				if err := application.configureForm(&value); err != nil {
					return err
				}
			}
			if err := config.Save(application.ConfigPath, value); err != nil {
				return err
			}
			if err := writef(application.Out, "Configuration saved to %s.\n", application.ConfigPath); err != nil {
				return err
			}
			if installed(application.StateDir) {
				return application.restart(command.Context(), true)
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

func (application *Application) configureForm(value *config.Config) error {
	profileID := value.Profile
	if profileID == "" || profileID == "legacy" {
		profileID = "custom"
	}
	profileOptions := make([]huh.Option[string], 0)
	for _, profile := range config.Profiles() {
		profileOptions = append(profileOptions, huh.NewOption(profile.Name, profile.ID))
	}
	if err := huh.NewSelect[string]().Title("Provider profile").Description("Start from known provider defaults, then review every value.").Options(profileOptions...).Value(&profileID).Run(); err != nil {
		return err
	}
	if profileID != value.Profile {
		selected, err := config.FromProfile(profileID, *value)
		if err != nil {
			return err
		}
		*value = withDetectedInterfaces(selected)
	}
	natDestinations := join(value.WAN.NATDestinations)
	proxySources := join(value.Proxy.SourceRanges)
	lanInterfaces := join(value.LAN.Interfaces)
	dhcpOptions := join(value.WAN.DHCPOptions)
	vlan := strconv.Itoa(value.WAN.VLAN)
	providerFields := []huh.Field{
		huh.NewNote().Title("udm-iptv configuration").Description("Configure the dedicated IPTV uplink and the LANs that receive multicast."),
	}
	if profile, found := config.ProfileByID(profileID); found && profile.Note != "" {
		providerFields = append(providerFields, huh.NewNote().Title("Provider note").Description(profile.Note))
	}
	providerFields = append(providerFields,
		huh.NewInput().Title("Physical WAN interface").Description("The port carrying the provider IPTV VLAN.").Value(&value.WAN.Interface),
		huh.NewInput().Title("IPTV VLAN ID").Description("Use 0 when IPTV is untagged.").Value(&vlan).Validate(func(value string) error {
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 0 || parsed > 4094 {
				return errors.New("enter a VLAN ID between 0 and 4094")
			}
			return nil
		}),
		huh.NewInput().Title("IPTV interface name").Value(&value.WAN.VLANInterface),
		huh.NewInput().Title("Custom VLAN MAC").Description("Usually empty. Set only when the provider binds IPTV to a specific MAC.").Value(&value.WAN.VLANMAC),
		huh.NewConfirm().Title("Obtain the IPTV address through DHCP?").Value(&value.WAN.DHCP),
		huh.NewInput().Title("DHCP client options").Description("Passed as separate arguments to udhcpc.").Value(&dhcpOptions),
		huh.NewInput().Title("Static IPTV address").Description("CIDR address used only when DHCP is disabled.").Value(&value.WAN.StaticAddress),
		huh.NewConfirm().Title("Allow a DHCP default-route fallback?").Description("Usually No. RFC3442 provider routes are safer; enabling this can create a second default route.").Value(&value.WAN.AllowDefaultRoute),
	)
	form := huh.NewForm(
		huh.NewGroup(providerFields...),
		huh.NewGroup(
			huh.NewInput().Title("NAT destination prefixes").Description("Space-separated unicast provider destinations. This does not control multicast source acceptance.").Value(&natDestinations),
			huh.NewInput().Title("Proxy source prefixes").Description("Space-separated multicast source allowlist for igmpproxy. Leave empty for improxy.").Value(&proxySources),
			huh.NewInput().Title("LAN interfaces").Description("Space-separated downstream interfaces, for example br0.").Value(&lanInterfaces),
		),
		huh.NewGroup(
			huh.NewSelect[string]().Title("Multicast proxy").Description("improxy is recommended on current UniFi OS.").Options(
				huh.NewOption("improxy (recommended)", "improxy"),
				huh.NewOption("igmpproxy", "igmpproxy"),
			).Value(&value.Proxy.Program),
			huh.NewSelect[int]().Title("IGMP version").Description("KPN and most current receivers use IGMPv3.").Options(
				huh.NewOption("IGMPv3 (recommended)", 3),
				huh.NewOption("IGMPv2", 2),
			).Value(&value.Proxy.IGMPVersion),
			huh.NewConfirm().Title("Enable quickleave?").Description("Disable this when multiple receivers may watch through the same downstream interface.").Value(&value.Proxy.QuickLeave),
			huh.NewConfirm().Title("Enable proxy debugging?").Description("Usually No. Enable temporarily only when collecting detailed troubleshooting logs.").Value(&value.Proxy.Debug),
		),
	)
	if err := form.Run(); err != nil {
		return err
	}
	value.WAN.VLAN, _ = strconv.Atoi(vlan)
	value.WAN.NATDestinations = fields(natDestinations)
	value.WAN.DHCPOptions = fields(dhcpOptions)
	value.Proxy.SourceRanges = fields(proxySources)
	value.LAN.Interfaces = fields(lanInterfaces)
	return value.Validate()
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

func join(values []string) string  { return strings.Join(values, " ") }
func fields(value string) []string { return strings.Fields(value) }
