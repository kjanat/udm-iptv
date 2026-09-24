package ui

import (
	"fmt"
	"io"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
)

// DHCP's manual-address branch is a popup, not another wizard page.
type dhcpConfirm struct {
	*huh.Confirm

	enabled  *bool
	address  *string
	keys     huh.ConfirmKeyMap
	answered bool
}

func newDHCPConfirm(enabled *bool, address *string) *dhcpConfirm {
	return &dhcpConfirm{
		Confirm: huh.NewConfirm().Key("dhcp").Title("Use DHCP for the IPTV address?").
			Description("Yes: receive an address automatically. No: enter a fixed address in a popup.").
			Affirmative("Yes").Negative("No").Value(enabled),
		enabled: enabled,
		address: address,
		keys:    huh.NewDefaultKeyMap().Confirm,
	}
}

func (d *dhcpConfirm) WithKeyMap(keys *huh.KeyMap) huh.Field {
	d.keys = keys.Confirm
	d.Confirm.WithKeyMap(keys)
	return d
}

func (d *dhcpConfirm) Update(msg tea.Msg) (huh.Model, tea.Cmd) {
	_, cmd := d.Confirm.Update(msg)
	if press, ok := msg.(tea.KeyPressMsg); ok && !*d.enabled && !d.answered && key.Matches(press, d.keys.Next, d.keys.Submit, d.keys.Reject) {
		entry := &entryPrompt{
			title: "Static IPTV address", description: "Enter the address and prefix from your provider, for example 10.0.0.2/24. Leave empty only when this connection needs no IPv4 address.",
			placeholder: "10.0.0.2/24", validate: validateOptionalPrefix,
			textValue: d.address, afterAccept: huh.NextField,
		}
		return d, func() tea.Msg { return openEntryMsg{entry: entry} }
	}
	return d, cmd
}

func (d *dhcpConfirm) RunAccessible(w io.Writer, r io.Reader) error {
	if err := d.Confirm.RunAccessible(w, r); err != nil {
		return fmt.Errorf("read DHCP choice: %w", err)
	}
	if !*d.enabled && !d.answered {
		if err := huh.NewInput().Title("Static IPTV address (address/prefix; empty for none)").Value(d.address).Validate(validateOptionalPrefix).RunAccessible(w, r); err != nil {
			return fmt.Errorf("read static IPTV address: %w", err)
		}
	}
	return nil
}
