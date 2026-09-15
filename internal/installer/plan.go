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
	Preflight    Action = "Check installation prerequisites"
	SaveConfig   Action = "Save configuration"
	RemoveLegacy Action = "Remove legacy Debian package if installed"
	CopyBinary   Action = "Install persistent executable"
	WriteFiles   Action = "Write service, links, tmpfiles rule and shell completion"
	Activate     Action = "Reload systemd, enable and restart service"
	CheckHealth  Action = "Wait for stable proxy readiness"
	Cleanup      Action = "Remove obsolete legacy recovery files after health verification"
)

// Backend provides the host-specific implementation of each installation action.
type Backend interface {
	Apply(context.Context, Action, Plan) error
}

func (p Plan) Validate() error {
	if err := p.Config.Validate(); err != nil {
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
	actions := []Action{Preflight}
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
	if _, err := fmt.Fprintln(out, "Dry run: no files will be saved, no package removed, no service started, and no telemetry sent."); err != nil {
		return err
	}
	for _, action := range p.Actions() {
		if _, err := fmt.Fprintf(out, "  - %s\n", action); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(out, "Preview complete. System prerequisites and service health have not been tested.")
	return err
}

// Execute stops at the first failure. Cleanup is reached only after health passes.
func (p Plan) Execute(ctx context.Context, backend Backend) error {
	if err := p.Validate(); err != nil {
		return err
	}
	for _, action := range p.Actions() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := backend.Apply(ctx, action, p); err != nil {
			return fmt.Errorf("%s: %w", action, err)
		}
	}
	return nil
}
