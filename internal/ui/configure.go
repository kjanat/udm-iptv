// Package ui provides terminal forms with explicit inputs and no host operations.
package ui

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"github.com/kjanat/udm-iptv/internal/config"
)

// RunForm supplies terminal input, output and execution to the wizard.
type RunForm func(context.Context, *huh.Form) error

type formValues struct {
	vlan, dhcpOptions, nat, sources string
}

func newFormValues(value config.Config) formValues {
	return formValues{
		vlan:        strconv.Itoa(value.WAN.VLAN),
		dhcpOptions: strings.Join(value.WAN.DHCPOptions, " "),
		nat:         strings.Join(value.WAN.NATDestinations, " "),
		sources:     strings.Join(value.Proxy.SourceRanges, " "),
	}
}

// Configure edits a private draft and only updates value after successful validation.
// Profiles must already contain any detected interface defaults.
func Configure(ctx context.Context, value *config.Config, profiles []config.Profile, run RunForm, ports ...Port) error {
	original := value
	draft := clone(*value)
	value = &draft
	profileID := value.Profile
	if profileID == "" || profileID == "legacy" {
		profileID = "custom"
	}
	profileOptions := make([]huh.Option[string], 0)
	for _, profile := range profiles {
		profileOptions = append(profileOptions, huh.NewOption(profile.Name, profile.ID))
	}
	selector := huh.NewSelect[string]().Key("profile").
		Title("Who is your TV provider?").
		Description("Loads matching defaults. You can change them next.").
		Options(profileOptions...).Height(12).Value(&profileID)
	if err := run(ctx, wizardForm(huh.NewGroup(selector))); err != nil {
		return err
	}
	if profileID != value.Profile {
		found := false
		for _, profile := range profiles {
			if profile.ID != profileID {
				continue
			}
			found = true
			if profileID != "custom" {
				selected := clone(profile.Config)
				selected.Telemetry = value.Telemetry
				*value = selected
			}
			value.Profile = profileID
			break
		}
		if !found {
			return fmt.Errorf("unknown provider profile %q", profileID)
		}
	}

	note := ""
	for _, profile := range profiles {
		if profile.ID == profileID {
			note = profile.Note
			break
		}
	}
	fields := newFormValues(*value)
	groups, selectedPort, selectedLAN, lanExtra := configurationGroups(value, ports, note, &fields)
	if err := run(ctx, wizardForm(groups...)); err != nil {
		return err
	}
	if *selectedPort != manualPort {
		value.WAN.Interface = *selectedPort
	}
	value.WAN.VLAN, _ = strconv.Atoi(fields.vlan)
	value.WAN.NATDestinations = strings.Fields(fields.nat)
	value.WAN.DHCPOptions = strings.Fields(fields.dhcpOptions)
	value.Proxy.SourceRanges = strings.Fields(fields.sources)
	value.LAN.Interfaces = resolveLAN(*selectedLAN, *lanExtra)
	if err := value.Validate(); err != nil {
		return err
	}
	*original = draft
	return nil
}

func configurationGroups(value *config.Config, ports []Port, note string, fields *formValues) ([]*huh.Group, *string, *[]string, *string) {
	groups, selectedPort := wanGroups(&value.WAN.Interface, ports)
	lanPages, selectedLAN, lanExtra := lanGroups(value.LAN.Interfaces, ports)
	connection := huh.NewGroup(
		huh.NewInput().Key("vlan").Title("IPTV VLAN ID").
			Description("Use 0 when IPTV is untagged.").
			Placeholder("4").Value(&fields.vlan).
			Validate(func(value string) error {
				parsed, err := strconv.Atoi(value)
				if err != nil || parsed < 0 || parsed > 4094 {
					return errors.New("enter a VLAN ID between 0 and 4094")
				}
				return nil
			}),
		huh.NewConfirm().Key("dhcp").Title("Use DHCP for the IPTV address?").
			Description("Most providers assign this automatically.").
			Affirmative("Yes").Negative("No").Value(&value.WAN.DHCP),
	).Title("IPTV connection")
	if note != "" {
		connection = connection.Description(note)
	}
	groups = append(groups,
		connection,
		huh.NewGroup(
			huh.NewInput().Key("vlan-interface").Title("VLAN interface name").
				Description("Virtual name, not a physical port.").
				Placeholder("iptv").Value(&value.WAN.VLANInterface),
			huh.NewInput().Key("vlan-mac").Title("Custom MAC address").
				Description("Leave empty unless your provider requires it.").
				Value(&value.WAN.VLANMAC).
				Validate(func(value string) error {
					if value == "" {
						return nil
					}
					if _, err := net.ParseMAC(value); err != nil {
						return errors.New("enter a valid MAC address")
					}
					return nil
				}),
		).Title("VLAN interface").WithHideFunc(func() bool { return fields.vlan == "0" }),
		huh.NewGroup(
			huh.NewInput().Key("dhcp-options").Title("DHCP client options").
				Description("Arguments passed to udhcpc.").
				Value(&fields.dhcpOptions),
			huh.NewConfirm().Key("default-route").Title("Allow a DHCP default-route fallback?").
				Description("Usually No. Enabling can create a second default route.").
				Affirmative("Yes").Negative("No").Value(&value.WAN.AllowDefaultRoute),
		).Title("DHCP options").WithHideFunc(func() bool { return !value.WAN.DHCP }),
		huh.NewGroup(
			huh.NewInput().Key("static-address").Title("Static IPTV address").
				Description("IPv4 CIDR, for example 10.0.0.2/24.").
				Placeholder("10.0.0.2/24").Value(&value.WAN.StaticAddress).
				Validate(func(value string) error {
					prefix, err := netip.ParsePrefix(value)
					if err != nil || !prefix.Addr().Is4() {
						return errors.New("enter an IPv4 CIDR address")
					}
					return nil
				}),
		).Title("Static address").WithHideFunc(func() bool { return value.WAN.DHCP }),
	)
	groups = append(groups, lanPages...)
	groups = append(groups,
		huh.NewGroup(
			huh.NewInput().Key("nat").Title("IPTV unicast destinations").
				Description("Unicast destinations your TVs need to reach.").
				Value(&fields.nat),
		).Title("IPTV destinations"),
		huh.NewGroup(
			huh.NewSelect[string]().Key("proxy").Title("Multicast proxy").
				Description("Recommended on current UniFi OS.").
				Options(
					huh.NewOption("improxy (recommended)", "improxy"),
					huh.NewOption("igmpproxy", "igmpproxy"),
				).Value(&value.Proxy.Program),
			huh.NewSelect[int]().Key("igmp").Title("IGMP version").
				Description("IGMPv3 works for most current receivers.").
				Options(
					huh.NewOption("IGMPv3 (recommended)", 3),
					huh.NewOption("IGMPv2", 2),
				).Value(&value.Proxy.IGMPVersion),
			huh.NewConfirm().Key("quickleave").Title("Enable quickleave?").
				Description("Off when several TVs share one interface.").
				Affirmative("Yes").Negative("No").Value(&value.Proxy.QuickLeave),
			huh.NewConfirm().Key("debug").Title("Enable proxy debug logs?").
				Description("Temporary. Leave off during normal use.").
				Affirmative("Yes").Negative("No").Value(&value.Proxy.Debug),
		).Title("Multicast"),
		huh.NewGroup(
			huh.NewInput().Key("proxy-sources").Title("Allowed multicast sources").
				Description("Allowlist for igmpproxy multicast sources.").
				Value(&fields.sources).
				Validate(func(value string) error {
					if len(strings.Fields(value)) == 0 {
						return errors.New("igmpproxy needs at least one source prefix")
					}
					return nil
				}),
		).Title("Multicast sources").WithHideFunc(func() bool { return value.Proxy.Program != "igmpproxy" }),
		huh.NewGroup(telemetryConsent(&value.Telemetry)),
	)
	return groups, selectedPort, selectedLAN, lanExtra
}

func wizardForm(groups ...*huh.Group) *huh.Form {
	form := huh.NewForm(groups...).WithWidth(88)
	return form.WithProgramOptions(tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
		if size, ok := msg.(tea.WindowSizeMsg); ok && size.Width > 0 {
			form.WithWidth(min(88, size.Width))
		}
		return msg
	}))
}

func telemetryConsent(settings *config.Telemetry) huh.Field {
	return huh.NewConfirm().Key("telemetry").
		Title("Help improve udm-iptv?").
		Description("Used to improve reliability and defaults.").
		Affirmative("Yes").Negative("No").Value(&settings.Enabled)
}

func clone(value config.Config) config.Config {
	value.WAN.DHCPOptions = slices.Clone(value.WAN.DHCPOptions)
	value.WAN.NATDestinations = slices.Clone(value.WAN.NATDestinations)
	value.WAN.StaticRoutes = slices.Clone(value.WAN.StaticRoutes)
	value.Proxy.SourceRanges = slices.Clone(value.Proxy.SourceRanges)
	value.LAN.Interfaces = slices.Clone(value.LAN.Interfaces)
	return value
}
