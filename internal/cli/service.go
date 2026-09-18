package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/network"
	"github.com/kjanat/udm-iptv/internal/service"
	"github.com/kjanat/udm-iptv/internal/telemetry"
)

var (
	errLeaseAcquisitionFailed = errors.New("DHCP lease acquisition failed")
	errLeaseRejected          = errors.New("DHCP server rejected the lease")
	errUnsupportedHookAction  = errors.New("unsupported udhcpc action")
)

func (application *Application) daemonCommand() *cobra.Command {
	command := &cobra.Command{
		Use: telemetry.OperationDaemon, Short: "Run the IPTV service", Hidden: true, Args: cobra.NoArgs,
		RunE: application.reporting(telemetry.OperationDaemon, func(command *cobra.Command, _ []string) error {
			err := requireRoot()
			if err != nil {
				return err
			}

			daemon := &service.Daemon{
				ConfigPath: application.ConfigPath, StateDir: application.StateDir, Out: application.Out, Err: application.Err,
				Monitor: application.monitor, Diagnostics: application.diagnosticsJSON,
			}

			return daemon.Run(command.Context())
		}),
	}

	return command
}

func (application *Application) dhcpHookCommand() *cobra.Command {
	var routes string
	command := &cobra.Command{
		Use: "dhcp-hook ACTION", Hidden: true, Args: cobra.ExactArgs(1),
		RunE: application.reportingHook(func(_ *cobra.Command, arguments []string) error {
			policy := config.RoutePolicy(routes)
			if value, err := config.Load(application.ConfigPath); err == nil {
				policy = value.WAN.DHCPRoutes
			}
			lease, err := network.LeaseFromEnvironment(arguments[0])
			if err != nil {
				return fmt.Errorf("read the DHCP lease from the environment: %w", err)
			}
			switch arguments[0] {
			case "deconfig":
				if previous, err := service.ReadLeaseState(); err == nil {
					lease.Address, lease.Mask = previous.Lease.Address, previous.Lease.Mask
				}

				return errors.Join(network.ApplyLease(lease, policy), service.RemoveLeaseState())
			case "bound", "renew":
				return errors.Join(network.ApplyLease(lease, policy), service.WriteLeaseState(lease))
			case "leasefail":
				return errLeaseAcquisitionFailed
			case "nak":
				return errLeaseRejected
			default:
				return fmt.Errorf("%w %q", errUnsupportedHookAction, arguments[0])
			}
		}),
	}
	command.Flags().StringVar(&routes, "dhcp-routes", string(config.RoutesNoDefault), "route policy when the configuration is unreadable")

	return command
}

// diagnosticsJSON is the snapshot the daemon attaches to its hourly observation.
func (application *Application) diagnosticsJSON(ctx context.Context) (json.RawMessage, error) {
	value, err := application.collector().Snapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("collect diagnostics: %w", err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode diagnostics: %w", err)
	}

	return data, nil
}

func (application *Application) waitHealthy(ctx context.Context, startup, stable time.Duration) error {
	err := application.reportRun(ctx, "service.health", func(ctx context.Context) error {
		return service.WaitHealthy(ctx, startup, stable)
	})
	if err != nil {
		return fmt.Errorf("udm-iptv.service health check: %w", err)
	}

	return nil
}
