package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kjanat/udm-iptv/internal/service"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/network"
	"github.com/spf13/cobra"
)

func (application *Application) daemonCommand() *cobra.Command {
	command := &cobra.Command{
		Use: "daemon", Short: "Run the IPTV service", Hidden: true, Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			err := requireRoot()
			if err != nil {
				return err
			}

			return (&service.Daemon{ConfigPath: application.ConfigPath, StateDir: application.StateDir, Out: application.Out, Err: application.Err, Monitor: application.monitor}).Run(command.Context())
		},
	}

	return command
}

func (application *Application) dhcpHookCommand() *cobra.Command {
	var allowDefaultRoute bool
	command := &cobra.Command{
		Use: "dhcp-hook ACTION", Hidden: true, Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, arguments []string) error {
			if value, err := config.Load(application.ConfigPath); err == nil {
				allowDefaultRoute = value.WAN.AllowDefaultRoute
			}
			lease, err := network.LeaseFromEnvironment(arguments[0])
			if err != nil {
				return err
			}
			switch arguments[0] {
			case "deconfig", "bound", "renew":
				return network.ApplyLease(lease, allowDefaultRoute)
			case "leasefail":
				return errors.New("DHCP lease acquisition failed")
			case "nak":
				return errors.New("DHCP server rejected the lease")
			default:
				return fmt.Errorf("unsupported udhcpc action %q", arguments[0])
			}
		},
	}
	command.Flags().BoolVar(&allowDefaultRoute, "allow-default-route", false, "allow DHCP router fallback when RFC3442 routes are absent")

	return command
}

func (application *Application) waitHealthy(ctx context.Context, startup, stable time.Duration) error {
	return application.monitor.Run(ctx, "service.health", func(ctx context.Context) error {
		return service.WaitHealthy(ctx, startup, stable)
	})
}
