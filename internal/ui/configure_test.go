package ui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"

	"github.com/kjanat/udm-iptv/internal/config"
)

func TestConfigureProfileSwitch(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "accept", true: "cancel"}[cancel], func(t *testing.T) {
			value := config.Default()
			value.Telemetry.Enabled = true
			original := clone(value)
			selected, _ := config.FromProfile("tweak", value)
			selected.WAN.Interface = "example9"
			profiles := []config.Profile{
				{ID: "kpn", Name: "KPN", Config: value},
				{ID: "tweak", Name: "Tweak", Config: selected},
			}
			calls := 0
			aborted := errors.New("cancelled")
			err := Configure(context.Background(), &value, profiles, func(_ context.Context, form *huh.Form) error {
				calls++
				if calls == 1 {
					field := form.GetFocusedField()
					field.Focus()
					_, _ = field.Update(tea.KeyPressMsg{Code: tea.KeyDown})
					_, _ = field.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
				} else if cancel {
					return aborted
				}

				return nil
			})
			wantCalls := 3
			if cancel {
				wantCalls = 2
			}
			if calls != wantCalls {
				t.Fatalf("forms = %d", calls)
			}
			if cancel {
				if !errors.Is(err, aborted) || !reflect.DeepEqual(value, original) {
					t.Fatalf("cancel modified input: %v", err)
				}

				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if value.Profile != "tweak" || value.WAN.Interface != "example9" || !value.Telemetry.Enabled {
				t.Fatalf("wrong selection: %+v", value)
			}
			value.WAN.NATDestinations[0] = "changed"
			if selected.WAN.NATDestinations[0] != "0.0.0.0/0" {
				t.Fatal("profile data aliased")
			}
		})
	}
}

func TestConfigurationPagesFollowAnswers(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*config.Config)
		want []string
	}{
		{
			name: "kpn",
			want: []string{"wan-port", "vlan", "vlan-interface", "dhcp-options", "lan", "nat", "proxy", "telemetry"},
		},
		{
			name: "untagged-static-igmpproxy",
			edit: func(value *config.Config) {
				value.WAN.VLAN = 0
				value.WAN.DHCP = false
				value.WAN.StaticAddress = "10.20.30.1/24"
				value.Proxy.Program = "igmpproxy"
			},
			want: []string{"wan-port", "vlan", "static-address", "lan", "nat", "proxy", "proxy-sources", "telemetry"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := config.Default()
			if test.edit != nil {
				test.edit(&value)
			}
			fields := newFormValues(value)
			groups, _, _, _ := configurationGroups(&value, nil, "", &fields)
			form := wizardForm(groups...)
			var keys []string
			for range 20 {
				if form.State == huh.StateCompleted {
					break
				}
				field := form.GetFocusedField()
				if field == nil {
					t.Fatal("no focused field")
				}
				keys = append(keys, field.GetKey())
				form.NextGroup()
			}
			if !reflect.DeepEqual(keys, test.want) {
				t.Fatalf("pages = %v, want %v", keys, test.want)
			}
		})
	}
}

func TestConfigurationPageFits(t *testing.T) {
	value := config.Default()
	fields := newFormValues(value)
	groups, _, _, _ := configurationGroups(&value, []Port{
		{Name: "eth8", Description: "connected, Internet route", Addresses: []string{"203.0.113.10/24"}, AddressesKnown: true},
		{Name: "br0", Description: "example: LAN", Addresses: []string{"192.168.1.1/24"}, AddressesKnown: true},
		{Name: "br4", Addresses: []string{"192.168.4.1/24"}, AddressesKnown: true},
	}, "IPTV DNS servers: 177.16.30.67 and 177.16.30.7.", &fields)
	form := wizardForm(groups...)
	form.Init()
	for range 12 {
		form.Update(tea.WindowSizeMsg{Width: 180, Height: 45})
		view := form.View()
		if strings.Contains(view, "\n\n\n") {
			t.Fatal("short page padded with blank rows")
		}
		if lipgloss.Width(view) > 88 {
			t.Fatalf("page stretched to %d columns", lipgloss.Width(view))
		}
		if height := lipgloss.Height(view); height > 24 {
			t.Fatalf("page is %d rows", height)
		}
		for _, jammed := range []string{"quickleave?Off", "logs?Temporary", "address?Most"} {
			if strings.Contains(view, jammed) {
				t.Fatalf("confirm title ate its description: %q", jammed)
			}
		}
		if form.State == huh.StateCompleted {
			return
		}
		form.NextGroup()
	}
	t.Fatal("form did not finish")
}

func TestConfigureFailureDoesNotChangeInput(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		value := config.Default()
		value.WAN.VLAN = 5000
		original := clone(value)
		calls := 0
		err := Configure(context.Background(), &value, config.Profiles(), func(_ context.Context, _ *huh.Form) error {
			calls++
			if failAt == 1 {
				return huh.ErrUserAborted
			}

			return nil
		})
		if err == nil || !reflect.DeepEqual(value, original) {
			t.Fatal("failure must leave input untouched")
		}
	}
}
