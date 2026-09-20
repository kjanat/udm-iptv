package service

import (
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/kjanat/udm-iptv/internal/config"
)

func TestDHCPRetryPolicyFitsTwoRounds(t *testing.T) {
	value := config.DefaultKPN()
	arguments := dhcpArguments(value, "/test/hook")
	options := map[string]int{}
	for _, flag := range []string{"-t", "-T", "-A"} {
		index := slices.Index(arguments, flag)
		if index < 0 || index+1 == len(arguments) {
			t.Fatalf("missing explicit %s: %v", flag, arguments)
		}
		var err error
		options[flag], err = strconv.Atoi(arguments[index+1])
		if err != nil || options[flag] <= 0 {
			t.Fatalf("invalid %s: %v", flag, arguments)
		}
	}
	// Include room for hook execution and the readiness poll, rather than
	// accepting a second round that ends exactly at the deadline.
	secondRound := time.Duration(2*options["-t"]*options["-T"]+options["-A"]) * time.Second
	if secondRound+5*time.Second >= dhcpAcquireTimeout {
		t.Fatalf("two discovery rounds take %s within %s deadline", secondRound, dhcpAcquireTimeout)
	}
	if slices.Contains(arguments, "-n") || slices.Contains(arguments, "-q") {
		t.Fatal("client must keep retrying and renewing leases")
	}
	value.WAN.DHCPOptions = []string{"-A", "1", "-V", "IPTV_RG"}
	arguments = dhcpArguments(value, "/test/hook")
	if !slices.Equal(arguments[len(arguments)-4:], value.WAN.DHCPOptions) {
		t.Fatal("user DHCP options lost their override precedence")
	}
}
