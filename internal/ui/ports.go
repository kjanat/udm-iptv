package ui

import (
	"errors"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"charm.land/huh/v2"

	"github.com/kjanat/udm-iptv/internal/config"
)

var (
	errManualPortNeedsEntry = errors.New("press Enter on that row to type a port name")
	errNoNetworkSelected    = errors.New("select at least one network")
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
	return ""
}

func (port Port) label() string {
	return annotate(port.Name, port.address(), port.description())
}

func (port Port) description() string {
	var parts []string
	for part := range strings.SplitSeq(port.Description, ", ") {
		if part != "link status unknown" {
			parts = append(parts, part)
		}
	}

	return strings.Join(parts, ", ")
}

func (port Port) public() bool {
	for _, value := range port.Addresses {
		prefix, err := netip.ParsePrefix(value)
		if err == nil && prefix.Addr().IsGlobalUnicast() && !prefix.Addr().IsPrivate() {
			return true
		}
	}

	return false
}

func (port Port) linkRank() int {
	for state := range strings.SplitSeq(port.Description, ", ") {
		switch state {
		case "connected", "example: connected":
			return 0
		case "disconnected", "example: disconnected":
			return 1
		}
	}

	return 2
}

func orderedWANPorts(ports []Port) []Port {
	ordered := slices.Clone(ports)
	slices.SortStableFunc(ordered, func(left, right Port) int {
		if left.public() != right.public() {
			if left.public() {
				return -1
			}
			return 1
		}
		if left.linkRank() != right.linkRank() {
			return left.linkRank() - right.linkRank()
		}
		leftBase, leftVLAN, _ := strings.Cut(left.Name, ".")
		rightBase, rightVLAN, _ := strings.Cut(right.Name, ".")
		if leftBase != rightBase {
			return strings.Compare(leftBase, rightBase)
		}
		leftID, leftErr := strconv.Atoi(leftVLAN)
		rightID, rightErr := strconv.Atoi(rightVLAN)
		if leftErr == nil && rightErr == nil {
			return leftID - rightID
		}

		return strings.Compare(left.Name, right.Name)
	})

	return ordered
}

func (port Port) lanLabel() string {
	if !isDownstreamName(port.Name) {
		return port.label()
	}
	return annotate(port.Name, lanKind(port.Name), port.address(), port.description())
}

func networkLabel(name string, ports []Port, origin string) string {
	for _, port := range ports {
		if port.Name == name {
			return port.lanLabel()
		}
	}
	return annotate(name, lanKind(name), origin, "addresses unavailable")
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
	ports = orderedWANPorts(ports)
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
		validate:    config.ValidateInterfaceName,
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
			Description("Public IPs first. Ctrl+P: public IPs / all interfaces. Connected means link detected, not provider verified.").
			Options(withManual(options)...).Height(listHeight(options)).
			Validate(func(value string) error {
				if value == manualPort {
					return errManualPortNeedsEntry
				}

				return nil
			}).
			Value(&selected)).title("Internet port").entering(entry).filteringPorts(publicPortFilter(ports, &options, &selected)),
	}, &selected
}

func publicPortFilter(ports []Port, options *[]huh.Option[string], selected *string) func(huh.Field) {
	var public []huh.Option[string]
	for _, port := range ports {
		if port.public() {
			public = append(public, huh.NewOption(port.label(), port.Name))
		}
	}
	filtered := false
	return func(field huh.Field) {
		list, ok := field.(*huh.Select[string])
		if !ok || len(public) == 0 {
			return
		}
		filtered = !filtered
		visible := *options
		if filtered {
			visible = public
		}
		if !hasOption(visible, *selected) {
			*selected = optionValues(visible)[0]
		}
		list.Options(withManual(visible)...)
	}
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

	return "role unknown"
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
			options = append(options, huh.NewOption(networkLabel(name, ports, "configured value"), name))
		}
	}
	entry := &entryPrompt{
		title:       "Add a network",
		description: "Pick an interface the router knows, or type a name as UniFi or ip link shows it. Separate several names with spaces or commas.",
		placeholder: "br4",
		candidates:  unlistedPorts(ports, options),
		validate:    config.ValidateInterfaceName,
		accept: func(field huh.Field, names []string) {
			list, ok := field.(*huh.MultiSelect[string])
			if !ok {
				return
			}
			for _, name := range names {
				if !hasOption(options, name) {
					options = append(options, huh.NewOption(networkLabel(name, ports, "entered manually"), name))
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
					return errNoNetworkSelected
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
