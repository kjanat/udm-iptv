package ui

import (
	"errors"
	"slices"
	"strconv"
	"strings"

	"charm.land/huh/v2"
)

// Port describes an interface discovered by the caller; the UI never probes it.
type Port struct {
	Name           string
	Description    string
	Addresses      []string
	AddressesKnown bool
}

func (port Port) label() string {
	address := "addresses unavailable"
	if port.AddressesKnown {
		address = "no assigned IP"
	}
	if len(port.Addresses) > 0 {
		address = strings.Join(port.Addresses, ", ")
	}
	return annotate(port.Name, address, port.Description)
}

const manualPort = "__manual__"

const (
	// manualEntrySlots reserves room for the "enter manually" and blank options
	// appended after the discovered ports.
	manualEntrySlots = 2
	// maxManualEntries is the number of manually entered networks the list
	// reserves rows for before it starts scrolling.
	maxManualEntries = 4
)

func wanGroups(current *string, ports []Port) ([]page, *string) {
	selected := *current
	options := make([]huh.Option[string], 0, len(ports)+manualEntrySlots)
	found := false
	for _, port := range ports {
		options = append(options, huh.NewOption(port.label(), port.Name))
		found = found || port.Name == selected
	}
	if !found && selected != "" {
		options = append(options, huh.NewOption(annotate(selected, "configured value", "availability unknown"), selected))
	}
	if selected == "" {
		selected = manualPort
	}
	withManual := func() []huh.Option[string] {
		return append(slices.Clone(options), huh.NewOption("Enter another interface manually…", manualPort))
	}
	entry := &entryPrompt{
		title:       "Which port?",
		description: "Pick a known interface, or type the router's name for the port as UniFi or ip link shows it.",
		placeholder: "eth8",
		candidates:  unlistedPorts(ports, options),
		validate:    validateInterface,
		accept: func(field huh.Field, names []string) {
			list, ok := field.(*huh.Select[string])
			if !ok || len(names) == 0 {
				return
			}
			name := names[0]
			if !slices.ContainsFunc(options, func(option huh.Option[string]) bool { return option.Value == name }) {
				options = append(options, huh.NewOption(annotate(name, "entered manually"), name))
			}
			selected = name
			list.Options(withManual()...)
		},
	}

	return []page{
		newPage(huh.NewSelect[string]().Key("wan-port").
			Title("Which connection goes to your provider?").
			Description("Usually Internet route. Connected means link detected, not provider verified.").
			Options(withManual()...).Height(len(options) + manualEntrySlots + maxManualEntries + selectChrome).
			Validate(func(value string) error {
				if value == manualPort {
					return errors.New("press Enter on that row to type a port name")
				}

				return nil
			}).
			Value(&selected)).title("Internet port").entering(entry),
	}, &selected
}

func (port Port) lanLabel() string {
	address := "addresses unavailable"
	if port.AddressesKnown {
		address = "no assigned IP"
	}
	if len(port.Addresses) > 0 {
		address = strings.Join(port.Addresses, ", ")
	}

	return annotate(port.Name, lanKind(port.Name), address)
}

func lanKind(name string) string {
	if rest, ok := strings.CutPrefix(name, "eth0."); ok && rest != "" {
		return "VLAN " + rest
	}
	if rest, ok := strings.CutPrefix(name, "br"); ok {
		if rest == "" || rest == "0" {
			return "LAN"
		}
		if _, err := strconv.Atoi(rest); err == nil {
			return "VLAN " + rest
		}
	}

	return "LAN"
}

func isDownstreamName(name string) bool {
	return strings.HasPrefix(name, "br") || strings.HasPrefix(name, "eth0.")
}

func lanGroups(current []string, ports []Port) ([]page, *[]string) {
	selected := append([]string{}, current...)
	seen := map[string]bool{}
	options := make([]huh.Option[string], 0, len(ports)+len(current)+1)
	for _, port := range ports {
		if !isDownstreamName(port.Name) || seen[port.Name] {
			continue
		}
		seen[port.Name] = true
		options = append(options, huh.NewOption(port.lanLabel(), port.Name))
	}
	for _, name := range current {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		options = append(options, huh.NewOption(annotate(name, lanKind(name), "configured value"), name))
	}
	withManual := func() []huh.Option[string] {
		return append(slices.Clone(options), huh.NewOption("Enter another interface manually…", manualPort))
	}
	entry := &entryPrompt{
		title:       "Add a network",
		description: "Pick an interface the router knows, or type a name as UniFi or ip link shows it. Separate several names with spaces or commas.",
		placeholder: "br4",
		candidates:  unlistedPorts(ports, options),
		validate:    validateInterface,
		accept: func(field huh.Field, names []string) {
			list, ok := field.(*huh.MultiSelect[string])
			if !ok {
				return
			}
			merged := slices.DeleteFunc(slices.Clone(selected), func(name string) bool { return name == manualPort })
			for _, name := range names {
				if !seen[name] {
					seen[name] = true
					options = append(options, huh.NewOption(annotate(name, lanKind(name), "entered manually"), name))
				}
				if !containsString(merged, name) {
					merged = append(merged, name)
				}
			}
			selected = merged
			list.Options(withManual()...)
		},
	}

	return []page{
		newPage(huh.NewMultiSelect[string]().Key("lan").
			Title("Which networks should receive IPTV?").
			Description("Press space to tick or untick a network, Enter when done. br0 is LAN. Other brN are VLANs.").
			Options(withManual()...).
			Height(len(options) + manualEntrySlots + maxManualEntries + selectChrome).
			Value(&selected).
			Validate(func(values []string) error {
				if len(resolveLAN(values)) == 0 {
					return errors.New("select at least one network")
				}

				return nil
			})).title("TV networks").entering(entry),
	}, &selected
}

// unlistedPorts returns the discovered interfaces a list does not show yet.
func unlistedPorts(ports []Port, listed []huh.Option[string]) []huh.Option[string] {
	var result []huh.Option[string]
	for _, port := range ports {
		if slices.ContainsFunc(listed, func(option huh.Option[string]) bool { return option.Value == port.Name }) {
			continue
		}
		result = append(result, huh.NewOption(port.label(), port.Name))
	}

	return result
}

func resolveLAN(selected []string) []string {
	seen := map[string]bool{}
	var result []string
	for _, name := range selected {
		if name == "" || name == manualPort || seen[name] {
			continue
		}
		seen[name] = true
		result = append(result, name)
	}

	return result
}

func containsString(values []string, wanted string) bool {
	return slices.Contains(values, wanted)
}
