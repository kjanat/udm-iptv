//go:build linux && firmware

package firmware_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/filemode"
)

const (
	legacyPackageURL = "https://github.com/kjanat/udm-iptv/releases/download/v4.3.2/udm-iptv_4.3.2_all.deb"
	legacyPackageSHA = "5f8e8fee4602dcc37c8568787408ea530ab05bd7f061d75ca6da8c155a9986e6"
	legacyPackageMax = 1 << 20
)

// Unlike the package-version upgrade test, this installs the published shell
// implementation. Its running proxy, unmarked VLAN and unmarked NAT rules must
// all cross the actual dpkg maintainer-script boundary into the Go service.
func TestFirmwareLegacyPackageUpgrade(t *testing.T) {
	from, _ := firmwareImages(t)
	packagePath := downloadLegacyPackage(t)
	h := newFirmwareHarness(t)
	h.requirePersistenceContract(from)
	name := h.id + "-v4-upgrade"
	h.bootWith(name, from, "bridge", false, true)
	h.docker("cp", packagePath, name+":/root/legacy.deb")
	h.installLegacyPackage(name)
	h.requireLegacyService(name)
	h.retainEvidence(name, "legacy-before-upgrade")
	// Import keeps the real legacy defaults, including telemetry. Disconnect
	// external networking rather than precreating JSON or changing reporting
	// preferences; the provider-side DHCP veth remains available.
	h.docker("network", "disconnect", "bridge", name)
	h.inside(name, "sh", "-ec", `test -z "$(ip route show default)"`)

	// No manual stop, deletion or alias assignment may prepare the migration.
	output := h.inside(name, "/usr/bin/udm-iptv", "upgrade", "--package", "/package.deb")
	if !strings.Contains(output, "Installation successful") || strings.Contains(output, "debconf:") {
		t.Fatalf("legacy upgrade did not complete cleanly:\n%s", output)
	}
	h.assertPackageConfigured(name)
	h.healthy(name)
	h.assertLegacyMigration(name)
	saved := h.readConfig(name)
	h.reboot(name)
	h.inside(name, "sh", "-ec", `test -z "$(ip route show default)"`)
	h.healthy(name)
	h.assertConfig(name, saved)
	h.assertLegacyMigration(name)
}

func downloadLegacyPackage(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, legacyPackageURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			t.Error(err)
		}
	}()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("download v4.3.2 package: %s", response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, legacyPackageMax+1))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > legacyPackageMax || fmt.Sprintf("%x", sha256.Sum256(data)) != legacyPackageSHA {
		t.Fatalf("published v4.3.2 package does not match pinned SHA256 %s", legacyPackageSHA)
	}
	path := filepath.Join(t.TempDir(), "udm-iptv_4.3.2_all.deb")
	if err := atomicfile.Write(path, data, filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	return path
}

func (h *firmwareHarness) installLegacyPackage(name string) {
	h.t.Helper()
	// Hardware-dependent debconf defaults differ across images. Install the
	// unchanged package with startup masked, then configure the fixture's ports
	// as an operator would before starting the real legacy unit.
	h.inside(name, "systemctl", "mask", "--runtime", "udm-iptv.service")
	h.inside(name, "sh", "-ec", `printf '%s\n' \
 'udm-iptv udm-iptv/profile select kpn' \
 'udm-iptv udm-iptv/igmpproxy-program select improxy' | debconf-set-selections`)
	h.inside(name, "apt-get", "update")
	h.inside(name, "apt-get", "install", "-y", "dialog")
	h.aptInstall(name, "/root/legacy.deb")
	h.inside(name, "sh", "-ec", `test "$(dpkg-query -W -f='${Version}' udm-iptv)" = 4.3.2
test -x /usr/lib/udm-iptv/udm-iptvd
test ! -e /usr/share/udm-iptv/go-package
test ! -e /data/udm-iptv/config.json
cat > /etc/udm-iptv.conf <<'EOF'
IPTV_WAN_INTERFACE="eth8"
IPTV_WAN_VLAN="4"
IPTV_WAN_VLAN_INTERFACE="iptv"
IPTV_WAN_VLAN_MAC="02:00:00:00:04:32"
IPTV_WAN_DHCP="true"
IPTV_WAN_DHCP_OPTIONS="-O staticroutes -V IPTV_RG"
IPTV_WAN_RANGES="213.75.0.0/16 217.166.0.0/16 195.121.0.0/16"
IPTV_LAN_INTERFACES="br0"
IPTV_IGMPPROXY_PROGRAM="improxy"
IPTV_IGMPPROXY_IGMP_VERSION="2"
IPTV_IGMPPROXY_DISABLE_QUICKLEAVE="false"
IPTV_IGMPPROXY_DEBUG="true"
EOF
chmod 0600 /etc/udm-iptv.conf
printf 'Acquire::Retries "0";\nAcquire::http::Timeout "2";\nAcquire::https::Timeout "2";\n' > /etc/apt/apt.conf.d/99-legacy-fixture-offline
systemctl unmask --runtime udm-iptv.service
systemctl daemon-reload
systemctl enable --now udm-iptv.service`)
}

func (h *firmwareHarness) requireLegacyService(name string) {
	h.t.Helper()
	first := h.inside(name, "sh", "-ec", `for n in $(seq 1 45); do
 pid=$(systemctl show -p MainPID --value udm-iptv.service)
 if systemctl is-active --quiet udm-iptv.service &&
    test "$(readlink /proc/"${pid}"/exe)" = "$(readlink -f "$(command -v improxy)")" &&
    ip -4 address show dev iptv | grep -q 'inet 10\.207\.64\.'; then
  printf '%s' "${pid}"
  exit 0
 fi
 sleep 1
done
exit 1`)
	pid, err := strconv.Atoi(strings.TrimSpace(first))
	if err != nil || pid <= 0 {
		h.t.Fatalf("legacy proxy PID: %q, %v", first, err)
	}
	h.inside(name, "sh", "-ec", `test -z "$(cat /sys/class/net/iptv/ifalias)"
ip -d link show iptv | grep -q 'vlan protocol 802.1Q id 4'
grep -qx 'igmp enable version 2' /run/igmpproxy.iptv.conf
grep -qx 'quickleave enable' /run/igmpproxy.iptv.conf`)
	for _, destination := range kpnDestinations {
		h.inside(name, "iptables", "-t", "nat", "-C", "POSTROUTING", "-d", destination, "-o", "iptv", "-j", "MASQUERADE")
	}
	select {
	case <-h.t.Context().Done():
		h.t.Fatal(h.t.Context().Err())
	case <-time.After(6 * time.Second):
	}
	if next := strings.TrimSpace(h.inside(name, "systemctl", "show", "-p", "MainPID", "--value", "udm-iptv.service")); next != strconv.Itoa(pid) {
		h.t.Fatalf("legacy proxy changed during observation: %d -> %s", pid, next)
	}
	h.inside(name, "kill", "-0", strconv.Itoa(pid))
}

func (h *firmwareHarness) assertLegacyMigration(name string) {
	h.t.Helper()
	h.assertSettings(name, map[string]string{
		"wan-interface": "eth8", "wan-vlan": "4", "iptv-interface": "iptv",
		"vlan-mac": "02:00:00:00:04:32", "dhcp": "true", "lan-interface": "br0",
		"proxy": "improxy", "igmp-version": "2", "quickleave": "true", "debug": "true",
	})
	h.assertLease(name)
	h.assertPackageRecord(name, strings.TrimSpace(h.inside(name, binary, "version")))
	h.inside(name, "sh", "-ec", `test "$(cat /sys/class/net/iptv/ifalias)" = udm-iptv
test "$(cat /sys/class/net/iptv/address)" = 02:00:00:00:04:32
grep -qx 'igmp enable version 2' /run/udm-iptv/proxy.conf
grep -qx 'quickleave enable' /run/udm-iptv/proxy.conf
grep -qx 'upstream iptv' /run/udm-iptv/proxy.conf
grep -qx 'downstream br0' /run/udm-iptv/proxy.conf
test ! -e /run/igmpproxy.iptv.conf
test ! -e /data/udm-iptv/legacy-network.pending
test ! -e /etc/systemd/system/udm-iptv-restore.service`)
	for _, destination := range kpnDestinations {
		h.inside(name, "sh", "-ec", `if iptables -t nat -C POSTROUTING -d "$1" -o iptv -j MASQUERADE 2>/dev/null; then exit 1; fi`, "legacy-nat", destination)
	}
}
