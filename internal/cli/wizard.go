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
	"github.com/kjanat/udm-iptv/internal/proxyinventory"
	"github.com/kjanat/udm-iptv/internal/telemetry"
	"github.com/kjanat/udm-iptv/internal/ui"
)

func (application *Application) wizardSession(header string) *ui.Session {
	input := application.In
	if input == nil {
		input = strings.NewReader("")
	}
	output := ui.Terminal(application.Err)
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
		if err != nil {
			return fmt.Errorf("run the wizard: %w", err)
		}

		return nil
	}
}

func (application *Application) configureForm(ctx context.Context, value *config.Config, answered ui.Answered) error {
	return application.runConfigureForm(ctx, value, nil, answered)
}

// configureFreshForm asks the reporting question first and only then looks the
// provider up, so a fresh installation never discloses anything before the
// user has answered.
func (application *Application) configureFreshForm(ctx context.Context, value *config.Config, answered ui.Answered) error {
	return application.runConfigureForm(ctx, value, application.suggestProvider, answered)
}

func (application *Application) runConfigureForm(ctx context.Context, value *config.Config, discover ui.Discover, answered ui.Answered) error {
	catalog := config.DefaultCatalog()

	session := application.wizardSession("").Observe(func(event ui.Event, question string) {
		application.monitor.WizardEvent(ctx, string(event), question)
	})
	defer session.Close()

	proxies := proxyinventory.Inspect(ctx, application.StateDir)
	err := ui.ConfigureDetected(ctx, value, catalog, runWizard(session), application.providerSuggestion, discover, answered, proxies, detectedPorts()...)
	if err != nil {
		return fmt.Errorf("collect the configuration: %w", err)
	}

	return nil
}

// Suggestions never replace saved/imported settings or explicit --profile values.
// Failed or disabled discovery leaves the ordinary provider selector untouched.
// settings are the answers the user has just given, never the defaults.
func (application *Application) suggestProvider(ctx context.Context, settings config.Telemetry) (string, error) {
	application.providerSuggestion = ""
	if application.networkIdentity == nil || !settings.Enabled || !settings.NetworkIdentity {
		return "", nil
	}
	err := writeString(application.Err, "Checking provider using ipify and reverse DNS…\n")
	if err != nil {
		return "", err
	}
	identity := application.networkIdentity(ctx)
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("look up the provider: %w", err)
	}
	application.providerSuggestion = suggestedProvider(identity)
	if application.providerSuggestion == "" {
		return "", writeString(application.Err, "Provider unknown. Choose manually.\n")
	}

	return application.providerSuggestion, writef(application.Err, "Suggested: %s (PTR hint, unverified). Confirm your TV provider.\n", application.providerSuggestion)
}

func suggestedProvider(identity telemetry.NetworkIdentity) string {
	if identity.Method != "ptr-suffix" || identity.Confidence != "low" || identity.Status != "ip-and-ptr" {
		return ""
	}
	if _, ok := config.DefaultCatalog().ProviderByID(identity.Provider); ok {
		return identity.Provider
	}

	return ""
}

const previewHeader = "Preview"

func examplePorts(value config.Config) config.Config {
	value.WAN.Interface = "eth8"
	value.LAN.Interfaces = []string{"br0"}

	return value
}

func (application *Application) previewCommand() *cobra.Command {
	return application.previewCommandWith(func(ctx context.Context, value *config.Config) error {
		session := application.wizardSession(previewHeader)
		defer session.Close()

		return ui.Configure(ctx, value, config.DefaultCatalog(), runWizard(session),
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
		Short: "Try the configuration wizard using example data",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			value, err := config.FromProfile(profile, examplePorts(config.Default()))
			if err != nil {
				return fmt.Errorf("apply --profile: %w", err)
			}
			if err := prompt(command.Context(), &value); err != nil {
				return fmt.Errorf("preview ended without saving changes: %w", err)
			}
			data, err := json.MarshalIndent(value, "", "  ")
			if err != nil {
				return fmt.Errorf("encode the previewed configuration: %w", err)
			}

			return writef(application.Out, "%s\n", data)
		},
	}
	command.Flags().StringVar(&profile, "profile", config.ProfileKPN, "provider profile for the example configuration")

	return command
}
