package config

import (
	"path/filepath"
	"testing"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
)

func TestExistingInvalidLegacyConfigurationIsNotIgnored(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	legacy := filepath.Join(directory, "legacy.conf")
	err := atomicfile.Write(legacy, []byte(`IPTV_WAN_INTERFACE="not a valid interface name"`), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ImportFirstLegacy([]string{filepath.Join(directory, "missing.conf"), legacy}); err == nil {
		t.Fatal("invalid existing legacy configuration was silently ignored")
	}
}
