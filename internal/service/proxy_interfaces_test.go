package service

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/kjanat/udm-iptv/internal/mroute"
)

func TestProxyHealthRequiresEveryInterfaceAndFamily(t *testing.T) {
	for _, test := range []struct {
		name  string
		state ProxyInterfaces
		want  string
	}{
		{"idle-complete", ProxyInterfaces{IPv4: mroute.Interfaces{Names: []string{"iptv", "br0", "br2"}}}, ""},
		{"live-partial-proxy", ProxyInterfaces{IPv4: mroute.Interfaces{Names: []string{"iptv", "br0"}}}, "IPv4: multicast interface not registered: br2"},
		{"missing-upstream", ProxyInterfaces{IPv4: mroute.Interfaces{Names: []string{"br0", "br2"}}}, "iptv"},
		{"unreadable", ProxyInterfaces{IPv4: mroute.Interfaces{Error: "permission denied"}}, "permission denied"},
		{"IPv6-partial", ProxyInterfaces{IPv4: mroute.Interfaces{Names: []string{"iptv", "br0", "br2"}}, IPv6: &mroute.Interfaces{Names: []string{"iptv", "br0"}}}, "IPv6: multicast interface not registered: br2"},
		{"stale-config", ProxyInterfaces{IPv4: mroute.Interfaces{Names: []string{"iptv", "br0", "br2"}}, ConfigError: "not applied"}, "not applied"},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.state.Expected = []string{"iptv", "br0", "br2"}
			err := test.state.Check()
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Check = %v, want %q", err, test.want)
			}
		})
	}
}

func TestDownstreamRequiresUpAndIPv4(t *testing.T) {
	for _, test := range []struct {
		name      string
		flags     net.Flags
		addresses []net.Addr
		want      string
	}{
		{"down", 0, []net.Addr{&net.IPNet{IP: net.ParseIP("192.0.2.1"), Mask: net.CIDRMask(24, 32)}}, "down"},
		{"no-address", net.FlagUp, nil, "no IPv4"},
		{"IPv6-only", net.FlagUp, []net.Addr{&net.IPNet{IP: net.ParseIP("fe80::1"), Mask: net.CIDRMask(64, 128)}}, "no IPv4"},
		{"ready", net.FlagUp, []net.Addr{&net.IPNet{IP: net.ParseIP("192.0.2.1"), Mask: net.CIDRMask(24, 32)}}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := checkDownstreamAddresses("br2", test.flags, test.addresses)
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("got %v, want %s", err, test.want)
			}
		})
	}
}

func TestProxyRegistrationWaitsForReadiness(t *testing.T) {
	calls := 0
	err := waitProxyCheck(t.Context(), time.Second, func() error {
		calls++
		if calls == 1 {
			return errDownstreamNotReady
		}
		return nil
	})
	if err != nil || calls != 2 {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
	err = waitProxyCheck(t.Context(), time.Millisecond, func() error { return errDownstreamNotReady })
	if !errors.Is(err, errDownstreamNotReady) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lost failure reason: %v", err)
	}
}
