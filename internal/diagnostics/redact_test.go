package diagnostics

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/kjanat/udm-iptv/internal/config"
)

func TestSanitizeCommonIdentifiers(t *testing.T) {
	t.Parallel()
	input := "private 192.168.10.1 fe80::1 AA-BB-CC-DD-EE-FF aabb.ccdd.eeff public 195.121.94.212"
	output := sanitize(input)
	for _, secret := range []string{"192.168.10.1", "fe80::1", "AA-BB-CC-DD-EE-FF", "aabb.ccdd.eeff"} {
		if strings.Contains(output, secret) {
			t.Errorf("%q was not redacted: %s", secret, output)
		}
	}
	if !strings.Contains(output, "195.121.94.212") {
		t.Fatalf("public provider address was removed: %s", output)
	}
}

func TestDiagnosticSanitizerRetainsObservedAndConfiguredAddresses(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "config.json")
	value := config.Default()
	value.WAN.StaticAddress = "198.51.100.10/24"
	err := config.Save(path, value)
	if err != nil {
		t.Fatal(err)
	}
	sanitizer := newDiagnosticSanitizer(path)
	sanitizer.observe([]string{"203.0.113.20"})
	sanitizer.observe([]string{"203.0.113.21"})
	output := sanitizer.sanitize("configured 198.51.100.10 old 203.0.113.20 current 203.0.113.21 provider 195.121.94.212")
	for _, secret := range []string{"198.51.100.10", "203.0.113.20", "203.0.113.21"} {
		if strings.Contains(output, secret) {
			t.Errorf("%q was not redacted: %s", secret, output)
		}
	}
	if !strings.Contains(output, "195.121.94.212") {
		t.Fatalf("unobserved provider address was removed: %s", output)
	}
}
