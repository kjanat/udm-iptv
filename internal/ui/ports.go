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

func (port Port) address() string {
	if len(port.Addresses) > 0 {
		return strings.Join(port.Addresses, ", ")
	}
	if port.AddressesKnown {
		return "no assigned IP"
	}

	return "addresses unavailable"
}

func (port Port) label() string {
	return annotate(port.Name, port.address(), port.Description)
}

func (port Port) lanLabel() string {
	return annotate(port.Name, lanKind(port.Name), port.address())
}

const (
	manualPort  = "__manual__"
	manualLabel = "Enter another interface manually…"
)

const (
	// manualEntrySlots reserves room for the "enter manually" and blank options
	// appended after the discovered ports.
	manualEntrySlots = 2
	// maxManualEntries is the number of manually entered networks the list
	// reserves rows for before it starts scrolling.
	maxManualEntries = 4
)

func withManual(options []huh.Option[string]) []huh.Option[string] {
	return append(slices.Clone(options), huh.NewOption(manualLabel, manualPort))
}

func hasOption(options []huh.Option[string], value string) bool {
	return slices.Contains(optionValues(options), value)
}

func listHeight(options []huh.Option[string]) int {
	return len(options) + manualEntrySlots + maxManualEntries + selectChrome
}

func wanGroups(current *string, ports []Port) ([]*page, *string) {
	selected := *current
	options := make([]huh.Option[string], 0, len(ports)+manualEntrySlots)
	for _, port := range ports {
		options = append(options, huh.NewOption(port.label(), port.Name))
	}
	if selected != "" && !hasOption(options, selected) {
		options = append(options, huh.NewOption(annotate(selected, "configured value", "availability unknown"), selected))
	}
	if selected == "" {
		selected = manualPort
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
			if !hasOption(options, name) {
				options = append(options, huh.NewOption(annotate(name, "entered manually"), name))
			}
			selected = name
			list.Options(withManual(options)...)
		},
	}

	return []*page{
		newPage(huh.NewSelect[string]().Key("wan-port").
			Title("Which connection goes to your provider?").
			Description("Usually Internet route. Connected means link detected, not provider verified.").
			Options(withManual(options)...).Height(listHeight(options)).
			Validate(func(value string) error {
				if value == manualPort {
					return errors.New("press Enter on that row to type a port name")
				}

				return nil
			}).
			Value(&selected)).title("Internet port").entering(entry),
	}, &selected
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

func lanGroups(current []string, ports []Port) ([]*page, *[]string) {
	selected := resolveLAN(current)
	options := make([]huh.Option[string], 0, len(ports)+len(current)+manualEntrySlots)
	for _, port := range ports {
		if isDownstreamName(port.Name) && !hasOption(options, port.Name) {
			options = append(options, huh.NewOption(port.lanLabel(), port.Name))
		}
	}
	for _, name := range selected {
		if !hasOption(options, name) {
			options = append(options, huh.NewOption(annotate(name, lanKind(name), "configured value"), name))
		}
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
			for _, name := range names {
				if !hasOption(options, name) {
					options = append(options, huh.NewOption(annotate(name, lanKind(name), "entered manually"), name))
				}
			}
			selected = resolveLAN(append(slices.Clone(selected), names...))
			list.Options(withManual(options)...)
		},
	}

	return []*page{
		newPage(huh.NewMultiSelect[string]().Key("lan").
			Title("Which networks should receive IPTV?").
			Description("Press space to tick or untick a network, Enter when done. br0 is LAN. Other brN are VLANs.").
			Options(withManual(options)...).
			Height(listHeight(options)).
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
		if !hasOption(listed, port.Name) {
			result = append(result, huh.NewOption(port.label(), port.Name))
		}
	}

	return result
}

// resolveLAN drops blanks, the manual-entry marker and duplicates.
func resolveLAN(selected []string) []string {
	var result []string
	for _, name := range selected {
		if name != "" && name != manualPort && !slices.Contains(result, name) {
			result = append(result, name)
		}
	}

	return result
}
