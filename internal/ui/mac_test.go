package ui

import "testing"

func TestMACFieldRejectsKernelInvalidAddresses(t *testing.T) {
	t.Parallel()
	for _, address := range []string{"00:00:00:00:00:00", "ff:ff:ff:ff:ff:ff", "01:00:5e:00:00:01", "00:11:22:33:44:55:66:77"} {
		if err := validateOptionalMAC(address); err == nil {
			t.Errorf("wizard accepted invalid MAC %q", address)
		}
	}
	for _, address := range []string{"", "02:11:22:33:44:55", "00:11:22:33:44:55"} {
		if err := validateOptionalMAC(address); err != nil {
			t.Errorf("wizard rejected valid MAC %q: %v", address, err)
		}
	}
}
