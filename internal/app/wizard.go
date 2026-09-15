package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strings"

	"charm.land/huh/v2"
	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/ui"
	"github.com/spf13/cobra"
)

func (application *Application) formRunner() ui.RunForm {
	return func(ctx context.Context, form *huh.Form) error {
		input := application.In
		if input == nil {
			input = strings.NewReader("")
		}
		output := application.Err
		if output == nil {
			output = io.Discard
		}
		// Huh's accessible runner currently discards field errors and ignores
		// cancellation contexts. Keep the context-aware terminal runner explicit.
		err := form.WithInput(input).WithOutput(output).WithAccessible(false).RunWithContext(ctx)
		if errors.Is(err, huh.ErrUserAborted) {
			return errors.Join(context.Canceled, err)
		}
		return err
	}
}

func (application *Application) configureForm(ctx context.Context, value *config.Config) error {
	profiles := config.Profiles()
	for i := range profiles {
		profiles[i].Config = withDetectedInterfaces(profiles[i].Config)
	}
	return ui.Configure(ctx, value, profiles, application.formRunner(), detectedPorts()...)
}

func (application *Application) previewCommand() *cobra.Command {
	return application.previewCommandWith(func(ctx context.Context, value *config.Config) error {
		return ui.Configure(ctx, value, config.Profiles(), application.formRunner(),
			ui.Port{Name: "eth8", Description: "example: connected, Internet route", Addresses: []string{"203.0.113.10/24"}, AddressesKnown: true},
			ui.Port{Name: "eth9", Description: "example: disconnected", AddressesKnown: true},
			ui.Port{Name: "br0", Description: "example: LAN", Addresses: []string{"192.168.1.1/24"}, AddressesKnown: true})
	})
}

func detectedPorts() []ui.Port {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	route := defaultRouteInterfaceFromSystem()
	candidates := wanInterfacesForBoard(detectBoard())
	var ports []ui.Port
	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		description := "link status unknown"
		if data, err := os.ReadFile("/sys/class/net/" + iface.Name + "/carrier"); err == nil {
			switch strings.TrimSpace(string(data)) {
			case "1":
				description = "connected"
			case "0":
				description = "disconnected"
			}
		}
		if iface.Name == route {
			description += ", Internet route"
		}
		if slicesContain(candidates, iface.Name) {
			description += ", WAN candidate"
		}
		port := ui.Port{Name: iface.Name, Description: description}
		addresses, err := iface.Addrs()
		if err == nil {
			port.AddressesKnown = true
			for _, address := range addresses {
				port.Addresses = append(port.Addresses, address.String())
			}
		}
		ports = append(ports, port)
	}
	sort.SliceStable(ports, func(i, j int) bool {
		rank := func(name string) int {
			if name == route {
				return 0
			}
			if slicesContain(candidates, name) {
				return 1
			}
			return 2
		}
		return rank(ports[i].Name) < rank(ports[j].Name)
	})
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
			if err := writeString(application.Out, "Preview · Example data. Nothing is applied or sent.\n"); err != nil {
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
