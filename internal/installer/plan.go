// Package installer plans and sequences installation independently of the host OS.
package installer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/telemetry"
)

var errPathNotAbsolute = errors.New("installation path must be absolute")

// Plan is the validated input for preview and execution. It performs no I/O.
type Plan struct {
	Config     config.Config
	ConfigPath string
	StateDir   string
	Executable string
	SaveConfig bool
	Replace    bool
}

// step is one stage of an installation: what it is called, and the work.
type step struct {
	name string
	run  func(Backend, context.Context, Plan) error
}

// Backend is the host-specific implementation of each installation stage.
type Backend interface {
	Preflight(context.Context, Plan) error
	Begin(context.Context, Plan) (InstallationTransaction, error)
	PreserveRuntime(context.Context, Plan) error
	SaveConfig(context.Context, Plan) error
	RemoveLegacy(context.Context, Plan) error
	CopyBinary(context.Context, Plan) error
	WriteFiles(context.Context, Plan) error
	Activate(context.Context, Plan) error
	CheckHealth(context.Context, Plan) error
	Cleanup(context.Context, Plan) error
}

// Validate reports whether the plan's config and paths are usable.
func (p Plan) Validate() error {
	if err := ValidateStatePath(p.StateDir); err != nil {
		return err
	}
	err := p.Config.Validate()
	if err != nil {
		return fmt.Errorf("validate installation configuration: %w", err)
	}
	for _, path := range []string{p.ConfigPath, p.StateDir, p.Executable} {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("%w: %q", errPathNotAbsolute, path)
		}
	}

	return nil
}

// steps returns the stages this plan requires, in execution order.
func (p Plan) steps() []step {
	steps := []step{
		{"Check installation prerequisites", Backend.Preflight},
		{"Preserve the proxy and shared libraries offline", Backend.PreserveRuntime},
	}
	if p.SaveConfig {
		steps = append(steps, step{"Save configuration", Backend.SaveConfig})
	}

	return append(steps,
		step{"Remove legacy Debian package if installed", Backend.RemoveLegacy},
		step{"Install persistent executable", Backend.CopyBinary},
		step{"Write service, links, tmpfiles rule and shell completion", Backend.WriteFiles},
		step{"Reload systemd, enable and restart service", Backend.Activate},
		step{"Wait for stable proxy readiness", Backend.CheckHealth},
		step{"Remove obsolete legacy recovery files after health verification", Backend.Cleanup},
	)
}

// Preview intentionally does not accept a Backend: it cannot apply the plan.
// Configuration contents are not printed, since they can include private data.
func (p Plan) Preview(out io.Writer) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if err := writeString(out, "Dry run: no changes, services or telemetry.\n"); err != nil {
		return err
	}
	for _, stage := range p.steps() {
		if err := writef(out, "  - %s\n", stage.name); err != nil {
			return err
		}
	}

	return writeString(out, "Preview complete. Prerequisites and service health remain untested.\n")
}

// Execute stops at the first failure. Cleanup is reached only after health passes.
func (p Plan) Execute(ctx context.Context, backend Backend) (result error) {
	err := p.Validate()
	if err != nil {
		return err
	}
	for index, stage := range p.steps() {
		err := ctx.Err()
		if err != nil {
			return fmt.Errorf("installation cancelled before %q: %w", stage.name, err)
		}
		stepCtx, finish := telemetry.Step(ctx, stage.name)
		err = stage.run(backend, stepCtx, p)
		finish(err)
		if err != nil {
			return fmt.Errorf("%s: %w", stage.name, err)
		}
		if index == 0 {
			transaction, err := backend.Begin(ctx, p)
			if err != nil {
				return fmt.Errorf("prepare installation recovery: %w", err)
			}
			if transaction.finish != nil {
				defer func() { result = transaction.finish(ctx, result) }()
			}
		}
	}

	return nil
}
