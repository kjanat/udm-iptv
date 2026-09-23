//go:build linux && firmware

package firmware_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/kjanat/udm-iptv/internal/diagnostics"
)

const binary = firmwareBinary

type firmwareHarness struct {
	t                                    *testing.T
	root, packagePath, data, overlay, id string
	containers                           []string
	engine                               *client.Client
	artifact                             [sha256.Size]byte
	evidence                             string
	evidenceSequence                     int
	commandSequence                      int
}

func firmwareImages(t *testing.T) (string, string) {
	t.Helper()
	if runtime.GOARCH != "arm64" {
		t.Fatal("firmware tests require a native ARM64 runner")
	}
	from, to := os.Getenv("SRC"), os.Getenv("DST")
	if from == "" || to == "" || from == to {
		t.Fatal("two distinct firmware images are required")
	}

	return from, to
}

func newFirmwareHarness(t *testing.T) *firmwareHarness {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	h := &firmwareHarness{t: t, root: root, packagePath: filepath.Join(root, "dist/udm-iptv-arm64.deb")}
	artifact, err := os.ReadFile(filepath.Join(root, "dist/udm-iptv-linux-arm64"))
	if err != nil {
		t.Fatalf("read shared build artifact: %v", err)
	}
	h.artifact = sha256.Sum256(artifact)
	h.engine, err = client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		err := h.engine.Close()
		if err != nil {
			t.Error(err)
		}
	})
	if _, err := os.Stat(h.packagePath); err != nil {
		t.Fatal(err)
	}
	h.id = fmt.Sprintf("udm-iptv-firmware-%d-%d", os.Getpid(), time.Now().UnixNano())
	h.evidence = os.Getenv("UDM_IPTV_FIRMWARE_EVIDENCE")
	if h.evidence == "" {
		h.evidence = filepath.Join(root, "dist", "firmware-evidence")
	}
	h.evidence = filepath.Join(h.evidence, h.t.Name(), h.id)
	h.data, h.overlay = h.id+"-data", h.id+"-etc"
	t.Cleanup(h.cleanup)
	h.docker("volume", "create", h.data)
	h.docker("volume", "create", h.overlay)

	return h
}

func (h *firmwareHarness) requirePersistenceContract(images ...string) {
	h.t.Helper()
	for _, image := range images {
		if _, err := h.tryDocker("image", "inspect", image); err != nil {
			h.docker("pull", image)
		}
		// Refuse a firmware whose persistence contract differs from the harness.
		h.docker("run", "--rm", "--platform", "linux/arm64", image, "sh", "-ec", `
grep -Fq 'upperdir=${MNT_RWFS}/data' /usr/share/initramfs-tools/scripts/ubnt
if grep -Eq '^etc/systemd/system/?$' /usr/share/initramfs-tools/scripts/ubnt; then exit 1; fi`)
	}
}

func (h *firmwareHarness) installPreviousPackage(name, image string) {
	h.t.Helper()
	h.boot(name, image, "bridge", false)
	// Preconfigure a static test network with reporting disabled, using the
	// real CLI. No background harness process supplies fake DHCP readiness.
	h.inside(name, "dpkg-deb", "-x", "/package.deb", "/run/package")
	h.inside(name, "/run/package"+binary, "configure", "set", "--profile", "kpn",
		"--wan-interface", "eth8", "--lan-interface", "br0", "--dhcp=false",
		"--static-address", "198.51.100.2/24", "--telemetry=false")
	h.inside(name, "apt-get", "update")
	// Exercise a package-version upgrade without depending on a past Go release.
	h.inside(name, "sh", "-ec", `
dpkg-deb -R /package.deb /run/previous-package
sed -i '/^Version:/s/$/~firmwaretest/' /run/previous-package/DEBIAN/control
dpkg-deb -Zxz --root-owner-group -b /run/previous-package /run/previous.deb`)
	h.aptInstall(name, "/run/previous.deb")
	h.healthy(name)
	h.assertNetwork(name, "iptv", "198.51.100.2/24", kpnDestinations)
	// The bumped package carries the current executable, so dpkg's record
	// and the running version disagree the way an in-place swap leaves them.
	h.assertStaleRecord(name)
}

func (h *firmwareHarness) upgradePackage(name string) (string, []byte) {
	h.t.Helper()
	h.aptInstall(name, "/package.deb")
	h.healthy(name)
	version := strings.TrimSpace(h.inside(name, binary, "version"))
	h.inside(name, "sh", "-ec", `test "$(dpkg-query -W -f='${Version}' udm-iptv)" = "$(dpkg-deb -f /package.deb Version)"`)
	h.assertPackageRecord(name, version)
	h.assertNetwork(name, "iptv", "198.51.100.2/24", kpnDestinations)
	config := h.readConfig(name)
	h.completes(name)
	h.capture(name)

	return version, config
}

// kpnDestinations are the NAT destinations of the kpn profile the lifecycle
// configuration starts from.
var kpnDestinations = []string{"213.75.0.0/16", "217.166.0.0/16", "195.121.0.0/16"}

// aptInstall installs a package and fails on anything debconf complains
// about, which apt prints without failing.
func (h *firmwareHarness) aptInstall(name, path string) {
	h.t.Helper()
	output := h.inside(name, "apt-get", "install", "-y", path)
	if strings.Contains(output, "debconf:") {
		h.t.Fatalf("debconf complained during apt-get install %s:\n%s", path, output)
	}
}

// assertPackageRecord checks that dpkg's record, the running executable and
// the status report all name the same version.
func (h *firmwareHarness) assertPackageRecord(name, version string) {
	h.t.Helper()
	state := h.status(name)
	if state.Version != version || state.Service.Package != version {
		h.t.Fatalf("status reports version %q with package %q, want %q for both", state.Version, state.Service.Package, version)
	}
	if text := h.inside(name, binary, "status"); !strings.Contains(text, "Installation: package "+version+"\n") {
		h.t.Fatalf("status does not name the package installation:\n%s", text)
	}
}

// assertStaleRecord checks that a dpkg record that trails the executable is
// named as such, in the JSON and in the text.
func (h *firmwareHarness) assertStaleRecord(name string) {
	h.t.Helper()
	state := h.status(name)
	if state.Service.Package == "" || state.Service.Package == state.Version {
		h.t.Fatalf("stale record not reported: version %q, package %q", state.Version, state.Service.Package)
	}
	if text := h.inside(name, binary, "status"); !strings.Contains(text, "recorded by dpkg while "+state.Version+" runs") {
		h.t.Fatalf("status does not flag the stale record:\n%s", text)
	}
}

// assertNetwork checks the IPTV state the service is supposed to produce:
// the address on the target interface and one managed NAT rule per
// configured destination, with the evidence entries that go with them.
// An empty target skips the interface check for a detected port.
func (h *firmwareHarness) assertNetwork(name, target, address string, destinations []string) {
	h.t.Helper()
	state := h.status(name)
	if target != "" && state.Network.Target != target {
		h.t.Fatalf("IPTV interface %q, want %q", state.Network.Target, target)
	}
	if !slices.Contains(state.Network.Addresses, address) {
		h.t.Fatalf("address %s missing on %s: %v", address, state.Network.Target, state.Network.Addresses)
	}
	h.assertNATRules(state, destinations)
	h.assertNATChain(name, state.Network.Target, destinations)
}

func (h *firmwareHarness) assertNATRules(state routerStatus, destinations []string) {
	h.t.Helper()
	if state.NATRules == nil || state.NATEvidence == nil {
		h.t.Fatal("NAT rules or evidence unreadable")
	}
	managed := map[string]bool{}
	for _, rule := range *state.NATRules {
		if !rule.Managed {
			h.t.Fatalf("unmanaged NAT rule for %s on %s", rule.Destination, state.Network.Target)
		}
		managed[rule.Destination] = true
	}
	if len(managed) != len(destinations) {
		h.t.Fatalf("NAT rules %v, want one per destination in %v", *state.NATRules, destinations)
	}
	for _, destination := range destinations {
		if !managed[destination] {
			h.t.Fatalf("no NAT rule for %s: %v", destination, *state.NATRules)
		}
	}
	if len(*state.NATEvidence) != len(destinations) {
		h.t.Fatalf("NAT evidence %v, want one entry per destination", *state.NATEvidence)
	}
}

func (h *firmwareHarness) assertNATChain(name, target string, destinations []string) {
	h.t.Helper()
	chain := h.inside(name, "iptables", "-t", "nat", "-S", "POSTROUTING")
	for _, destination := range destinations {
		if !strings.Contains(chain, "-d "+destination+" -o "+target+" -m comment --comment udm-iptv -j MASQUERADE") {
			h.t.Fatalf("iptables holds no udm-iptv rule for %s:\n%s", destination, chain)
		}
	}
}

// completes runs the installed completion function outside Readline. compopt
// is a builtin that only works inside a completion, so a shell function
// stands in for it; Cobra skips the builtin when it is shadowed.
func (h *firmwareHarness) completes(name string) {
	h.t.Helper()
	h.inside(name, "bash", "--noprofile", "--norc", "-ec", `
test -s /etc/bash_completion.d/udm-iptv
test -s /etc/profile.d/udm-iptv-completion.sh
source /etc/bash_completion.d/udm-iptv
complete -p udm-iptv >/dev/null
compopt() { :; }
COMP_WORDS=(udm-iptv "")
COMP_CWORD=1
COMP_LINE=$'udm-iptv '
COMP_POINT=${#COMP_LINE}
__start_udm-iptv
test "${#COMPREPLY[@]}" -gt 0
printf '%s\n' "${COMPREPLY[@]}" | grep -qx configure
`)
}

func (h *firmwareHarness) requireFailedServiceReports(name string) {
	h.t.Helper()
	h.inside(name, "sh", "-ec", `mkdir -p /run/systemd/system/udm-iptv.service.d
printf '[Service]\nExecStart=\nExecStart=/bin/false\nRestart=no\n' > /run/systemd/system/udm-iptv.service.d/failure.conf
systemctl daemon-reload`)
	for _, command := range [][]string{{"install", "--force", "--non-interactive"}, {"start"}, {"restart"}} {
		h.inside(name, "systemctl", "reset-failed", "udm-iptv.service")
		args := append([]string{"sh", "-ec", `if "$@" > /run/service-failure.log 2>&1; then
 cat /run/service-failure.log
 exit 1
fi
cat /run/service-failure.log
if grep -q 'Automatic startup enabled' /run/service-failure.log; then exit 1; fi`, "failure-check", binary}, command...)
		if err := checkFailureReport(h.inside(name, args...)); err != nil {
			h.t.Fatalf("%s: %v", command[0], err)
		}
	}
	h.inside(name, "rm", "/run/systemd/system/udm-iptv.service.d/failure.conf")
	h.inside(name, "systemctl", "daemon-reload")
	h.inside(name, "systemctl", "reset-failed", "udm-iptv.service")
	h.inside(name, binary, "restart")
	h.healthy(name)
}

func (h *firmwareHarness) reboot(name string) {
	h.t.Helper()
	h.stop(name)
	h.docker("start", name)
	h.waitBoot(name)
}

func (h *firmwareHarness) stop(name string) {
	h.t.Helper()
	h.retainEvidence(name, "before-stop")
	h.docker("stop", name)
}

func (h *firmwareHarness) bootReplacement(name, image, version string, config []byte) {
	h.t.Helper()
	h.boot(name, image, "none", true)
	if got := strings.TrimSpace(h.docker("inspect", "-f", "{{.HostConfig.NetworkMode}}", name)); got != "none" {
		h.t.Fatalf("firmware recovery has network access: %s", got)
	}
	h.inside(name, "test", "!", "-e", "/usr/share/udm-iptv/go-package")
	h.inside(name, "sh", "-ec", "if command -v improxy; then exit 1; fi")
	h.healthy(name)
	h.assertConfig(name, config)
	if got := strings.TrimSpace(h.inside(name, binary, "version")); got != version {
		h.t.Fatalf("version changed: %s -> %s", version, got)
	}
	h.inside(name, "test", "-x", "/usr/local/bin/udm-iptv")
	h.assertNetwork(name, "iptv", "198.51.100.2/24", kpnDestinations)
	// Confirm the running process uses the saved runtime, not a substitute.
	state := h.status(name)
	executable := strings.TrimSpace(h.inside(name, "readlink", fmt.Sprintf("/proc/%d/exe", state.Service.ProxyPID)))
	if !strings.HasPrefix(executable, "/data/udm-iptv/runtime/") {
		h.t.Fatalf("proxy bypassed preserved runtime: %s", executable)
	}
	h.capture(name)
}

func (h *firmwareHarness) removeKeepingConfig(name string) {
	h.t.Helper()
	h.retainEvidence(name, "before-uninstall")
	h.inside(name, binary, "uninstall", "--keep-config")
	h.inside(name, "test", "-f", "/data/udm-iptv/config.json")
	h.reboot(name)
	h.inside(name, "test", "!", "-e", binary)
	h.inside(name, "test", "!", "-e", "/etc/systemd/system/udm-iptv.service")
}

func (h *firmwareHarness) reinstallAndPurge(name string) {
	h.t.Helper()
	h.inside(name, "dpkg-deb", "-x", "/package.deb", "/run/package")
	h.inside(name, "/run/package"+binary, "install", "--non-interactive")
	h.healthy(name)
	h.retainEvidence(name, "before-purge")
	h.inside(name, binary, "uninstall")
	h.reboot(name)
	h.inside(name, "sh", "-ec", `test "$(ls -A /data/udm-iptv)" = .lock`)
	h.inside(name, "test", "!", "-e", "/etc/systemd/system/udm-iptv.service")
}

// The firmware build tag separates privileged lifecycle tests from unit tests.
func TestFirmwareLifecycle(t *testing.T) {
	from, to := firmwareImages(t)
	h := newFirmwareHarness(t)
	h.requirePersistenceContract(from, to)
	first, second := h.id+"-from", h.id+"-to"
	t.Log("Install the Debian package on the previous firmware")
	h.installPreviousPackage(first, from)
	t.Log("Upgrade to the current Debian package")
	version, originalConfig := h.upgradePackage(first)
	t.Log("A failed service must report diagnostics for install, start and restart")
	h.requireFailedServiceReports(first)

	t.Log("Reboot without reinstalling or manually starting the service")
	h.reboot(first)
	h.healthy(first)
	h.assertConfig(first, originalConfig)
	h.assertNetwork(first, "iptv", "198.51.100.2/24", kpnDestinations)
	h.assertPackageRecord(first, version)

	t.Log("Swap firmware rootfs offline; erase the firmware proxy")
	h.stop(first)
	h.bootReplacement(second, to, version, originalConfig)

	t.Log("Reboot the replacement firmware offline")
	h.reboot(second)
	h.healthy(second)
	h.assertConfig(second, originalConfig)
	h.assertNetwork(second, "iptv", "198.51.100.2/24", kpnDestinations)

	t.Log("Remove while keeping configuration; reboot must stay removed")
	h.removeKeepingConfig(second)

	t.Log("Reinstall standalone using the preserved proxy; then purge")
	h.reinstallAndPurge(second)
}

func (h *firmwareHarness) setting(name, setting string) string {
	h.t.Helper()

	return strings.TrimSpace(h.inside(name, binary, "configure", "get", setting))
}

func (h *firmwareHarness) assertSettings(name string, want map[string]string) {
	h.t.Helper()
	for setting, value := range want {
		if got := h.setting(name, setting); got != value {
			h.t.Fatalf("%s = %q, want %q", setting, got, value)
		}
	}
}

func (h *firmwareHarness) assertPackageConfigured(name string) {
	h.t.Helper()
	h.inside(name, "sh", "-ec", `test "$(dpkg-query -W -f='${db:Status-Status}' udm-iptv)" = installed`)
	h.inside(name, "test", "-f", "/etc/systemd/system/udm-iptv.service")
}

// A cold package installation with a named profile: nothing is configured
// before apt runs, and postinst must save the profile and install the
// service in that order. The container has no network, so the detected
// uplink is a dummy port and the BT profile's static address applies to it.
func TestFirmwareFreshPackageInstall(t *testing.T) {
	from, _ := firmwareImages(t)
	h := newFirmwareHarness(t)
	h.requirePersistenceContract(from)
	name := h.id + "-fresh"
	h.boot(name, from, "none", false)
	h.inside(name, "test", "!", "-e", "/data/udm-iptv")
	h.inside(name, "sh", "-ec", `echo 'udm-iptv udm-iptv/profile select bt' | debconf-set-selections`)
	h.aptInstall(name, "/package.deb")
	h.assertPackageConfigured(name)
	h.healthy(name)
	h.assertSettings(name, map[string]string{"profile": "bt", "wan-vlan": "0", "dhcp": "false", "static-address": "10.20.30.1/24", "lan-interface": "br0"})
	h.assertNetwork(name, "", "10.20.30.1/24", []string{"109.159.247.0/24"})
	h.assertPackageRecord(name, strings.TrimSpace(h.inside(name, binary, "version")))
}

// A console that only holds the v4 package's configuration backup: postinst
// must import it and leave the debconf default profile unused.
func TestFirmwareLegacyBackupBootstrap(t *testing.T) {
	from, _ := firmwareImages(t)
	h := newFirmwareHarness(t)
	h.requirePersistenceContract(from)
	name := h.id + "-legacy"
	h.boot(name, from, "bridge", false)
	h.inside(name, "sh", "-ec", `mkdir -p /data/udm-iptv
cat > /data/udm-iptv/udm-iptv.conf <<'EOF'
IPTV_WAN_INTERFACE="eth9"
IPTV_WAN_VLAN="0"
IPTV_WAN_DHCP="false"
IPTV_WAN_STATIC_IP="198.51.100.2/24"
IPTV_WAN_RANGES="198.51.100.0/24"
IPTV_LAN_INTERFACES="br0"
IPTV_IGMPPROXY_PROGRAM="improxy"
IPTV_IGMPPROXY_IGMP_VERSION="3"
EOF
echo 'udm-iptv udm-iptv/profile select kpn' | debconf-set-selections`)
	h.inside(name, "apt-get", "update")
	h.aptInstall(name, "/package.deb")
	h.assertPackageConfigured(name)
	h.healthy(name)
	h.assertSettings(name, map[string]string{"profile": "legacy", "wan-interface": "eth9", "wan-vlan": "0", "dhcp": "false", "static-address": "198.51.100.2/24"})
	h.assertNetwork(name, "eth9", "198.51.100.2/24", []string{"198.51.100.0/24"})
	h.inside(name, "test", "!", "-e", "/data/udm-iptv/udm-iptv.conf")
}

// A DHCP profile on a real image: the VLAN is created, the lease is
// obtained from the provider-side server, its RFC3442 route is installed,
// the lease is recorded, and the NAT evidence tells the routed destination
// from the two that no route reaches. All of it must come back after a
// reboot without anyone touching the service.
func TestFirmwareDHCPLease(t *testing.T) {
	from, _ := firmwareImages(t)
	h := newFirmwareHarness(t)
	h.requirePersistenceContract(from)
	name := h.id + "-dhcp"
	h.bootWith(name, from, "none", false, true)
	h.inside(name, "dpkg-deb", "-x", "/package.deb", "/run/package")
	h.inside(name, "/run/package"+binary, "configure", "set", "--profile", "kpn",
		"--wan-interface", "eth8", "--lan-interface", "br0", "--telemetry=false")
	h.aptInstall(name, "/package.deb")
	h.healthy(name)
	h.assertLease(name)
	h.reboot(name)
	h.healthy(name)
	h.assertLease(name)
	h.inside(name, "systemctl", "stop", "udm-iptv.service")
	h.inside(name, "sh", "-ec", `if ip link show iptv >/dev/null 2>&1; then exit 1; fi`)
	h.inside(name, "sh", "-ec", `if iptables -t nat -S POSTROUTING | grep -q 'comment udm-iptv'; then exit 1; fi`)
}

const (
	leaseNetwork = "10.207.64.0/20"
	leaseRoute   = "213.75.112.0/21 via 10.207.64.1"
)

func (h *firmwareHarness) assertLease(name string) {
	h.t.Helper()
	state := h.status(name)
	if state.Network.Target != "iptv" {
		h.t.Fatalf("IPTV interface %q, want the VLAN interface", state.Network.Target)
	}
	pool := netip.MustParsePrefix(leaseNetwork)
	if !slices.ContainsFunc(state.Network.Addresses, func(address string) bool {
		prefix, err := netip.ParsePrefix(address)

		return err == nil && pool.Contains(prefix.Addr())
	}) {
		h.t.Fatalf("no lease address from %s on iptv: %v", leaseNetwork, state.Network.Addresses)
	}
	if !slices.Contains(state.Network.Routes, leaseRoute) {
		h.t.Fatalf("RFC3442 route missing: %v", state.Network.Routes)
	}
	if slices.ContainsFunc(state.Network.Routes, func(route string) bool { return strings.HasPrefix(route, "default") }) {
		h.t.Fatalf("a default route was installed on iptv: %v", state.Network.Routes)
	}
	h.assertLeaseRecord(state)
	h.assertNATRules(state, kpnDestinations)
	h.assertNATChain(name, "iptv", kpnDestinations)
	for _, entry := range *state.NATEvidence {
		routed := entry.Destination == "213.75.0.0/16"
		if routed != (len(entry.Routes) > 0) {
			h.t.Fatalf("NAT evidence for %s lists routes %v", entry.Destination, entry.Routes)
		}
	}
}

func (h *firmwareHarness) assertLeaseRecord(state routerStatus) {
	h.t.Helper()
	if state.Lease == nil {
		h.t.Fatal("no lease recorded")
	}
	if !state.Lease.Applied {
		h.t.Fatalf("lease recorded as not applied: %s", state.Lease.Failure)
	}
	if action := state.Lease.Lease.Action; action != "bound" && action != "renew" {
		h.t.Fatalf("lease action %q", action)
	}
	if !slices.Contains(state.Lease.Lease.StaticRoutes, "213.75.112.0/21") {
		h.t.Fatalf("lease record lacks the RFC3442 route: %v", state.Lease.Lease.StaticRoutes)
	}
}

func (h *firmwareHarness) docker(args ...string) string {
	h.t.Helper()
	output, err := h.tryDocker(args...)
	if err != nil {
		h.t.Fatalf("docker %v: %v\n%s", args, err, output)
	}

	return output
}

func (h *firmwareHarness) readConfig(container string) []byte {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(h.t.Context(), 10*time.Second)
	defer cancel()
	result, err := h.engine.CopyFromContainer(ctx, container, client.CopyFromContainerOptions{SourcePath: "/data/udm-iptv/config.json"})
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() {
		if err := result.Content.Close(); err != nil {
			h.t.Error(err)
		}
	}()
	content, err := readConfigArchive(result.Content)
	if err != nil {
		h.t.Fatal(err)
	}

	return content
}

func (h *firmwareHarness) assertConfig(container string, expected []byte) {
	h.t.Helper()
	if !bytes.Equal(h.readConfig(container), expected) {
		h.t.Fatal("configuration changed across firmware lifecycle")
	}
}

func (h *firmwareHarness) tryDocker(args ...string) (string, error) {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(h.t.Context(), 3*time.Minute)
	defer cancel()
	result := evidenceResult{Command: evidenceCommand{File: "output.log", Args: args}, Started: time.Now().UTC()}
	output, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	result.Finished = time.Now().UTC()
	if err != nil {
		result.Error = err.Error()
	}
	h.commandSequence++
	directory := filepath.Join(h.evidence, "commands", fmt.Sprintf("%04d", h.commandSequence))
	if saveErr := saveFirmwareCommand(directory, result, output); saveErr != nil {
		h.t.Logf("Save command evidence (%s): %v", directory, saveErr)
	}

	return string(output), err
}

func (h *firmwareHarness) inside(name string, args ...string) string {
	h.t.Helper()

	return h.docker(append([]string{"exec", name}, args...)...)
}

func (h *firmwareHarness) boot(name, image, network string, eraseProxy bool) {
	h.t.Helper()
	h.bootWith(name, image, network, eraseProxy, false)
}

func (h *firmwareHarness) bootWith(name, image, network string, eraseProxy, dhcp bool) {
	h.t.Helper()
	h.containers = append(h.containers, name)
	erase, serve := "0", "0"
	if eraseProxy {
		erase = "1"
	}
	if dhcp {
		serve = "1"
	}
	h.docker("run", "-d", "--name", name, "--platform", "linux/arm64", "--privileged", "--cgroupns=host",
		"--network", network, "--stop-signal", "SIGRTMIN+3",
		"-v", "/sys/fs/cgroup:/sys/fs/cgroup:rw",
		"--tmpfs", "/run:exec", "--tmpfs", "/run/lock", "--tmpfs", "/tmp:exec",
		"-v", h.data+":/data", "-v", h.overlay+":/var/lib/udm-iptv-test/etc-overlay",
		"-v", h.packagePath+":/package.deb:ro",
		"-v", filepath.Join(h.root, "tests/firmware/boot.sh")+":/harness.sh:ro",
		"-v", filepath.Join(h.root, "tests/firmware/udm-iptv-test.target")+":/usr/local/lib/systemd/system/udm-iptv-test.target:ro",
		"-e", "DEBIAN_FRONTEND=noninteractive", "-e", "UDM_IPTV_TEST_ERASE_PROXY="+erase, "-e", "UDM_IPTV_TEST_DHCP="+serve,
		image, "/bin/sh", "/harness.sh")
	h.waitBoot(name)
}

func (h *firmwareHarness) waitBoot(name string) {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(h.t.Context(), 90*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		if exec.CommandContext(ctx, "docker", "exec", name, "systemctl", "is-active", "--quiet", "udm-iptv-test.target").Run() == nil {
			return
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
	h.t.Fatal("firmware did not reach its test target")
}

type routerStatus struct {
	Version string `json:"version"`
	Service struct {
		Package       string `json:"package"`
		LoadState     string `json:"loadState"`
		ActiveState   string `json:"activeState"`
		SubState      string `json:"subState"`
		UnitFileState string `json:"unitFileState"`
		Proxy         string `json:"proxy"`
		Restarts      uint64 `json:"restarts"`
		ProxyPID      int    `json:"proxyPID"`
	} `json:"service"`
	Network struct {
		Target    string   `json:"target"`
		Addresses []string `json:"addresses"`
		Routes    []string `json:"routes"`
	} `json:"network"`
	Lease *struct {
		Applied bool   `json:"applied"`
		Failure string `json:"failure"`
		Lease   struct {
			Action       string   `json:"action"`
			StaticRoutes []string `json:"staticRoutes"`
		} `json:"lease"`
	} `json:"lease"`
	NATRules *[]struct {
		Destination string `json:"destination"`
		Managed     bool   `json:"managed"`
	} `json:"natRules"`
	NATEvidence *[]struct {
		Destination string   `json:"destination"`
		Routes      []string `json:"routes"`
	} `json:"natEvidence"`
}

func (h *firmwareHarness) status(name string) routerStatus {
	h.t.Helper()
	output := h.inside(name, binary, "status", "--json")
	var snapshot diagnostics.Snapshot
	if err := json.Unmarshal([]byte(output), &snapshot); err != nil {
		h.t.Fatal(err)
	}
	if err := checkRunningUnit(snapshot); err != nil {
		h.t.Fatal(err)
	}
	var state routerStatus
	err := json.Unmarshal([]byte(output), &state)
	if err != nil {
		h.t.Fatal(err)
	}

	return state
}

func (h *firmwareHarness) healthy(name string) {
	h.t.Helper()
	h.assertArtifact(name)
	// Observe automatic activation without issuing a service start or restart.
	h.inside(name, "sh", "-ec", `for n in $(seq 1 60); do
 test "$(systemctl show -p ActiveState --value udm-iptv)" = active &&
 test "$(systemctl show -p SubState --value udm-iptv)" = running &&
 test -z "$(systemctl show -p Job --value udm-iptv)" && exit 0
 sleep 1
done
exit 1`)
	first := h.status(name)
	if first.Service.LoadState != "loaded" || first.Service.ActiveState != "active" || first.Service.SubState != "running" || first.Service.UnitFileState != "enabled" || first.Service.Proxy != "improxy" || first.Service.ProxyPID <= 0 {
		h.t.Fatalf("unhealthy service: %+v", first.Service)
	}
	h.inside(name, "kill", "-0", strconv.Itoa(first.Service.ProxyPID))
	select {
	case <-h.t.Context().Done():
		h.t.Fatal(h.t.Context().Err())
	case <-time.After(6 * time.Second):
	}
	second := h.status(name)
	if second.Service != first.Service {
		h.t.Fatalf("service changed during observation: %+v -> %+v", first.Service, second.Service)
	}
	h.inside(name, "kill", "-0", strconv.Itoa(second.Service.ProxyPID))
}

func (h *firmwareHarness) assertArtifact(name string) {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(h.t.Context(), 30*time.Second)
	defer cancel()
	result, err := h.engine.CopyFromContainer(ctx, name, client.CopyFromContainerOptions{SourcePath: binary})
	if err != nil {
		h.t.Fatalf("read installed artifact: %v", err)
	}
	defer func() {
		if err := result.Content.Close(); err != nil {
			h.t.Error(err)
		}
	}()
	if err := verifyArtifactArchive(result.Content, h.artifact); err != nil {
		h.t.Fatalf("installed artifact in %s: %v", name, err)
	}
}

func capturePaths(output string) (string, string) {
	var textPath, jsonPath string
	for line := range strings.SplitSeq(output, "\n") {
		if value, ok := strings.CutPrefix(line, "Text: "); ok {
			textPath = value
		}
		if value, ok := strings.CutPrefix(line, "Structured JSON Lines: "); ok {
			jsonPath = value
		}
	}

	return textPath, jsonPath
}

func (h *firmwareHarness) assertCaptureCompleted(name, jsonPath string, expected diagnosticExpectation) {
	h.t.Helper()
	lines := strings.Split(strings.TrimSpace(h.inside(name, "cat", jsonPath)), "\n")
	var last diagnostics.Event
	var initial, final bool
	for _, line := range lines {
		if err := json.Unmarshal([]byte(line), &last); err != nil {
			h.t.Fatal(err)
		}
		if last.Type == diagnostics.EventInitial || last.Type == diagnostics.EventFinal {
			h.assertSnapshot(last.Snapshot, expected)
			initial = initial || last.Type == diagnostics.EventInitial
			final = final || last.Type == diagnostics.EventFinal
		}
	}
	if last.Type != diagnostics.EventCompleted || !initial || !final {
		h.t.Fatalf("incomplete capture: last=%s initial=%t final=%t", last.Type, initial, final)
	}
}

func (h *firmwareHarness) assertSnapshot(snapshot *diagnostics.Snapshot, expected diagnosticExpectation) {
	h.t.Helper()
	if snapshot == nil {
		h.t.Fatal("snapshot record has no snapshot")
	}
	if err := checkFirmwareSnapshot(*snapshot, expected); err != nil {
		h.t.Fatal(err)
	}
}

func (h *firmwareHarness) diagnosticExpectation(name string) diagnosticExpectation {
	h.t.Helper()
	raw := h.inside(name, "cat", "/usr/lib/version")
	version := strings.TrimSpace(raw)
	if _, suffix, found := strings.Cut(version, ".v"); found {
		parts := strings.Split(suffix, ".")
		if len(parts) < 3 {
			h.t.Fatalf("unexpected firmware version source: %q", raw)
		}
		version = strings.Join(parts[:3], ".")
	}
	expected := diagnosticExpectation{
		firmware: version, rawVersion: raw,
		kernel:      strings.TrimSpace(h.inside(name, "uname", "-srvm")),
		proxyConfig: h.inside(name, "cat", "/run/udm-iptv/proxy.conf"),
	}
	for _, source := range []struct{ path, key string }{
		{"/etc/board.info", "board.shortname"}, {"/proc/ubnthal/system.info", "shortname"},
	} {
		data, err := h.tryDocker("exec", name, "cat", source.path)
		if err != nil {
			continue
		}
		for line := range strings.SplitSeq(data, "\n") {
			key, value, _ := strings.Cut(line, "=")
			if strings.TrimSpace(key) == source.key {
				expected.board = strings.Trim(strings.TrimSpace(value), `"'`)
			}
		}
		if expected.board != "" {
			break
		}
	}
	return expected
}

func (h *firmwareHarness) assertDiagnosticReport(name string, expected diagnosticExpectation) {
	h.t.Helper()
	var event diagnostics.Event
	if err := json.Unmarshal([]byte(h.inside(name, binary, "diagnose", "--format", "json")), &event); err != nil {
		h.t.Fatal(err)
	}
	if event.Type != "snapshot" {
		h.t.Fatalf("one-shot diagnostic report type = %q", event.Type)
	}
	h.assertSnapshot(event.Snapshot, expected)
	if event.Snapshot.RecentLogs == nil || len(event.Snapshot.RecentLogs.Events) == 0 {
		h.t.Fatal("one-shot diagnostic report lost current-boot service logs")
	}
}

func (h *firmwareHarness) capture(name string) {
	h.t.Helper()
	expected := h.diagnosticExpectation(name)
	h.assertDiagnosticReport(name, expected)
	output := h.inside(name, binary, "diagnose", "--capture", "5s", "--format", "both")
	textPath, jsonPath := capturePaths(output)
	if textPath == "" || jsonPath == "" || !strings.Contains(output, "Expected completion:") {
		h.t.Fatalf("missing capture instructions: %s", output)
	}
	h.inside(name, "sh", "-ec", `for n in $(seq 1 20); do
 grep -q 'Capture completed:' "$1" && exit 0
 grep -Eq 'Capture failed:|Capture timed out:' "$1" && exit 1
 sleep 1
done
exit 1`, "capture", textPath)
	for _, path := range []string{textPath, jsonPath} {
		if mode := strings.TrimSpace(h.inside(name, "stat", "-c", "%a", path)); mode != "600" {
			h.t.Fatalf("capture mode: %s", mode)
		}
	}
	h.assertCaptureCompleted(name, jsonPath, expected)
	if err := checkCaptureText(h.inside(name, "cat", textPath), expected); err != nil {
		h.t.Fatal(err)
	}
	h.retainEvidence(name, "capture-completed")
}

func runEvidenceCommand(ctx context.Context, args []string) ([]byte, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var stdout, stderr bytes.Buffer
	command := exec.CommandContext(ctx, "docker", args...)
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if err != nil {
		err = fmt.Errorf("docker %v: %w", args, err)
	}
	return stdout.Bytes(), stderr.Bytes(), err
}

func (h *firmwareHarness) retainEvidence(name, phase string) {
	h.t.Helper()
	// A failed or cancelled test still needs its output saved. Evidence has
	// its own deadline and never consumes the later container cleanup budget.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	h.evidenceSequence++
	directory := filepath.Join(h.evidence, fmt.Sprintf("%03d-%s-%s", h.evidenceSequence, name, phase))
	if err := collectFirmwareEvidence(ctx, directory, name, runEvidenceCommand); err != nil {
		h.t.Logf("Evidence collection warnings (%s): %v", directory, err)
	}
	h.t.Logf("Firmware evidence: %s", directory)
}

func (h *firmwareHarness) cleanup() {
	// Save success and failure evidence before deleting any containers or
	// volumes. Earlier snapshots also preserve journals across reboots.
	for _, name := range h.containers {
		h.retainEvidence(name, "cleanup")
	}
	// Test contexts are cancelled before Cleanup. Cleanup owns a fresh deadline.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, name := range h.containers {
		if output, err := exec.CommandContext(ctx, "docker", "rm", "-f", name).CombinedOutput(); err != nil {
			h.t.Logf("cleanup %s: %v: %s", name, err, output)
		}
	}
	for _, volume := range []string{h.data, h.overlay} {
		if output, err := exec.CommandContext(ctx, "docker", "volume", "rm", volume).CombinedOutput(); err != nil {
			h.t.Logf("cleanup %s: %v: %s", volume, err, output)
		}
	}
}
