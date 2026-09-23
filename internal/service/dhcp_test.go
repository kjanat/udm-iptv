package service

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/network"
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

func TestDHCPChildEnvironmentDoesNotRecordInheritedOptions(t *testing.T) {
	t.Setenv("http_proxy", "http://example:secret@proxy.invalid")
	t.Setenv("private_token", "secret")
	t.Setenv("domain", "not-from-the-server.invalid")
	t.Setenv("UDM_IPTV_CONFIG", "/wrong/config")
	t.Setenv("UDM_IPTV_STATE_DIR", "/wrong/state")
	t.Setenv("IF_METRIC", "320")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDHCPHookEnvironmentHelper$")
	// Model udhcpc adding the environment values it received from the server.
	child.Env = append(dhcpEnvironment("/chosen/config", "/chosen/state"),
		"UDM_IPTV_TEST_DHCP_ENV=1", "interface=iptv", "ip=192.0.2.20", "mask=24",
		"dns=192.0.2.53", "opt224=vendor-option")
	output, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("hook environment subprocess: %v: %s", err, output)
	}
	var lease network.Lease
	if err := json.Unmarshal(output, &lease); err != nil {
		t.Fatal(err)
	}
	if lease.Metric != 320 || lease.Options["dns"] != "192.0.2.53" || lease.Options["opt224"] != "vendor-option" {
		t.Fatalf("lease options lost: %+v", lease)
	}
	for _, forbidden := range []string{"http_proxy", "private_token", "domain", "interface"} {
		if _, exists := lease.Options[forbidden]; exists {
			t.Errorf("inherited/non-option %q was recorded", forbidden)
		}
	}
}

func TestDHCPHookEnvironmentHelper(t *testing.T) {
	if os.Getenv("UDM_IPTV_TEST_DHCP_ENV") != "1" {
		return
	}
	if os.Getenv("UDM_IPTV_CONFIG") != "/chosen/config" || os.Getenv("UDM_IPTV_STATE_DIR") != "/chosen/state" || os.Getenv("PATH") == "" {
		t.Fatal("explicit hook environment lost")
	}
	lease, err := network.LeaseFromEnvironment("bound")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range os.Environ() {
		if strings.Contains(entry, "secret") {
			t.Fatal("inherited secret reached child")
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(lease); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}
