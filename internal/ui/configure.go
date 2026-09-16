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

const (
	// selectChrome is the extra rows a select adds around its visible options.
	selectChrome = 4
	// igmpVersion2 is IGMPv2, the compatibility fallback next to the recommended v3.
	igmpVersion2 = 2
)

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
func Configure(ctx context.Context, value *config.Config, catalog config.Catalog, run RunForm, ports ...Port) error {
	return ConfigureSuggested(ctx, value, catalog, run, "", ports...)
}

// selection is the country → provider path the wizard walks. choice is
// "provider/profile": a provider with several networks lists one row per
// network. An empty country means custom settings without a profile;
// allChoices as country widens the provider list to the whole catalog.
type selection struct {
	country, choice string
}

func choiceOf(provider, profile string) string {
	return provider + "/" + profile
}

// profile returns the profile ID inside the choice, or custom.
func (chosen selection) profile() string {
	if chosen.country == "" {
		return "custom"
	}
	_, profile, found := strings.Cut(chosen.choice, "/")
	if !found {
		return "custom"
	}

	return profile
}

func startingSelection(catalog config.Catalog, current, suggestion string) selection {
	if provider, found := catalog.ProviderByID(suggestion); found {
		return selection{country: provider.Countries[0], choice: choiceOf(provider.ID, provider.Profiles[0])}
	}
	if country, provider, found := catalog.Locate(current); found {
		return selection{country: country, choice: choiceOf(provider, current)}
	}

	return selection{}
}

func providersFor(catalog config.Catalog, country string) []config.Provider {
	if country == allChoices {
		return catalog.Providers
	}

	return catalog.ProvidersIn(country)
}

func countryName(catalog config.Catalog, code string) string {
	for _, country := range catalog.Countries {
		if country.Code == code {
			return country.Name
		}
	}

	return code
}

func countryOptions(catalog config.Catalog) []huh.Option[string] {
	options := make([]huh.Option[string], 0, len(catalog.Countries))
	for _, country := range catalog.Countries {
		label := country.Name
		if country.LocalName != country.Name {
			label = annotate(label, country.LocalName)
		}
		options = append(options, huh.NewOption(label, country.Code))
	}

	return options
}

// annotate appends secondary details in parentheses: "KPN (Netherlands)".
func annotate(label string, notes ...string) string {
	notes = slices.DeleteFunc(slices.Clone(notes), func(note string) bool { return note == "" })
	if len(notes) == 0 {
		return label
	}

	return label + " (" + strings.Join(notes, ", ") + ")"
}

// providerOptions lists one row per provider, or one per network for a
// provider that runs several: "Vivo (São Paulo network)".
func providerOptions(catalog config.Catalog, country, suggestion string) []huh.Option[string] {
	providers := providersFor(catalog, country)
	options := make([]huh.Option[string], 0, len(providers))
	for _, provider := range providers {
		for _, profile := range catalog.ProfilesOf(provider.ID) {
			var notes []string
			if len(provider.Profiles) > 1 {
				notes = append(notes, profile.Name)
			}
			if country == allChoices {
				notes = append(notes, countryName(catalog, provider.Countries[0]))
			}
			if provider.ID == suggestion {
				notes = append(notes, "PTR suggestion")
			}
			options = append(options, huh.NewOption(annotate(provider.Name, notes...), choiceOf(provider.ID, profile.ID)))
		}
	}

	return options
}

func ensureChoice(chosen *string, ids []string) {
	if !slices.Contains(ids, *chosen) {
		*chosen = ids[0]
	}
}

// profileSteps is the country → provider chain.
func profileSteps(catalog config.Catalog, suggestion string) []cascadeStep {
	return []cascadeStep{
		{
			key: "country", title: "Where do you live?", plural: "countries",
			description: "Type to search. Pick your country.",
			options:     func(string) []huh.Option[string] { return countryOptions(catalog) },
			extra:       []huh.Option[string]{huh.NewOption("Manual", "")},
			ends:        func(answer string) bool { return answer == "" },
		},
		{
			key: "provider", title: "Who is your TV provider?", plural: "providers",
			description: "Type to search. TV Company.",
			options:     func(country string) []huh.Option[string] { return providerOptions(catalog, country, suggestion) },
		},
	}
}

// chooseProfile walks the chain from step start and returns the number of
// questions asked. remaining counts the questions that follow the chain.
func chooseProfile(ctx context.Context, catalog config.Catalog, run RunForm, chosen *selection, suggestion string, remaining, start int) (int, error) {
	answers := []*string{&chosen.country, &chosen.choice}

	return cascade(ctx, run, profileSteps(catalog, suggestion), answers, remaining, start)
}

// applyProfile replaces the draft with the chosen profile's settings, keeping
// the telemetry choice.
func applyProfile(catalog config.Catalog, value *config.Config, profileID string) error {
	if profileID == value.Profile {
		return nil
	}
	if profileID == "custom" {
		value.Profile = "custom"

		return nil
	}
	profile, found := catalog.Profile(profileID)
	if !found {
		return fmt.Errorf("unknown provider profile %q", profileID)
	}
	selected := clone(profile.Config)
	selected.Telemetry = value.Telemetry
	*value = selected

	return nil
}

// settingsForm builds the settings pages for the draft and returns the
// wizard plus the function that copies the answers back into the draft.
func settingsForm(catalog config.Catalog, value *config.Config, ports []Port, asked int) (*Wizard, func() error) {
	note := ""
	if profile, found := catalog.Profile(value.Profile); found {
		note = profile.Note
	}
	fields := newFormValues(*value)
	groups, selectedPort, selectedLAN := configurationGroups(value, ports, note, &fields)
	apply := func() error {
		if *selectedPort != manualPort {
			value.WAN.Interface = *selectedPort
		}
		value.WAN.VLAN, _ = strconv.Atoi(fields.vlan)
		value.WAN.NATDestinations = splitList(fields.nat)
		value.WAN.DHCPOptions = strings.Fields(fields.dhcpOptions)
		value.Proxy.SourceRanges = splitList(fields.sources)
		value.LAN.Interfaces = resolveLAN(*selectedLAN)

		return value.Validate()
	}

	return wizardForm(groups...).steps(asked, 1), apply
}

func reviewForm(value config.Config, before int, accepted *bool) *Wizard {
	return wizardForm(newPage(
		huh.NewConfirm().Key("accept").Title("Use these settings?").
			Description(reviewSummary(value)).
			Affirmative("Continue").Negative("Cancel").Value(accepted),
	)).steps(before, 0)
}

// wizardStage is where ConfigureSuggested is in its chain → settings →
// review sequence. Stepping back moves one stage earlier.
type wizardStage int

const (
	stageChain wizardStage = iota
	stageSettings
	stageReview
)

// ConfigureSuggested highlights evidence without treating it as an applied choice.
// The caller supplies a provider ID only for a new, unconfigured installation.
func ConfigureSuggested(ctx context.Context, value *config.Config, catalog config.Catalog, run RunForm, suggestion string, ports ...Port) error {
	original := value
	draft := clone(*value)
	value = &draft
	chosen := startingSelection(catalog, value.Profile, suggestion)
	fields := newFormValues(*value)
	estimate := configurationPages(value, ports, "", &fields)
	remaining := wizardForm(estimate...).visibleFields() + 1
	stage, start, asked := stageChain, 0, 0
	var settings *Wizard
	var apply func() error
	accepted := true
	for {
		switch stage {
		case stageChain:
			count, err := chooseProfile(ctx, catalog, run, &chosen, suggestion, remaining, start)
			if err != nil {
				return err
			}
			asked = count
			if err := applyProfile(catalog, value, chosen.profile()); err != nil {
				return err
			}
			settings, apply = settingsForm(catalog, value, ports, asked)
			stage = stageSettings
		case stageSettings:
			err := run(ctx, settings)
			if errors.Is(err, ErrBack) {
				stage, start = stageChain, max(asked-1, 0)

				continue
			}
			if err != nil {
				return err
			}
			if err := apply(); err != nil {
				return err
			}
			stage = stageReview
		case stageReview:
			err := run(ctx, reviewForm(*value, asked+settings.visiblePages(), &accepted))
			if errors.Is(err, ErrBack) {
				settings, apply = settingsForm(catalog, value, ports, asked)
				stage = stageSettings

				continue
			}
			if err != nil {
				return err
			}
			if !accepted {
				return context.Canceled
			}
			*original = draft

			return nil
		}
	}
}

func configurationPages(value *config.Config, ports []Port, note string, fields *formValues) []page {
	groups, _, _ := configurationGroups(value, ports, note, fields)

	return groups
}

func configurationGroups(value *config.Config, ports []Port, note string, fields *formValues) ([]page, *string, *[]string) {
	groups, selectedPort := wanGroups(&value.WAN.Interface, ports)
	lanPages, selectedLAN := lanGroups(value.LAN.Interfaces, ports)
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
					huh.NewOption("IGMPv3 (recommended)", config.DefaultIGMPVersion),
					huh.NewOption("IGMPv2", igmpVersion2),
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

	return groups, selectedPort, selectedLAN
}

func reviewSummary(value config.Config) string {
	return fmt.Sprintf("%s, %s, VLAN %d, %s\nLAN: %s", value.Profile, value.WAN.Interface, value.WAN.VLAN, value.Proxy.Program, strings.Join(value.LAN.Interfaces, ", "))
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
