package installer

import (
	"strings"
	"testing"
)

func TestSystemdUnitWaitsForNativeReadiness(t *testing.T) {
	t.Parallel()
	unit := systemdUnit("/custom state/bin/udm-iptv", "/custom state/config.json", "/custom state")
	for _, expected := range []string{
		"Type=notify", "NotifyAccess=main", "TimeoutStartSec=45s",
		`Environment="UDM_IPTV_STATE_DIR=/custom state"`,
		`ExecStart="/custom state/bin/udm-iptv" daemon --config "/custom state/config.json"`,
	} {
		if !strings.Contains(unit, expected) {
			t.Errorf("unit does not contain %q:\n%s", expected, unit)
		}
	}
}
