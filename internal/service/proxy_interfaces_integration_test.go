//go:build linux && integration

package service

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/config/configtest"
)

// The permanent multicast wrapper supplies a native source or firmware IMProxy
// build. Each scenario gets private network and mount namespaces; no host
// interfaces, /run files, or systemd units are changed.
func TestProxyRegistrationRejectsPartialStartup(t *testing.T) {
	binary := os.Getenv("UDM_IPTV_TEST_IMPROXY")
	if binary == "" {
		t.Skip("run bash tests/multicast/registration.sh or set UDM_IPTV_TEST_IMPROXY")
	}
	for _, unavailable := range []string{"missing", "addressless"} {
		t.Run(unavailable, func(t *testing.T) { runRegistrationScenario(t, binary, unavailable == "missing") })
	}
}

func runRegistrationScenario(t *testing.T, binary string, missing bool) {
	t.Helper()
	enterRegistrationNamespace(t)
	prepareVendorInterfaces(t, missing)
	value := configtest.KPN()
	value.WAN.Interface, value.WAN.VLAN = "wan", 0
	value.LAN.Interfaces = []string{"br0", "br2"}
	if err := writeProxyConfig(value); err != nil {
		t.Fatal(err)
	}
	if err := checkDownstreams(value.LAN.Interfaces, net.InterfaceByName); err == nil || !strings.Contains(err.Error(), "br2") {
		t.Fatalf("preflight accepted unavailable br2: %v", err)
	}
	proxy := startVendorProxy(t, binary)
	if err := waitProxyCheck(t.Context(), proxyInterfaceTimeout, func() error {
		return InspectProxyInterfaces(value).IPv4.Check([]string{"wan", "br0"})
	}); err != nil {
		t.Fatal(err)
	}
	assertVendorPartialRejected(t, value, proxy)
	assertDaemonRejectsPartial(t, value)
	if missing {
		createRegistrationInterface(t, "br2")
	}
	addVendorAddress(t, "br2", "203.0.113.1/24")
	if err := checkDownstreams(value.LAN.Interfaces, net.InterfaceByName); err != nil {
		t.Fatal(err)
	}
	if err := proxy.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = proxy.Wait()
	startVendorProxy(t, binary)
	if err := waitProxyCheck(t.Context(), proxyInterfaceTimeout, func() error { return InspectProxyInterfaces(value).Check() }); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(InspectProxyInterfaces(value).IPv4.Names, "br2") {
		t.Fatal("restart did not register br2")
	}
	t.Log("Unavailable br2: preflight refused; proxy alive but partial; activation refused; br2 made ready and proxy restarted: all VIFs registered")
}

func enterRegistrationNamespace(t *testing.T) {
	t.Helper()
	enterPrivateRuntime(t)
	// /proc/net follows /proc/self/net (the thread-group leader), which can
	// belong to an earlier test's namespace. Pin this private mount namespace
	// to the locked scenario thread's network view before reading kernel VIFs.
	if err := unix.Mount("/proc/thread-self/net", "/proc/self/net", "", unix.MS_BIND, ""); err != nil {
		t.Fatal(err)
	}
}

func prepareVendorInterfaces(t *testing.T, missing bool) {
	t.Helper()
	for _, name := range []string{"wan", "br0", "br2"} {
		if name == "br2" && missing {
			continue
		}
		createRegistrationInterface(t, name)
	}
	addVendorAddress(t, "wan", "198.51.100.1/24")
	addVendorAddress(t, "br0", "192.0.2.1/24")
}

func createRegistrationInterface(t *testing.T, name string) {
	t.Helper()
	if err := netlink.LinkAdd(&netlink.Dummy{Name: name}); err != nil {
		t.Fatal(err)
	}
	link, err := netlink.LinkByName(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatal(err)
	}
}

func assertDaemonRejectsPartial(t *testing.T, value config.Config) {
	t.Helper()
	var logs bytes.Buffer
	application := &Daemon{Err: &logs}
	sources := supervised{config: value, proxy: &managedProcess{done: make(chan struct{})}}
	if err := application.supervise(t.Context(), sources); err == nil || !strings.Contains(err.Error(), "br2") {
		t.Fatalf("daemon accepted partial proxy: %v", err)
	}
	if !strings.Contains(logs.String(), "Kernel multicast interface evidence:") || !strings.Contains(logs.String(), "Interface") {
		t.Fatalf("startup failure lost kernel evidence: %s", logs.String())
	}
	if err := application.monitorProxy(t.Context(), sources); err == nil || !strings.Contains(err.Error(), "br2") {
		t.Fatalf("runtime monitor accepted partial proxy: %v", err)
	}
}

func addVendorAddress(t *testing.T, name, cidr string) {
	t.Helper()
	link, err := netlink.LinkByName(name)
	if err != nil {
		t.Fatal(err)
	}
	address, err := netlink.ParseAddr(cidr)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(link, address); err != nil {
		t.Fatal(err)
	}
}

func startVendorProxy(t *testing.T, binary string) *exec.Cmd {
	t.Helper()
	output, err := os.CreateTemp(t.TempDir(), "improxy-*.log")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = output.Close() })
	cmd := exec.CommandContext(t.Context(), binary, "-d", "3", "-c", proxyConfigPath)
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		data, err := os.ReadFile(output.Name())
		if err != nil {
			t.Error(err)
			return
		}
		t.Logf("Proxy process output:\n%s", data)
	})
	return cmd
}

func assertVendorPartialRejected(t *testing.T, value config.Config, proxy *exec.Cmd) {
	t.Helper()
	state := InspectProxyInterfaces(value)
	if err := state.Check(); err == nil || !strings.Contains(err.Error(), "br2") {
		t.Fatalf("accepted partial startup: %+v", state)
	}
	if err := proxy.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("proxy did not stay alive: %v", err)
	}
	// A live process must not pass activation solely because one LAN works.
	if err := observeServiceHealth(t.Context(), time.Millisecond, time.Millisecond, time.Millisecond, func(_ context.Context) (healthSample, error) {
		sample, err := checkedHealthSample(healthyProperties(), RuntimeState{ProxyPID: proxy.Process.Pid}, true)
		return sample, errors.Join(err, InspectProxyInterfaces(value).Check())
	}); err == nil {
		t.Fatal("activation accepted partial proxy")
	}
}
