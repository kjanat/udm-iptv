//go:build linux && firmware

package firmware_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

const binary = "/data/udm-iptv/bin/udm-iptv"

type firmwareHarness struct {
	t                                *testing.T
	root, packagePath, data, overlay string
	containers                       []string
	engine                           *client.Client
	artifact                         [sha256.Size]byte
}

// The firmware build tag separates privileged lifecycle tests from unit tests.
func TestFirmwareLifecycle(t *testing.T) {
	if runtime.GOARCH != "arm64" {
		t.Fatal("firmware tests require a native ARM64 runner")
	}
	from, to := os.Getenv("SRC"), os.Getenv("DST")
	if from == "" || to == "" || from == to {
		t.Fatal("two distinct firmware images are required")
	}
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
	id := fmt.Sprintf("udm-iptv-firmware-%d-%d", os.Getpid(), time.Now().UnixNano())
	h.data, h.overlay = id+"-data", id+"-etc"
	t.Cleanup(h.cleanup)
	h.docker("volume", "create", h.data)
	h.docker("volume", "create", h.overlay)
	for _, image := range []string{from, to} {
		if _, err := h.tryDocker("image", "inspect", image); err != nil {
			h.docker("pull", image)
		}
		// Refuse a firmware whose persistence contract differs from the harness.
		h.docker("run", "--rm", "--platform", "linux/arm64", image, "sh", "-ec", `
grep -Fq 'upperdir=${MNT_RWFS}/data' /usr/share/initramfs-tools/scripts/ubnt
if grep -Eq '^etc/systemd/system/?$' /usr/share/initramfs-tools/scripts/ubnt; then exit 1; fi`)
	}
	first := id + "-from"
	t.Log("Install the Debian package on the previous firmware")
	h.boot(first, from, false)
	// Preconfigure a static test network with reporting disabled, using the
	// real CLI. No background harness process supplies fake DHCP readiness.
	h.inside(first, "dpkg-deb", "-x", "/package.deb", "/run/package")
	h.inside(first, "/run/package"+binary, "configure", "--non-interactive", "--profile", "kpn",
		"--wan-interface", "eth8", "--lan-interface", "br0", "--dhcp=false",
		"--static-address", "198.51.100.2/24", "--telemetry=false")
	h.inside(first, "apt-get", "update")
	// Exercise a package-version upgrade without depending on a past Go release.
	h.inside(first, "sh", "-ec", `
dpkg-deb -R /package.deb /run/previous-package
sed -i '/^Version:/s/$/~firmware-test/' /run/previous-package/DEBIAN/control
dpkg-deb -Zxz --root-owner-group -b /run/previous-package /run/previous.deb
apt-get install -y /run/previous.deb`)
	h.healthy(first)
	t.Log("Upgrade to the current Debian package")
	h.inside(first, "apt-get", "install", "-y", "/package.deb")
	h.healthy(first)
	version := strings.TrimSpace(h.inside(first, binary, "version"))
	h.inside(first, "sh", "-ec", `test "$(dpkg-query -W -f='${Version}' udm-iptv)" = "$(dpkg-deb -f /package.deb Version)"`)
	originalConfig := h.readConfig(first)
	h.inside(first, "bash", "-c", "source /etc/bash_completion.d/udm-iptv; complete -p udm-iptv")
	h.capture(first)
	t.Log("A failed service must fail installation")
	h.inside(first, "sh", "-ec", `mkdir -p /run/systemd/system/udm-iptv.service.d
printf '[Service]\nExecStart=\nExecStart=/bin/false\n' > /run/systemd/system/udm-iptv.service.d/failure.conf
systemctl daemon-reload`)
	h.inside(first, "sh", "-ec", `if "$1" install --force --non-interactive > /run/install-failure.log 2>&1; then
 cat /run/install-failure.log
 exit 1
fi
cat /run/install-failure.log
if grep -q 'Automatic startup enabled' /run/install-failure.log; then exit 1; fi`, "failure-check", binary)
	h.inside(first, "rm", "/run/systemd/system/udm-iptv.service.d/failure.conf")
	h.inside(first, "systemctl", "daemon-reload")
	h.inside(first, "systemctl", "reset-failed", "udm-iptv.service")
	h.inside(first, binary, "restart")
	h.healthy(first)

	t.Log("Reboot without reinstalling or manually starting the service")
	h.docker("stop", first)
	h.docker("start", first)
	h.waitBoot(first)
	h.healthy(first)
	h.assertConfig(first, originalConfig)

	t.Log("Swap firmware rootfs offline; erase the firmware proxy")
	h.docker("stop", first)
	second := id + "-to"
	h.boot(second, to, true)
	if got := strings.TrimSpace(h.docker("inspect", "-f", "{{.HostConfig.NetworkMode}}", second)); got != "none" {
		t.Fatalf("firmware recovery has network access: %s", got)
	}
	h.inside(second, "test", "!", "-e", "/usr/share/udm-iptv/go-package")
	h.inside(second, "sh", "-ec", "if command -v improxy; then exit 1; fi")
	h.healthy(second)
	h.assertConfig(second, originalConfig)
	if got := strings.TrimSpace(h.inside(second, binary, "version")); got != version {
		t.Fatalf("version changed: %s -> %s", version, got)
	}
	h.inside(second, "test", "-x", "/usr/local/bin/udm-iptv")
	// Confirm the running process uses the saved runtime, not a substitute.
	state := h.status(second)
	executable := strings.TrimSpace(h.inside(second, "readlink", fmt.Sprintf("/proc/%d/exe", state.Service.ProxyPID)))
	if !strings.HasPrefix(executable, "/data/udm-iptv/runtime/") {
		t.Fatalf("proxy bypassed preserved runtime: %s", executable)
	}
	h.capture(second)

	t.Log("Reboot the replacement firmware offline")
	h.docker("stop", second)
	h.docker("start", second)
	h.waitBoot(second)
	h.healthy(second)
	h.assertConfig(second, originalConfig)

	t.Log("Remove while keeping configuration; reboot must stay removed")
	h.inside(second, binary, "uninstall", "--keep-config")
	h.inside(second, "test", "-f", "/data/udm-iptv/config.json")
	h.docker("stop", second)
	h.docker("start", second)
	h.waitBoot(second)
	h.inside(second, "test", "!", "-e", binary)
	h.inside(second, "test", "!", "-e", "/etc/systemd/system/udm-iptv.service")

	t.Log("Reinstall standalone using the preserved proxy; then purge")
	h.inside(second, "dpkg-deb", "-x", "/package.deb", "/run/package")
	h.inside(second, "/run/package"+binary, "install", "--non-interactive")
	h.healthy(second)
	h.inside(second, binary, "uninstall")
	h.docker("stop", second)
	h.docker("start", second)
	h.waitBoot(second)
	h.inside(second, "test", "!", "-e", "/data/udm-iptv")
	h.inside(second, "test", "!", "-e", "/etc/systemd/system/udm-iptv.service")
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
	output, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()

	return string(output), err
}

func (h *firmwareHarness) inside(name string, args ...string) string {
	h.t.Helper()

	return h.docker(append([]string{"exec", name}, args...)...)
}

func (h *firmwareHarness) boot(name, image string, offline bool) {
	h.t.Helper()
	h.containers = append(h.containers, name)
	network, erase := "bridge", "0"
	if offline {
		network, erase = "none", "1"
	}
	h.docker("run", "-d", "--name", name, "--platform", "linux/arm64", "--privileged", "--cgroupns=host",
		"--network", network, "--stop-signal", "SIGRTMIN+3",
		"-v", "/sys/fs/cgroup:/sys/fs/cgroup:rw",
		"--tmpfs", "/run:exec", "--tmpfs", "/run/lock", "--tmpfs", "/tmp:exec",
		"-v", h.data+":/data", "-v", h.overlay+":/var/lib/udm-iptv-test/etc-overlay",
		"-v", h.packagePath+":/package.deb:ro",
		"-v", filepath.Join(h.root, "tests/firmware/boot.sh")+":/harness.sh:ro",
		"-v", filepath.Join(h.root, "tests/firmware/udm-iptv-test.target")+":/usr/local/lib/systemd/system/udm-iptv-test.target:ro",
		"-e", "DEBIAN_FRONTEND=noninteractive", "-e", "UDM_IPTV_TEST_ERASE_PROXY="+erase,
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
	Service struct {
		LoadState, ActiveState, SubState, UnitFileState, Proxy string
		Restarts                                               uint64
		ProxyPID                                               int
	}
}

func (h *firmwareHarness) status(name string) routerStatus {
	h.t.Helper()
	var state routerStatus
	err := json.Unmarshal([]byte(h.inside(name, binary, "status", "--json")), &state)
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

func (h *firmwareHarness) capture(name string) {
	h.t.Helper()
	output := h.inside(name, binary, "diagnose", "--capture", "5s", "--format", "both")
	var textPath, jsonPath string
	for line := range strings.SplitSeq(output, "\n") {
		if value, ok := strings.CutPrefix(line, "Share-ready text: "); ok {
			textPath = value
		}
		if value, ok := strings.CutPrefix(line, "Structured JSON Lines: "); ok {
			jsonPath = value
		}
	}
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
	lines := strings.Split(strings.TrimSpace(h.inside(name, "cat", jsonPath)), "\n")
	var last struct{ Type string }
	for _, line := range lines {
		err := json.Unmarshal([]byte(line), &last)
		if err != nil {
			h.t.Fatal(err)
		}
	}
	if last.Type != "completed" {
		h.t.Fatalf("incomplete capture: %s", last.Type)
	}
}

func (h *firmwareHarness) cleanup() {
	// Test contexts are cancelled before Cleanup. Cleanup owns a fresh deadline.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, name := range h.containers {
		if h.t.Failed() {
			for _, args := range [][]string{{"logs", name}, {"exec", name, "systemctl", "list-jobs", "--no-pager"}, {"exec", name, "journalctl", "-u", "udm-iptv.service", "-n", "200", "--no-pager"}} {
				output, _ := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
				h.t.Logf("%v\n%s", args, output)
			}
		}
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
