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

var (
	errNotIPv4Prefix         = errors.New("use IPv4 prefixes, for example 213.75.0.0/16")
	errVLANIDOutOfRange      = errors.New("enter a VLAN ID between 0 and 4094")
	errNotMACAddress         = errors.New("enter a valid MAC address")
	errNotIPv4CIDR           = errors.New("enter an IPv4 CIDR address")
	errSourcePrefixesMissing = errors.New("igmpproxy needs at least one source prefix")
)

const (
	// selectChrome is the extra rows a select adds around its visible options.
	selectChrome = 4
	// igmpVersion2 is IGMPv2, the compatibility fallback next to the recommended v3.
	igmpVersion2 = 2
	// mldVersion1 is MLDv1, the compatibility fallback next to MLDv2.
	mldVersion1 = 1
	// reviewQuestions is the one confirmation the wizard ends with.
	reviewQuestions = 1
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
	return ConfigureSuggested(ctx, value, catalog, run, "", nil, ports...)
}

// Answered lists field keys the caller has already filled in, by flag or
// otherwise. A page whose every field is answered is skipped, and an
// answered "profile" skips the provider chain.
type Answered []string

func (answered Answered) has(key string) bool {
	return slices.Contains(answered, key)
}

func (answered Answered) covers(p *page) bool {
	if len(p.keys) == 0 {
		return false
	}
	for _, key := range p.keys {
		if !answered.has(key) {
			return false
		}
	}

	return true
}

// FieldKeys lists every key the settings form can ask, so callers can map
// their own inputs onto Answered.
func FieldKeys() []string {
	value := config.Default()
	fields := newFormValues(value)
	pages, _, _ := configurationGroups(&value, nil, "", &fields, true)
	keys := []string{"profile", "static-address"} // Static address is answered in the DHCP popup.
	for _, p := range pages {
		keys = append(keys, p.keys...)
	}

	return keys
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
		return config.ProfileCustom
	}
	_, profile, found := strings.Cut(chosen.choice, "/")
	if !found {
		return config.ProfileCustom
	}

	return profile
}

func startingSelection(catalog config.Catalog, current, suggestion string) selection {
	if provider, found := catalog.ProviderByID(suggestion); found {
		return selection{country: provider.Countries[0], choice: choiceOf(provider.ID, catalog.ProfilesOf(provider.ID)[0].ID)}
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
	if country, found := catalog.Country(code); found {
		return country.Name
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
// the telemetry choice. Re-choosing the current profile keeps the draft.
func applyProfile(catalog config.Catalog, value *config.Config, profileID string) error {
	if profileID == value.Profile {
		return nil
	}
	applied, err := catalog.Apply(profileID, *value)
	if err != nil {
		return fmt.Errorf("select a provider profile: %w", err)
	}
	*value = applied

	return nil
}

// settingsForm builds the settings pages for the draft and returns the
// wizard plus the function that copies the answers back into the draft.
func settingsForm(catalog config.Catalog, value *config.Config, ports []Port, asked int, askConsent bool, answered Answered) (*Wizard, func() error) {
	note := ""
	if profile, found := catalog.Profile(value.Profile); found {
		note = profile.Note
	}
	fields := newFormValues(*value)
	groups, selectedPort, selectedLAN := configurationGroups(value, ports, note, &fields, askConsent)
	for _, p := range groups {
		if p.address != nil {
			p.address.answered = answered.has("static-address")
			if !value.WAN.DHCP && !p.address.answered {
				continue
			}
		}
		if answered.covers(p) {
			p.skip()
		}
	}
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
	stageConsent wizardStage = iota
	stageChain
	stageSettings
	stageReview
	stageDone
)

type configureSession struct {
	catalog    config.Catalog
	run        RunForm
	draft      *config.Config
	apply      func() error
	settings   *Wizard
	ports      []Port
	suggestion string
	answered   Answered
	chosen     selection
	discover   Discover
	remaining  int
	asked      int
	start      int
	accepted   bool
	fresh      bool
}

// Discover returns a provider suggestion for the answered reporting settings,
// or an empty ID when the lookup is not permitted or found nothing.
type Discover func(context.Context, config.Telemetry) (string, error)

// ConfigureSuggested highlights evidence without treating it as an applied choice.
// The caller supplies a provider ID only for a new, unconfigured installation.
func ConfigureSuggested(ctx context.Context, value *config.Config, catalog config.Catalog, run RunForm, suggestion string, answered Answered, ports ...Port) error {
	return configure(ctx, value, catalog, run, suggestion, nil, answered, ports)
}

// ConfigureFresh asks the reporting question before anything else, then runs
// discover, so a provider lookup never precedes the answer that permits it.
// A fresh installation has no saved answer to rely on.
func ConfigureFresh(ctx context.Context, value *config.Config, catalog config.Catalog, run RunForm, discover Discover, answered Answered, ports ...Port) error {
	return configure(ctx, value, catalog, run, "", discover, answered, ports)
}

func configure(ctx context.Context, value *config.Config, catalog config.Catalog, run RunForm, suggestion string, discover Discover, answered Answered, ports []Port) error {
	draft := clone(*value)
	fresh := discover != nil
	estimate, _ := settingsForm(catalog, &draft, ports, 0, !fresh, answered)
	session := &configureSession{
		catalog:    catalog,
		run:        run,
		draft:      &draft,
		ports:      ports,
		suggestion: suggestion,
		answered:   answered,
		chosen:     startingSelection(catalog, draft.Profile, suggestion),
		discover:   discover,
		remaining:  estimate.visibleFields() + reviewQuestions,
		accepted:   true,
		fresh:      fresh,
	}
	if err := session.walk(ctx); err != nil {
		return err
	}
	config.NormalizeAddressing(&draft)
	*value = draft

	return nil
}

var wizardStages = map[wizardStage]func(*configureSession, context.Context) (wizardStage, error){
	stageConsent:  (*configureSession).askConsent,
	stageChain:    (*configureSession).pickProfile,
	stageSettings: (*configureSession).editSettings,
	stageReview:   (*configureSession).confirmSettings,
}

func (session *configureSession) firstStage() wizardStage {
	if session.fresh {
		return stageConsent
	}

	return stageChain
}

func (session *configureSession) walk(ctx context.Context) error {
	for stage := session.firstStage(); stage != stageDone; {
		next, err := wizardStages[stage](session, ctx)
		if err != nil {
			return err
		}
		stage = next
	}

	return nil
}

// askConsent runs before provider discovery so that a default-enabled setting
// never authorises the first lookup on its own.
func (session *configureSession) askConsent(ctx context.Context) (wizardStage, error) {
	if !session.answered.has("telemetry") {
		page := newPage(telemetryConsent(&session.draft.Telemetry))
		if err := session.run(ctx, wizardForm(page).steps(0, session.remaining)); err != nil {
			return stageConsent, err
		}
	}
	suggestion, err := session.discover(ctx, session.draft.Telemetry)
	if err != nil {
		return stageConsent, err
	}
	session.suggestion = suggestion
	session.chosen = startingSelection(session.catalog, session.draft.Profile, suggestion)
	session.start = 0

	return stageChain, nil
}

func (session *configureSession) pickProfile(ctx context.Context) (wizardStage, error) {
	if session.answered.has("profile") {
		session.asked = 0
		session.settings, session.apply = session.settingsForm()

		return stageSettings, nil
	}
	asked, err := chooseProfile(ctx, session.catalog, session.run, &session.chosen, session.suggestion, session.remaining, session.start)
	if errors.Is(err, ErrBack) && session.fresh {
		return stageConsent, nil
	}
	if err != nil {
		return stageChain, err
	}
	session.asked = asked
	if err := applyProfile(session.catalog, session.draft, session.chosen.profile()); err != nil {
		return stageChain, err
	}
	session.settings, session.apply = session.settingsForm()

	return stageSettings, nil
}

func (session *configureSession) settingsForm() (*Wizard, func() error) {
	return settingsForm(session.catalog, session.draft, session.ports, session.asked, !session.fresh, session.answered)
}

func (session *configureSession) editSettings(ctx context.Context) (wizardStage, error) {
	if session.settings.visiblePages() == 0 {
		if err := session.apply(); err != nil {
			return stageSettings, err
		}

		return stageReview, nil
	}
	err := session.run(ctx, session.settings)
	if errors.Is(err, ErrBack) {
		session.start = max(session.asked-1, 0)

		return stageChain, nil
	}
	if err != nil {
		return stageSettings, err
	}
	if err := session.apply(); err != nil {
		return stageSettings, err
	}

	return stageReview, nil
}

func (session *configureSession) confirmSettings(ctx context.Context) (wizardStage, error) {
	err := session.run(ctx, reviewForm(*session.draft, session.asked+session.settings.visiblePages(), &session.accepted))
	if errors.Is(err, ErrBack) {
		session.settings, session.apply = session.settingsForm()

		return stageSettings, nil
	}
	if err != nil {
		return stageReview, err
	}
	if !session.accepted {
		return stageReview, context.Canceled
	}

	return stageDone, nil
}

func configurationPages(value *config.Config, ports []Port, note string, fields *formValues) []*page {
	groups, _, _ := configurationGroups(value, ports, note, fields, true)

	return groups
}

func configurationGroups(value *config.Config, ports []Port, note string, fields *formValues, askConsent bool) ([]*page, *string, *[]string) {
	groups, selectedPort := wanGroups(&value.WAN.Interface, ports)
	lanPages, selectedLAN := lanGroups(value.LAN.Interfaces, ports)
	groups = append(groups, uplinkPages(value, note, fields)...)
	groups = append(groups, lanPages...)
	groups = append(groups, multicastPages(value, fields)...)
	if askConsent {
		groups = append(groups, newPage(telemetryConsent(&value.Telemetry)))
	}

	return groups, selectedPort, selectedLAN
}

func uplinkPages(value *config.Config, note string, fields *formValues) []*page {
	dhcp := newDHCPConfirm(&value.WAN.DHCP, &value.WAN.StaticAddress)
	connection := newPage(
		huh.NewInput().Key("vlan").Title("IPTV VLAN ID").
			Description("Use 0 when IPTV is untagged.").
			Placeholder("4").Value(&fields.vlan).Validate(validateVLANID),
		dhcp,
	).title("IPTV connection")
	connection.address = dhcp
	if note != "" {
		connection = connection.description(note)
	}

	return []*page{
		connection,
		newPage(
			huh.NewInput().Key("dhcp-options").Title("DHCP client options").
				Description("Space-separated flags and values. Keep the provider profile's defaults unless instructed otherwise.").
				Value(&fields.dhcpOptions),
			huh.NewSelect[config.RoutePolicy]().Key("dhcp-routes").Title("Set up access to your provider's TV services?").
				Description("Usually needed for the TV guide, replay and on-demand video.").
				Options(
					huh.NewOption("TV via IPTV; internet via your normal connection (recommended)", config.RoutesNoDefault),
					huh.NewOption("Internet via IPTV too (only if your provider requires it)", config.RoutesAllowDefault),
					huh.NewOption("Do not configure automatically (already set up separately)", config.RoutesNone),
				).Value(&value.WAN.DHCPRoutes),
		).title("DHCP options").hide(func() bool { return !value.WAN.DHCP }),
		newPage(
			huh.NewInput().Key("vlan-interface").Title("VLAN interface name").
				Description("Virtual name, not a physical port.").
				Placeholder("iptv").Value(&value.WAN.VLANInterface).Validate(config.ValidateInterfaceName),
			huh.NewInput().Key("vlan-mac").Title("Custom MAC address").
				Description("Leave empty unless your provider requires it.").
				Value(&value.WAN.VLANMAC).Validate(validateOptionalMAC),
		).title("VLAN interface").hide(func() bool { return fields.vlan == "0" }),
	}
}

func multicastPages(value *config.Config, fields *formValues) []*page {
	return []*page{
		newPage(newPrefixInputs(&fields.nat)).title("IPTV destinations"),
		newPage(
			huh.NewSelect[int]().Key("mld").Title("Does IPTV also use IPv6 multicast?").
				Description("IPv4 uses IGMP. Enable MLD only if your provider also uses IPv6.").
				Options(
					huh.NewOption("IPv4 only (MLD off)", 0),
					huh.NewOption("IPv4 and IPv6 (MLDv2)", config.MaxMLDVersion),
					huh.NewOption("IPv4 and IPv6 (legacy MLDv1)", mldVersion1),
				).Value(&value.Proxy.MLDVersion),
			newProxySelect(value),
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
				Value(&fields.sources).Validate(validateSourcePrefixes),
		).title("Multicast sources").hide(func() bool { return value.Proxy.Program != config.ProxyIgmpproxy }),
	}
}

func proxyOptions(mld int) []huh.Option[string] {
	options := []huh.Option[string]{huh.NewOption("improxy (recommended)", config.ProxyImproxy)}
	if mld == 0 {
		options = append(options, huh.NewOption("igmpproxy (IPv4 only)", config.ProxyIgmpproxy))
	}
	return options
}

func reviewSummary(value config.Config) string {
	return fmt.Sprintf("%s, %s, VLAN %d, %s\nLAN: %s", value.Profile, value.WAN.Interface, value.WAN.VLAN, value.Proxy.Program, strings.Join(value.LAN.Interfaces, ", "))
}

func telemetryConsent(settings *config.Telemetry) huh.Field {
	return huh.NewConfirm().Key("telemetry").
		Title("Help improve udm-iptv?").
		Description("Share failures, metrics and your settings with the maintainer.").
		Affirmative("Yes").Negative("No").Value(&settings.Enabled)
}

func clone(value config.Config) config.Config {
	return value.Clone()
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
			return errNotIPv4Prefix
		}
	}

	return nil
}

func validateVLANID(value string) error {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 || parsed > 4094 {
		return errVLANIDOutOfRange
	}

	return nil
}

func validateOptionalMAC(value string) error {
	if value == "" {
		return nil
	}
	if _, err := net.ParseMAC(value); err != nil {
		return errNotMACAddress
	}

	return nil
}

func validateOptionalPrefix(value string) error {
	if value == "" {
		return nil
	}
	prefix, err := netip.ParsePrefix(value)
	if err != nil || !prefix.Addr().Is4() {
		return errNotIPv4CIDR
	}

	return nil
}

func validateSourcePrefixes(value string) error {
	if len(splitList(value)) == 0 {
		return errSourcePrefixesMissing
	}

	return validatePrefixes(value)
}
