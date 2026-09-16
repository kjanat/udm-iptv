package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"charm.land/huh/v2"
	"github.com/spf13/cobra"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/device"
	"github.com/kjanat/udm-iptv/internal/telemetry"
	"github.com/kjanat/udm-iptv/internal/ui"
)

func (application *Application) wizardSession(header string) *ui.Session {
	input := application.In
	if input == nil {
		input = strings.NewReader("")
	}
	output := application.Err
	if output == nil {
		output = io.Discard
	}

	return ui.NewSession(header, input, output)
}

func runWizard(session *ui.Session) ui.RunForm {
	return func(ctx context.Context, wizard *ui.Wizard) error {
		err := session.Run(ctx, wizard)
		if errors.Is(err, huh.ErrUserAborted) {
			return errors.Join(context.Canceled, err)
		}

		return err
	}
}

func (application *Application) configureForm(ctx context.Context, value *config.Config) error {
	profiles := config.Profiles()
	for i := range profiles {
		profiles[i].Config = device.WithInterfaces(profiles[i].Config)
	}

	session := application.wizardSession("").Observe(func(event, question string) {
		application.monitor.WizardEvent(ctx, event, question)
	})
	defer session.Close()

	return ui.ConfigureSuggested(ctx, value, profiles, runWizard(session), application.providerSuggestion, detectedPorts()...)
}

// Suggestions never replace saved/imported settings or explicit --profile values.
// Failed or disabled discovery leaves the ordinary provider selector untouched.
func (application *Application) suggestProvider(ctx context.Context, value config.Config) error {
	application.providerSuggestion = ""
	if application.networkIdentity == nil || !value.Telemetry.Enabled || !value.Telemetry.NetworkIdentity {
		return nil
	}
	err := writeString(application.Err, "Checking provider using ipify and reverse DNS…\n")
	if err != nil {
		return err
	}
	identity := application.networkIdentity(ctx)
	err = ctx.Err()
	if err != nil {
		return err
	}
	application.providerSuggestion = suggestedProfile(identity)
	if application.providerSuggestion == "" {
		return writeString(application.Err, "Provider unknown. Choose manually.\n")
	}

	return writef(application.Err, "Suggested: %s (PTR hint, unverified). Confirm your TV provider.\n", application.providerSuggestion)
}

func suggestedProfile(identity telemetry.NetworkIdentity) string {
	if identity.Method != "ptr-suffix" || identity.Confidence != "low" || identity.Status != "ip-and-ptr" {
		return ""
	}
	id := identity.Provider
	if id == "xs4all" || id == "freedom" {
		id = "kpn"
	}
	if _, ok := config.ProfileByID(id); ok && id != "custom" {
		return id
	}

	return ""
}

const previewHeader = "Preview"

func (application *Application) previewCommand() *cobra.Command {
	return application.previewCommandWith(func(ctx context.Context, value *config.Config) error {
		session := application.wizardSession(previewHeader)
		defer session.Close()

		return ui.Configure(ctx, value, config.Profiles(), runWizard(session),
			ui.Port{Name: "eth8", Description: "example: connected, Internet route", Addresses: []string{"203.0.113.10/24"}, AddressesKnown: true},
			ui.Port{Name: "eth9", Description: "example: disconnected", AddressesKnown: true},
			ui.Port{Name: "br0", Description: "example: LAN", Addresses: []string{"192.168.1.1/24"}, AddressesKnown: true})
	})
}

func detectedPorts() []ui.Port {
	ports := make([]ui.Port, 0, len(device.Ports()))
	for _, port := range device.Ports() {
		ports = append(ports, ui.Port(port))
	}

	return ports
}

// The preview has no discovery, persistence, installer or telemetry dependencies.
func (application *Application) previewCommandWith(prompt func(context.Context, *config.Config) error) *cobra.Command {
	var profile string
	command := &cobra.Command{
		Use:   "preview",
		Short: "Try the configuration wizard using example data; discard all changes",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			value, err := config.FromProfile(profile, config.Default())
			if err != nil {
				return err
			}
			if err := prompt(command.Context(), &value); err != nil {
				return fmt.Errorf("preview ended without saving changes: %w", err)
			}
			data, err := json.MarshalIndent(value, "", "  ")
			if err != nil {
				return err
			}

			return writef(application.Out, "%s\n", data)
		},
	}
	command.Flags().StringVar(&profile, "profile", "kpn", "provider profile for the example configuration")

	return command
}
