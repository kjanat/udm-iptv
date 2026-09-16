// Package installer plans and sequences installation independently of the host OS.
package installer

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/kjanat/udm-iptv/internal/config"
)

// Plan is the validated input for preview and execution. It performs no I/O.
type Plan struct {
	Config     config.Config
	ConfigPath string
	StateDir   string
	Executable string
	SaveConfig bool
	Replace    bool
}

type Action string

const (
	Preflight       Action = "Check installation prerequisites"
	PreserveRuntime Action = "Preserve the proxy and shared libraries offline"
	SaveConfig      Action = "Save configuration"
	RemoveLegacy    Action = "Remove legacy Debian package if installed"
	CopyBinary      Action = "Install persistent executable"
	WriteFiles      Action = "Write service, links, tmpfiles rule and shell completion"
	Activate        Action = "Reload systemd, enable and restart service"
	CheckHealth     Action = "Wait for stable proxy readiness"
	Cleanup         Action = "Remove obsolete legacy recovery files after health verification"
)

// Backend provides the host-specific implementation of each installation action.
type Backend interface {
	Apply(context.Context, Action, Plan) error
}

func (p Plan) Validate() error {
	if err := validateStatePath(p.StateDir); err != nil {
		return err
	}
	err := p.Config.Validate()
	if err != nil {
		return err
	}
	for _, path := range []string{p.ConfigPath, p.StateDir, p.Executable} {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("installation path must be absolute: %q", path)
		}
	}

	return nil
}

func (p Plan) Actions() []Action {
	actions := []Action{Preflight, PreserveRuntime}
	if p.SaveConfig {
		actions = append(actions, SaveConfig)
	}

	return append(actions, RemoveLegacy, CopyBinary, WriteFiles, Activate, CheckHealth, Cleanup)
}

// Preview intentionally does not accept a Backend: it cannot apply the plan.
// Configuration contents are not printed, since they can include private data.
func (p Plan) Preview(out io.Writer) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(out, "Dry run: no changes, services or telemetry."); err != nil {
		return err
	}
	for _, action := range p.Actions() {
		if _, err := fmt.Fprintf(out, "  - %s\n", action); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(out, "Preview complete. Prerequisites and service health remain untested.")

	return err
}

// Execute stops at the first failure. Cleanup is reached only after health passes.
func (p Plan) Execute(ctx context.Context, backend Backend) error {
	err := p.Validate()
	if err != nil {
		return err
	}
	for _, action := range p.Actions() {
		err := ctx.Err()
		if err != nil {
			return err
		}
		err = backend.Apply(ctx, action, p)
		if err != nil {
			return fmt.Errorf("%s: %w", action, err)
		}
	}

	return nil
}
