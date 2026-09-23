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

var errUnsupportedHookAction = errors.New("unsupported udhcpc action")

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
		RunE: application.reportingHook(func(command *cobra.Command, arguments []string) error {
			// These callbacks describe recoverable client events.
			// udhcpc keeps trying until the daemon's deadline.
			if arguments[0] == "leasefail" || arguments[0] == "nak" {
				return application.dhcpRetryWarning(command.Context(), arguments[0])
			}
			policy := config.RoutePolicy(routes)
			if value, err := config.Load(application.ConfigPath); err == nil {
				policy = value.WAN.DHCPRoutes
			}
			lease, err := network.LeaseFromEnvironment(arguments[0])
			if err != nil {
				return fmt.Errorf("read the DHCP lease from the environment: %w", err)
			}
			previous := network.Lease{}
			if state, err := service.ReadLeaseState(); err == nil {
				previous = state.Lease
			}
			switch arguments[0] {
			case "deconfig":
				applied := network.ApplyLease(&lease, previous, policy)
				if applied != nil {
					// Retain ownership for a later retry when cleanup was only partial.
					previous.ManagedRoutes = lease.ManagedRoutes
					previous.ManagedAddresses = lease.ManagedAddresses
					return errors.Join(applied, service.WriteLeaseState(previous, applied))
				}
				return service.RemoveLeaseState()
			case "bound", "renew":
				applied := network.ApplyLease(&lease, previous, policy)

				return errors.Join(applied, service.WriteLeaseState(lease, applied))
			default:
				return fmt.Errorf("%w %q", errUnsupportedHookAction, arguments[0])
			}
		}),
	}
	command.Flags().StringVar(&routes, "dhcp-routes", string(config.RoutesNoDefault), "route policy when the configuration is unreadable")

	return command
}

func (application *Application) dhcpRetryWarning(ctx context.Context, action string) error {
	message := "DHCP discovery round exhausted; client will retry"
	if action == "nak" {
		message = "DHCP server rejected the lease; client will retry"
	}
	application.monitor.Warn(ctx, message)
	if _, err := fmt.Fprintln(application.Err, message); err != nil {
		return fmt.Errorf("write DHCP retry warning: %w", err)
	}

	return nil
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
		if err := service.WaitHealthy(ctx, startup, stable); err != nil {
			// Attach diagnostics before the innermost operation reports the
			// failure; outer wrappers intentionally do not report it again.
			return application.reportHealthFailure(ctx, err)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("udm-iptv.service health check: %w", err)
	}

	return nil
}
