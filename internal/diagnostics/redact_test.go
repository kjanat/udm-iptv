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

func TestSanitizeLeadingColonLiterals(t *testing.T) {
	t.Parallel()
	input := "peer ::1%eth0 mapped ::ffff:127.0.0.1 ll ::ffff:169.254.1.1 mc ::ffff:224.0.0.1 any :: lan ::ffff:10.0.0.25"
	output := sanitize(input)
	for _, secret := range []string{"::1%eth0", "127.0.0.1", "169.254.1.1", "224.0.0.1"} {
		if strings.Contains(output, secret) {
			t.Errorf("%q was not redacted: %s", secret, output)
		}
	}
	if !strings.Contains(output, "::ffff:10.0.0.25") {
		t.Fatalf("mapped private address was removed: %s", output)
	}
}
