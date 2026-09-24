package network

import (
	"path/filepath"
	"testing"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/config"
)

func mldConfig(version int) config.Config {
	value := config.Config{Proxy: config.Proxy{MLDVersion: version}}
	value.WAN.Interface = "eth8"
	value.WAN.VLAN = 4
	value.WAN.VLANInterface = "iptv"
	value.LAN.Interfaces = []string{"br0", "br10"}

	return value
}

func TestMLDOffTouchesNoKnob(t *testing.T) {
	restore, err := EnableIPv6Multicast(mldConfig(0))
	if err != nil {
		t.Fatalf("EnableIPv6Multicast: %v", err)
	}
	if err := restore(); err != nil {
		t.Fatalf("restore: %v", err)
	}
}

func TestIPv6KnobsCoverTheIPTVLaneAndEveryLAN(t *testing.T) {
	knobs := ipv6Knobs(mldConfig(2))
	want := map[string]string{
		"/proc/sys/net/ipv6/conf/iptv/forwarding": "1",
		"/proc/sys/net/ipv6/conf/iptv/accept_ra":  "2",
		"/proc/sys/net/ipv6/conf/br0/forwarding":  "1",
		"/proc/sys/net/ipv6/conf/br10/forwarding": "1",
	}
	if len(knobs) != len(want) {
		t.Fatalf("got %d knobs, want %d: %v", len(knobs), len(want), knobs)
	}
	for path, value := range want {
		if knobs[path] != value {
			t.Errorf("%s = %q, want %q", path, knobs[path], value)
		}
	}
}

func TestUntaggedIPTVLaneUsesTheWANPort(t *testing.T) {
	value := mldConfig(1)
	value.WAN.VLAN = 0
	knobs := ipv6Knobs(value)
	if _, ok := knobs["/proc/sys/net/ipv6/conf/eth8/accept_ra"]; !ok {
		t.Errorf("untagged IPTV wants accept_ra on the WAN port: %v", knobs)
	}
}

func TestSysctlReportsThePreviousValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forwarding")
	if err := atomicfile.Write(path, []byte("0\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	was, err := setSysctl(path, "1")
	if err != nil {
		t.Fatalf("setSysctl: %v", err)
	}
	if was != "0" {
		t.Errorf("previous value %q, want %q", was, "0")
	}
	now, err := readSysctl(path)
	if err != nil {
		t.Fatalf("readSysctl: %v", err)
	}
	if now != "1" {
		t.Errorf("value after write %q, want %q", now, "1")
	}
	if err := writeSysctl(path, was); err != nil {
		t.Fatalf("writeSysctl: %v", err)
	}
	if back, _ := readSysctl(path); back != "0" {
		t.Errorf("value after restore %q, want %q", back, "0")
	}
}

func TestMissingKnobIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent")
	if _, err := setSysctl(path, "1"); err == nil {
		t.Error("a knob that does not exist must not pass silently")
	}
}

func TestEveryKnobPathSitsUnderTheIPv6Tree(t *testing.T) {
	for path := range ipv6Knobs(mldConfig(2)) {
		if root := filepath.Dir(filepath.Dir(path)); root != sysctlRoot {
			t.Errorf("%s sits under %s, want %s", path, root, sysctlRoot)
		}
	}
}
