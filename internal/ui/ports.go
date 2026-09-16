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
	label := port.Name + " · " + address
	if port.Description != "" {
		label += " — " + port.Description
	}

	return label
}

const manualPort = "__manual__"

func wanGroups(current *string, ports []Port) ([]*huh.Group, *string) {
	selected := *current
	options := make([]huh.Option[string], 0, len(ports)+2)
	found := false
	for _, port := range ports {
		options = append(options, huh.NewOption(port.label(), port.Name))
		found = found || port.Name == selected
	}
	if !found && selected != "" {
		options = append(options, huh.NewOption(selected+" — configured value; availability unknown", selected))
	}
	if selected == "" {
		selected = manualPort
	}
	options = append(options, huh.NewOption("Enter another interface manually…", manualPort))

	return []*huh.Group{
		huh.NewGroup(huh.NewSelect[string]().Key("wan-port").
			Title("Which connection goes to your provider?").
			Description("Usually Internet route. Connected means link detected, not provider verified.").
			Options(options...).Height(min(8, len(options)+4)).Value(&selected)).Title("Internet port"),
		huh.NewGroup(huh.NewInput().Key("wan-interface").Title("Interface name").
			Description("Enter the interface name from UniFi or ip link.").
			Placeholder("eth8").Value(current).Validate(validateInterface)).WithHideFunc(func() bool { return selected != manualPort }),
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

	return port.Name + " · " + lanKind(port.Name) + " · " + address
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

func lanGroups(current []string, ports []Port) ([]*huh.Group, *[]string, *string) {
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
		options = append(options, huh.NewOption(name+" · "+lanKind(name)+" · configured value", name))
	}
	if len(selected) == 0 {
		selected = []string{manualPort}
	}
	options = append(options, huh.NewOption("Enter another interface manually…", manualPort))
	extra := ""

	return []*huh.Group{
		huh.NewGroup(huh.NewMultiSelect[string]().Key("lan").
			Title("Which networks should receive IPTV?").
			Description("br0 is LAN. Other brN are VLANs.").
			Options(options...).Height(min(8, len(options)+4)).
			Value(&selected).
			Validate(func(values []string) error {
				if len(resolveLAN(values, extra)) == 0 && !containsString(values, manualPort) {
					return errors.New("select at least one network")
				}

				return nil
			})).Title("TV networks"),
		huh.NewGroup(huh.NewInput().Key("lan-extra").Title("Additional interface names").
			Description("Space-separated, for example br4.").
			Placeholder("br4").Value(&extra).
			Validate(func(value string) error {
				if len(resolveLAN(selected, value)) == 0 {
					return errors.New("enter at least one interface name")
				}
				for _, name := range resolveLAN(selected, value) {
					err := validateInterface(name)
					if err != nil {
						return err
					}
				}

				return nil
			})).WithHideFunc(func() bool { return !containsString(selected, manualPort) }),
	}, &selected, &extra
}

func resolveLAN(selected []string, extra string) []string {
	seen := map[string]bool{}
	var result []string
	add := func(name string) {
		if name == "" || name == manualPort || seen[name] {
			return
		}
		seen[name] = true
		result = append(result, name)
	}
	for _, name := range selected {
		add(name)
	}
	for name := range strings.FieldsSeq(extra) {
		add(name)
	}

	return result
}

func containsString(values []string, wanted string) bool {
	return slices.Contains(values, wanted)
}
