package network

import "testing"

func TestMaskBits(t *testing.T) {
	t.Parallel()
	for input, want := range map[string]int{"24": 24, "255.255.255.0": 24, "255.255.255.255": 32} {
		got, err := maskBits(input)
		if err != nil {
			t.Errorf("maskBits(%q): %v", input, err)
			continue
		}
		if got != want {
			t.Errorf("maskBits(%q) = %d, want %d", input, got, want)
		}
	}
}

func TestInvalidMasks(t *testing.T) {
	t.Parallel()
	for _, input := range []string{"33", "255.0.255.0", "nope"} {
		if _, err := maskBits(input); err == nil {
			t.Errorf("maskBits(%q) unexpectedly succeeded", input)
		}
	}
}
