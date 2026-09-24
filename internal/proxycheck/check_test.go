package proxycheck

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
)

func TestNativeState(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, state, want string
	}{
		{"enabled without running process", `{"services":{"igmpProxy":{"enabled":true}}}`, "native IGMP Proxy is enabled"},
		{"disabled", `{"services":{"igmpProxy":{"enabled":false}}}`, ""},
		{"absent", `{"services":{}}`, ""},
		{"null", `{"services":{"igmpProxy":null}}`, ""},
		{"snooping only", `{"services":{"igmpSnooping":{"enabled":true}}}`, ""},
		{"truncated state", `{"services":`, "decode UniFi"},
		{"wrong enabled type", `{"services":{"igmpProxy":{"enabled":"true"}}}`, "decode UniFi"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := fixtureRoot(t)
			writeFixture(t, root, statePath, test.state)
			assertCheck(t, root, test.want)
		})
	}
}

func TestProcesses(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, comm, namespace, cgroup, want string
	}{
		{"native watchdog", "improxy", "net:[1]", "1:name=systemd:/os.slice/udapi-server.service\n", "PID 42"},
		{"foreign igmpproxy", "igmpproxy", "net:[1]", "0::/system.slice/igmpproxy.service\n", "another multicast proxy"},
		{"v4 managed proxy", "igmpproxy", "net:[1]", "1:name=systemd:/system.slice/udm-iptv.service\n", ""},
		{"v5 managed proxy", "improxy", "net:[1]", "0::/system.slice/udm-iptv.service\n", ""},
		{"our service subgroup", "improxy", "net:[1]", "0::/system.slice/udm-iptv.service/proxy\n", ""},
		{"similarly named service", "improxy", "net:[1]", "0::/system.slice/not-udm-iptv.service\n", "another multicast proxy"},
		{"nested container service sharing network", "improxy", "net:[1]", "0::/docker/example/system.slice/udm-iptv.service\n", "another multicast proxy"},
		{"unmanaged proxy", "improxy", "net:[1]", "0::/user.slice/session.scope\n", "another multicast proxy"},
		{"container proxy", "improxy", "net:[2]", "0::/docker/example\n", ""},
		{"unrelated daemon", "ubios-udapi-ser", "net:[1]", "0::/os.slice/udapi-server.service\n", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := fixtureRoot(t)
			writeFixture(t, root, "proc/42/comm", test.comm+"\n")
			writeFixture(t, root, "proc/42/cgroup", test.cgroup)
			linkFixture(t, root, "proc/42/ns/net", test.namespace)
			assertCheck(t, root, test.want)
		})
	}
}

func TestExitedProcessAndCancellation(t *testing.T) {
	t.Parallel()
	root := fixtureRoot(t)
	// A PID can disappear between directory enumeration and reading comm.
	if err := os.Mkdir(filepath.Join(root, "proc/42"), 0o700); err != nil {
		t.Fatal(err)
	}
	assertCheck(t, root, "")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := check(ctx, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestReadFailuresAreNotReportedAsAvailable(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, path, want string }{
		{"native state", statePath, "read UniFi"},
		{"process ownership", "proc/42/cgroup", "service ownership"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := fixtureRoot(t)
			writeFixture(t, root, "proc/42/comm", "improxy\n")
			linkFixture(t, root, "proc/42/ns/net", "net:[1]")
			// A directory at a file path produces a read error, even as root.
			if err := os.MkdirAll(filepath.Join(root, test.path), 0o700); err != nil {
				t.Fatal(err)
			}
			assertCheck(t, root, test.want)
		})
	}
}

func fixtureRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	linkFixture(t, root, "proc/self/ns/net", "net:[1]")
	return root
}

func writeFixture(t *testing.T, root, path, data string) {
	t.Helper()
	full := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := atomicfile.Write(full, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func linkFixture(t *testing.T, root, path, target string) {
	t.Helper()
	full := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, full); err != nil {
		t.Fatal(err)
	}
}

func assertCheck(t *testing.T, root, want string) {
	t.Helper()
	err := check(t.Context(), root)
	if want == "" {
		if err != nil {
			t.Fatal(err)
		}
	} else if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("got %v, want %q", err, want)
	}
}
