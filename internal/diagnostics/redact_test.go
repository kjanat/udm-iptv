package diagnostics

import (
	"strings"
	"testing"
)

func TestSanitizeCommonIdentifiers(t *testing.T) {
	t.Parallel()
	input := "route 10.207.64.0/20 host 10.207.73.135 lan 192.168.10.1 fe80::1 AA-BB-CC-DD-EE-FF aabb.ccdd.eeff public 195.121.94.212"
	output := sanitize(input)
	for _, secret := range []string{"fe80::1", "AA-BB-CC-DD-EE-FF", "aabb.ccdd.eeff"} {
		if strings.Contains(output, secret) {
			t.Errorf("%q was not redacted: %s", secret, output)
		}
	}
	for _, keep := range []string{"10.207.64.0/20", "10.207.73.135", "192.168.10.1", "195.121.94.212"} {
		if !strings.Contains(output, keep) {
			t.Fatalf("%q was removed: %s", keep, output)
		}
	}
}
