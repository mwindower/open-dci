package kernel

import (
	"fmt"

	"github.com/vishvananda/netlink"
)

// VNIPath is the device chain that carries one EVPN L3VNI of the base config in
// the VLAN-aware bridge layout (as e.g. metal-networker sets it up): VRF <- SVI
// (vlan device on the VLAN-aware bridge) <- bridge <- vxlan port with the SVI's
// VLAN as PVID.
type VNIPath struct {
	VRF    string
	SVI    netlink.Link
	Bridge netlink.Link
	Vxlan  netlink.Link
}

// Links returns the devices whose MTU limits routed traffic of the VNI,
// in the order they must be raised (bridge before its upper SVI).
func (p VNIPath) Links() []netlink.Link { return []netlink.Link{p.Bridge, p.Vxlan, p.SVI} }

// FindVNIPath discovers the device chain of the L3VNI bound to vrf. It
// derives everything from the kernel state instead of relying on names.
func FindVNIPath(vrf string) (*VNIPath, error) {
	v, err := netlink.LinkByName(vrf)
	if err != nil {
		return nil, fmt.Errorf("vrf %s: %w", vrf, err)
	}
	links, err := netlink.LinkList()
	if err != nil {
		return nil, err
	}
	p := &VNIPath{VRF: vrf}
	var vid int
	for _, l := range links {
		vl, ok := l.(*netlink.Vlan)
		if !ok || l.Attrs().MasterIndex != v.Attrs().Index {
			continue
		}
		parent, err := netlink.LinkByIndex(l.Attrs().ParentIndex)
		if err != nil || parent.Type() != "bridge" {
			continue
		}
		p.SVI, p.Bridge, vid = l, parent, vl.VlanId
		break
	}
	if p.SVI == nil {
		return nil, fmt.Errorf("vrf %s: no SVI (vlan device on a bridge) found", vrf)
	}
	vlans, err := netlink.BridgeVlanList()
	if err != nil {
		return nil, err
	}
	for _, l := range links {
		if l.Type() != "vxlan" || l.Attrs().MasterIndex != p.Bridge.Attrs().Index {
			continue
		}
		for _, info := range vlans[int32(l.Attrs().Index)] {
			if int(info.Vid) == vid && info.PortVID() {
				p.Vxlan = l
			}
		}
	}
	if p.Vxlan == nil {
		return nil, fmt.Errorf("vrf %s: no vxlan port with PVID %d on %s", vrf, vid, p.Bridge.Attrs().Name)
	}
	return p, nil
}

// MinMTU returns the smallest MTU along the path.
func (p VNIPath) MinMTU() int {
	m := 0
	for _, l := range p.Links() {
		if m == 0 || l.Attrs().MTU < m {
			m = l.Attrs().MTU
		}
	}
	return m
}

// RaiseMTU raises every device of the path to at least mtu (it never lowers).
func (p VNIPath) RaiseMTU(mtu int) error {
	for _, l := range p.Links() {
		if l.Attrs().MTU >= mtu {
			continue
		}
		if err := SetMTU(l, mtu); err != nil {
			return err
		}
	}
	return nil
}
