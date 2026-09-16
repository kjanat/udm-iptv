package service

import (
	"strings"
	"testing"
)

func TestMulticastCountersKeepOnlyNumericTotals(t *testing.T) {
	t.Parallel()
	routes, packets, err := multicastCounters(strings.NewReader("Group Origin Iif Pkts Bytes Wrong\n01020304 05060708 1 0019 1000 0\n02030405 06070809 1 12 400 0\n"))
	if err != nil || routes != 2 || packets != 31 {
		t.Fatalf("incorrect counters: %d, %d, %v", routes, packets, err)
	}
	if _, _, err := multicastCounters(strings.NewReader("header\nbroken row\n")); err == nil {
		t.Fatal("malformed counters treated as valid")
	}
}
