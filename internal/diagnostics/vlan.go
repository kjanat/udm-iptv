package diagnostics

import (
	"errors"
	"fmt"

	"github.com/vishvananda/netlink"

	"github.com/kjanat/udm-iptv/internal/config"
)

type vlanCheck struct {
	Status         string `json:"status"`
	Name           string `json:"name"`
	ExpectedParent string `json:"expectedParent"`
	ExpectedID     int    `json:"expectedID"`
	Parent         string `json:"parent,omitempty"`
	ParentIndex    int    `json:"parentIndex,omitempty"`
	ID             int    `json:"id,omitempty"`
	LinkType       string `json:"linkType,omitempty"`
	Detail         string `json:"detail,omitempty"`
}

// inspectVLAN compares saved intent with native state, without assuming a
// particular interface name or changing links that may belong to another service.
func inspectVLAN(value config.Config, byName func(string) (netlink.Link, error), byIndex func(int) (netlink.Link, error)) *vlanCheck {
	if value.WAN.VLAN == 0 {
		return nil
	}
	check := &vlanCheck{
		Name: value.WAN.VLANInterface, ExpectedParent: value.WAN.Interface,
		ExpectedID: value.WAN.VLAN, Status: "unavailable",
	}
	link, err := byName(check.Name)
	if err != nil {
		if _, missing := errors.AsType[netlink.LinkNotFoundError](err); missing {
			check.Status = "missing"
		} else {
			check.Detail = err.Error()
		}
		return check
	}
	check.LinkType = link.Type()
	vlan, ok := link.(*netlink.Vlan)
	if !ok {
		check.Status = "wrong-type"
		return check
	}
	check.ID, check.ParentIndex = vlan.VlanId, vlan.Attrs().ParentIndex
	parent, err := byIndex(check.ParentIndex)
	if err != nil {
		check.Detail = fmt.Sprintf("cannot resolve parent link index %d: %v", check.ParentIndex, err)
		return check
	}
	check.Parent = parent.Attrs().Name
	check.Status = "match"
	if check.Parent != check.ExpectedParent || check.ID != check.ExpectedID {
		check.Status = "mismatch"
	}
	return check
}

func (r reportRenderer) vlan(check *vlanCheck, active string) string {
	if check == nil {
		return ""
	}
	expected := fmt.Sprintf("  Configured: %s on %s, VLAN %d\n", check.Name, check.ExpectedParent, check.ExpectedID)
	switch check.Status {
	case "match":
		return fmt.Sprintf("%s: %s on %s, VLAN %d (DHCP and playback not verified).\n\n", r.text(ReportGood, "VLAN configuration matches"), check.Name, check.Parent, check.ID)
	case "mismatch":
		return r.text(ReportBad, "VLAN configuration mismatch") + ":\n" +
			fmt.Sprintf("  Existing:   %s on %s, VLAN %d\n", check.Name, check.Parent, check.ID) + expected +
			"Confirm which physical port carries IPTV from your provider. Then use 'udm-iptv configure' to correct the WAN port or VLAN settings if needed. Do not delete an interface owned by another service.\n\n"
	case "missing":
		role := ReportPlain
		if active == "active" || active == "failed" {
			role = ReportBad
		}
		return r.text(role, "Configured IPTV VLAN interface is absent") + ":\n" + expected +
			"This can be normal while the service is stopped. If startup fails, check its logs for interface creation errors.\n\n"
	case "wrong-type":
		return fmt.Sprintf("%s: %s is a %s, not a VLAN.\n", r.text(ReportBad, "IPTV interface name conflict"), check.Name, check.LinkType) + expected +
			"Use 'udm-iptv configure' to choose an unused VLAN interface name. Leave the existing interface intact.\n\n"
	default:
		return r.text(ReportWarning, "VLAN comparison unavailable") + ":\n" + expected + "  " + check.Detail + "\nNo match or mismatch could be established.\n\n"
	}
}
