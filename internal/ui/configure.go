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
	"unicode"

	"charm.land/huh/v2"

	"github.com/kjanat/udm-iptv/internal/config"
)

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
	return ConfigureSuggested(ctx, value, profiles, run, "", ports...)
}

// ConfigureSuggested highlights evidence without treating it as an applied choice.
// The caller supplies a profile ID only for a new, unconfigured installation.
func ConfigureSuggested(ctx context.Context, value *config.Config, profiles []config.Profile, run RunForm, suggestion string, ports ...Port) error {
	original := value
	draft := clone(*value)
	value = &draft
	profileID := value.Profile
	if profileID == "" || profileID == "legacy" {
		profileID = "custom"
	}
	profileOptions := make([]huh.Option[string], 0, len(profiles))
	for _, profile := range profiles {
		label := profile.Name
		if suggestion == profile.ID {
			profileID = suggestion
			label += " · PTR suggestion"
		}
		profileOptions = append(profileOptions, huh.NewOption(label, profile.ID))
	}
	selector := huh.NewSelect[string]().Key("profile").
		Title("Who is your TV provider?").
		Description("Loads matching defaults. You can change them next.").
		Options(profileOptions...).Height(min(12, len(profileOptions)+4)).Value(&profileID)
	fields := newFormValues(*value)
	estimate := configurationPages(value, ports, "", &fields)
	err := run(ctx, wizardForm(newPage(selector)).steps(0, wizardForm(estimate...).visiblePages()+1))
	if err != nil {
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
	fields = newFormValues(*value)
	groups, selectedPort, selectedLAN, lanExtra := configurationGroups(value, ports, note, &fields)
	settings := wizardForm(groups...).steps(1, 1)
	err = run(ctx, settings)
	if err != nil {
		return err
	}
	if *selectedPort != manualPort {
		value.WAN.Interface = *selectedPort
	}
	value.WAN.VLAN, _ = strconv.Atoi(fields.vlan)
	value.WAN.NATDestinations = splitList(fields.nat)
	value.WAN.DHCPOptions = strings.Fields(fields.dhcpOptions)
	value.Proxy.SourceRanges = splitList(fields.sources)
	value.LAN.Interfaces = resolveLAN(*selectedLAN, *lanExtra)
	err = value.Validate()
	if err != nil {
		return err
	}
	accepted := true
	err = run(ctx, wizardForm(newPage(
		huh.NewConfirm().Key("accept").Title("Use these settings?").
			Description(reviewSummary(*value)).
			Affirmative("Continue").Negative("Cancel").Value(&accepted),
	)).steps(1+settings.visiblePages(), 0))
	if err != nil {
		return err
	}
	if !accepted {
		return context.Canceled
	}
	*original = draft

	return nil
}

func configurationPages(value *config.Config, ports []Port, note string, fields *formValues) []page {
	groups, _, _, _ := configurationGroups(value, ports, note, fields) //nolint:dogsled //nolint:nolintlint

	return groups
}

func configurationGroups(value *config.Config, ports []Port, note string, fields *formValues) ([]page, *string, *[]string, *string) {
	groups, selectedPort := wanGroups(&value.WAN.Interface, ports)
	lanPages, selectedLAN, lanExtra := lanGroups(value.LAN.Interfaces, ports)
	connection := newPage(
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
	).title("IPTV connection")
	if note != "" {
		connection = connection.description(note)
	}
	groups = append(groups,
		connection,
		newPage(
			huh.NewInput().Key("vlan-interface").Title("VLAN interface name").
				Description("Virtual name, not a physical port.").
				Placeholder("iptv").Value(&value.WAN.VLANInterface).Validate(validateInterface),
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
		).title("VLAN interface").hide(func() bool { return fields.vlan == "0" }),
		newPage(
			huh.NewInput().Key("dhcp-options").Title("DHCP client options").
				Description("Arguments passed to udhcpc.").
				Value(&fields.dhcpOptions),
			huh.NewConfirm().Key("default-route").Title("Allow a DHCP default-route fallback?").
				Description("Usually No. Enabling can create a second default route.").
				Affirmative("Yes").Negative("No").Value(&value.WAN.AllowDefaultRoute),
		).title("DHCP options").hide(func() bool { return !value.WAN.DHCP }),
		newPage(
			huh.NewInput().Key("static-address").Title("Static IPTV address").
				Description("Address for the IPTV connection with its prefix length, for example 10.0.0.2/24.").
				Placeholder("10.0.0.2/24").Value(&value.WAN.StaticAddress).
				Validate(func(value string) error {
					if value == "" {
						return nil
					}
					prefix, err := netip.ParsePrefix(value)
					if err != nil || !prefix.Addr().Is4() {
						return errors.New("enter an IPv4 CIDR address")
					}

					return nil
				}),
		).title("Static address").hide(func() bool { return value.WAN.DHCP }),
	)
	groups = append(groups, lanPages...)
	groups = append(groups,
		newPage(
			huh.NewInput().Key("nat").Title("IPTV unicast destinations").
				Description("Networks your TVs talk to for the guide, video on demand and other services. Write each one as an address and prefix length such as 213.75.0.0/16, separated by spaces or commas.").
				Value(&fields.nat).Validate(validatePrefixes),
		).title("IPTV destinations"),
		newPage(
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
		).title("Multicast"),
		newPage(
			huh.NewInput().Key("proxy-sources").Title("Allowed multicast sources").
				Description("Networks igmpproxy accepts multicast video from. Write each one as an address and prefix length such as 213.75.0.0/16, separated by spaces or commas. 0.0.0.0/0 accepts every source.").
				Value(&fields.sources).
				Validate(func(value string) error {
					if len(splitList(value)) == 0 {
						return errors.New("igmpproxy needs at least one source prefix")
					}

					return validatePrefixes(value)
				}),
		).title("Multicast sources").hide(func() bool { return value.Proxy.Program != "igmpproxy" }),
		newPage(telemetryConsent(&value.Telemetry)),
	)

	return groups, selectedPort, selectedLAN, lanExtra
}

func reviewSummary(value config.Config) string {
	return fmt.Sprintf("%s · %s · VLAN %d · %s\nLAN: %s", value.Profile, value.WAN.Interface, value.WAN.VLAN, value.Proxy.Program, strings.Join(value.LAN.Interfaces, ", "))
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

func validateInterface(name string) error {
	value := config.Default()
	value.WAN.Interface = name

	return value.Validate()
}

func splitList(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || r == ',' || r == ';'
	})
}

func validatePrefixes(value string) error {
	for _, item := range splitList(value) {
		prefix, err := netip.ParsePrefix(item)
		if err != nil || !prefix.Addr().Is4() {
			return errors.New("use IPv4 prefixes, for example 213.75.0.0/16")
		}
	}

	return nil
}
