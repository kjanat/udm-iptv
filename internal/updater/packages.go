package updater

import (
	"context"
	"io"

	"github.com/kjanat/udm-iptv/internal/installer"
)

// packageCommands exposes only the package operations needed by an update.
type packageCommands struct {
	record  func(context.Context) (installer.PackageRecord, error)
	install func(ctx context.Context, packagePath string, allowDowngrade bool, out, errOut io.Writer) error
}
