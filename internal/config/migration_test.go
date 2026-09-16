package config

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
)

func TestLegacyMissingDefaults(t *testing.T) {
	for _, test := range []struct {
		vlan, dhcp, program string
		wantDHCP            bool
		wantProxy           string
	}{
		{"0", "", "", false, "igmpproxy"},
		{"4", "", "", true, "igmpproxy"},
		{"0", "true", "improxy", true, "improxy"},
		{"4", "false", "igmpproxy", false, "igmpproxy"},
	} {
		file := filepath.Join(t.TempDir(), "legacy.conf")
		data := fmt.Sprintf("IPTV_WAN_VLAN=%s\nIPTV_WAN_RANGES=1.2.3.0/24\n", test.vlan)
		if test.dhcp != "" {
			data += "IPTV_WAN_DHCP=" + test.dhcp + "\n"
		}
		if test.program != "" {
			data += "IPTV_IGMPPROXY_PROGRAM=" + test.program + "\n"
		}
		if err := atomicfile.Write(file, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		value, err := ImportLegacy(file)
		if err != nil {
			t.Fatal(err)
		}
		if value.WAN.DHCP != test.wantDHCP || value.Proxy.Program != test.wantProxy {
			t.Fatalf("migration defaults: %+v", test)
		}
		if test.wantProxy == "igmpproxy" && len(value.Proxy.SourceRanges) == 0 {
			t.Fatal("missing legacy sources")
		}
	}
}
