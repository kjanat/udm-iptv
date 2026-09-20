package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	systemd "github.com/coreos/go-systemd/v22/dbus"
	"github.com/spf13/cobra"

	"github.com/kjanat/udm-iptv/internal/service"
)

var errNegativePause = errors.New("--for must be at least one microsecond")

func (application *Application) startCommand() *cobra.Command {
	return &cobra.Command{
		Use: "start", Short: "Start the IPTV service", Args: cobra.NoArgs,
		RunE: application.reporting("start", func(command *cobra.Command, _ []string) error {
			if err := requireRoot(); err != nil {
				return err
			}

			return application.start(command.Context())
		}),
	}
}

func (application *Application) stopCommand() *cobra.Command {
	var pause time.Duration
	command := &cobra.Command{
		Use: "stop", Short: "Stop the IPTV service", Args: cobra.NoArgs,
		RunE: application.reporting("stop", func(command *cobra.Command, _ []string) error {
			if command.Flags().Changed("for") && pause < time.Microsecond {
				return errNegativePause
			}
			if err := requireRoot(); err != nil {
				return err
			}

			return application.stop(command.Context(), pause)
		}),
	}
	command.Flags().DurationVar(&pause, "for", 0, "start again after this long, for example 30m")

	return command
}

func (application *Application) start(ctx context.Context) error {
	connection, err := systemd.NewSystemConnectionContext(ctx)
	if err != nil {
		return fmt.Errorf("connect to systemd: %w", err)
	}
	defer connection.Close()
	lifecycle := service.Lifecycle{Connection: connection, Unit: service.Unit}
	if err := lifecycle.Start(ctx); err != nil {
		return fmt.Errorf("start %s: %w", service.Unit, err)
	}
	if err := application.waitHealthy(ctx, restartHealthStartup, restartHealthStable); err != nil {
		return err
	}

	return writef(application.Out, "%s started.\n", service.Unit)
}

func (application *Application) stop(ctx context.Context, pause time.Duration) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("stop IPTV: %w", err)
	}
	// Recovery needs the bus after the command's context is canceled.
	connection, err := systemd.NewSystemConnectionContext(context.WithoutCancel(ctx))
	if err != nil {
		return fmt.Errorf("connect to systemd: %w", err)
	}
	defer connection.Close()
	lifecycle := service.Lifecycle{Connection: connection, Unit: service.Unit}
	if pause == 0 {
		if err := lifecycle.Stop(ctx); err != nil {
			return fmt.Errorf("stop IPTV: %w", err)
		}
		return writef(application.Out, "%s stopped. Run 'udm-iptv start' to start it again.\n", service.Unit)
	}
	if err := lifecycle.Pause(ctx, pause); err != nil {
		return fmt.Errorf("pause IPTV: %w", err)
	}
	return writef(application.Out, "%s stopped. It starts again in %s. Run 'udm-iptv status' for the scheduled time.\n", service.Unit, pause)
}
