package diagnostics

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"

	"github.com/kjanat/udm-iptv/internal/config"
)

var errLinkQuery = errors.New("netlink query denied")

func TestVLANComparison(t *testing.T) {
	for _, test := range []struct {
		name      string
		parent    string
		id        int
		linkErr   error
		parentErr error
		notVLAN   bool
		status    string
		want      string
	}{
		{name: "matching custom name", parent: "eth4", id: 4, status: "match", want: "tv-custom on eth4, VLAN 4"},
		{name: "different parent", parent: "eth6", id: 4, status: "mismatch", want: "Existing:   tv-custom on eth6, VLAN 4"},
		{name: "different tag", parent: "eth4", id: 6, status: "mismatch", want: "Existing:   tv-custom on eth4, VLAN 6"},
		{name: "absent", linkErr: netlink.LinkNotFoundError{}, status: "missing", want: "normal while the service is stopped"},
		{name: "lookup denied", linkErr: errLinkQuery, status: "unavailable", want: "netlink query denied"},
		{name: "parent unavailable", id: 4, parentErr: errLinkQuery, status: "unavailable", want: "cannot resolve parent link index 8"},
		{name: "name occupied", notVLAN: true, status: "wrong-type", want: "tv-custom is a dummy, not a VLAN"},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := config.DefaultKPN()
			value.WAN.Interface, value.WAN.VLANInterface = "eth4", "tv-custom"
			check := inspectVLAN(value, func(name string) (netlink.Link, error) {
				if name != "tv-custom" {
					t.Fatalf("looked up %q instead of configured name", name)
				}
				if test.notVLAN {
					return &netlink.Dummy{Name: name}, nil
				}
				return &netlink.Vlan{Name: name, ParentIndex: 8, VlanId: test.id}, test.linkErr
			}, func(index int) (netlink.Link, error) {
				if index != 8 {
					t.Fatalf("parent index = %d", index)
				}
				return &netlink.Device{Name: test.parent}, test.parentErr
			})
			if check.Status != test.status {
				t.Fatalf("status = %s, want %s", check.Status, test.status)
			}
			output := RenderSnapshot(Snapshot{Network: networkStatus{VLAN: check}})
			if !strings.Contains(output, test.want) {
				t.Fatalf("missing %q in snapshot:\n%s", test.want, output)
			}
			if test.status == "mismatch" && !strings.Contains(output, "Configured: tv-custom on eth4, VLAN 4") {
				t.Fatal("comparison omitted saved settings")
			}
			assertVLANJSON(t, check)
		})
	}
}

func assertVLANJSON(t *testing.T, check *vlanCheck) {
	t.Helper()
	data, err := json.Marshal(check)
	if err != nil || !strings.Contains(string(data), `"expectedParent":"eth4"`) {
		t.Fatalf("JSON lost comparison evidence: %s (%v)", data, err)
	}
}

func TestUntaggedConnectionSkipsVLANComparison(t *testing.T) {
	value := config.DefaultKPN()
	value.WAN.VLAN = 0
	if check := inspectVLAN(value, nil, nil); check != nil {
		t.Fatalf("untagged connection produced VLAN check: %+v", check)
	}
}
