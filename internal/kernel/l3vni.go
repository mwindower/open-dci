package kernel

import (
	"errors"
	"fmt"
	"net"
	"net/netip"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// OwnerAlias marks the devices open-dci provisioned itself (as the interface
// alias, "ip link set X alias ..."). Only devices carrying it are ever
// modified or deleted; everything else belongs to the base system.
const OwnerAlias = "open-dci"

// L3VNIBridgePrefix starts the name of every provisioned L3VNI's bridge
// (config.Network.BridgeName): tenant packets arrive on these devices.
const L3VNIBridgePrefix = "dcibr"

// L3VNI is a tenant VRF provisioned as an EVPN L3VNI in the classic layout
// FRR's zebra understands without VLANs: VRF <- bridge (acts as the SVI) <-
// VXLAN device.
type L3VNI struct {
	VRF           string
	Table         uint32
	VNI           uint32
	Bridge, Vxlan string
	VTEP          net.IP
	MTU           int
}

// EnsureL3VNI creates the L3VNI's devices or brings them back to the desired
// state. It refuses to take over a VRF or device it did not create.
func EnsureL3VNI(v L3VNI) error {
	vrf, err := ensureOwned(&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: v.VRF}, Table: v.Table}, 0, func(l netlink.Link) error {
		if got := l.(*netlink.Vrf).Table; got != v.Table {
			return fmt.Errorf("vrf %s uses table %d, want %d (delete it to change the table)", v.VRF, got, v.Table)
		}
		return nil
	})
	if err != nil {
		return err
	}
	br, err := ensureOwned(&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: v.Bridge}}, 0, nil)
	if err != nil {
		return err
	}
	if err := EnsureMaster(br, vrf.Attrs().Name); err != nil {
		return err
	}
	vx := &netlink.Vxlan{
		LinkAttrs: netlink.LinkAttrs{Name: v.Vxlan},
		VxlanId:   int(v.VNI),
		SrcAddr:   v.VTEP,
		Port:      4789,
		Learning:  false,
	}
	// a VXLAN device's VNI and source address can't be changed: recreate it
	if l, err := netlink.LinkByName(v.Vxlan); err == nil && l.Attrs().Alias == OwnerAlias {
		if old, ok := l.(*netlink.Vxlan); !ok || old.VxlanId != vx.VxlanId || !old.SrcAddr.Equal(vx.SrcAddr) {
			if err := netlink.LinkDel(l); err != nil {
				return fmt.Errorf("%s: recreate: %w", v.Vxlan, err)
			}
		}
	}
	vxl, err := ensureOwned(vx, v.MTU, nil)
	if err != nil {
		return err
	}
	if err := EnsureMaster(vxl, v.Bridge); err != nil {
		return err
	}
	// the bridge's MTU follows its ports only as long as it was never set
	if err := SetMTU(br, v.MTU); err != nil {
		return err
	}
	return EnsureUnreachableDefault(v.Table)
}

// ensureOwned creates l (tagged with OwnerAlias) unless it exists. An
// existing device must carry the tag and have the same type; check verifies
// further attributes. It returns the kernel's view of the device, up.
func ensureOwned(l netlink.Link, mtu int, check func(netlink.Link) error) (netlink.Link, error) {
	name := l.Attrs().Name
	got, err := netlink.LinkByName(name)
	var nf netlink.LinkNotFoundError
	switch {
	case errors.As(err, &nf):
		if err := EnsureLink(l, mtu); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if err := netlink.LinkSetAlias(l, OwnerAlias); err != nil {
			return nil, fmt.Errorf("%s: alias: %w", name, err)
		}
		return netlink.LinkByName(name)
	case err != nil:
		return nil, err
	case got.Attrs().Alias != OwnerAlias:
		return nil, fmt.Errorf("%s exists but was not created by open-dci (alias %q); open-dci only manages devices it created", name, got.Attrs().Alias)
	case got.Type() != l.Type():
		return nil, fmt.Errorf("%s is a %s, want %s", name, got.Type(), l.Type())
	}
	if check != nil {
		if err := check(got); err != nil {
			return nil, err
		}
	}
	if err := SetMTU(got, mtu); err != nil {
		return nil, err
	}
	return got, netlink.LinkSetUp(got)
}

// RemoveL3VNIs deletes the devices open-dci provisioned that are not in keep
// (device names). It returns the deleted VRFs, whose FRR VRF can be removed
// afterwards. VXLAN devices go first, VRFs last.
func RemoveL3VNIs(keep map[string]bool) (vrfs []string, err error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, err
	}
	for _, typ := range []string{"vxlan", "bridge", "vrf"} {
		for _, l := range links {
			a := l.Attrs()
			if a.Alias != OwnerAlias || l.Type() != typ || keep[a.Name] {
				continue
			}
			if err := netlink.LinkDel(l); err != nil && !errors.Is(err, unix.ENODEV) {
				return vrfs, fmt.Errorf("delete %s: %w", a.Name, err)
			}
			if typ == "vrf" {
				vrfs = append(vrfs, a.Name)
			}
		}
	}
	return vrfs, nil
}

// L3VNIUp reports whether the L3VNI's devices exist, are owned and up.
func L3VNIUp(v L3VNI) bool {
	for _, n := range []string{v.VRF, v.Bridge, v.Vxlan} {
		l, err := netlink.LinkByName(n)
		if err != nil || l.Attrs().Alias != OwnerAlias || l.Attrs().Flags&net.FlagUp == 0 {
			return false
		}
	}
	return true
}

// UnreachableMetric is the metric of the catch-all unreachable routes in VRF
// tables (the value ifupdown2 uses): anything else in the table wins.
const UnreachableMetric = 4278198272

// EnsureUnreachableDefault adds "unreachable default" (IPv4 and IPv6) with
// UnreachableMetric to a VRF's table. Without it, a lookup that finds nothing
// in the VRF falls through to the main table, where e.g. the End.DT46 SIDs of
// other tenants live.
func EnsureUnreachableDefault(table uint32) error {
	for _, dst := range []string{"0.0.0.0/0", "::/0"} {
		_, n, _ := net.ParseCIDR(dst)
		r := &netlink.Route{Table: int(table), Dst: n, Type: unix.RTN_UNREACHABLE, Priority: UnreachableMetric}
		if err := netlink.RouteReplace(r); err != nil {
			return fmt.Errorf("table %d: unreachable %s: %w", table, dst, err)
		}
	}
	return nil
}

// EnsureThrow adds "throw <prefix>" to a VRF's table: lookups for it end in
// the table and continue with the next policy rule (the main table). The
// gateway's SRv6 encapsulation needs this: the kernel routes the outer packet
// (to a remote SID) in the tenant VRF's table.
func EnsureThrow(table uint32, p netip.Prefix) error {
	_, n, _ := net.ParseCIDR(p.String())
	r := &netlink.Route{Table: int(table), Dst: n, Type: unix.RTN_THROW}
	if err := netlink.RouteReplace(r); err != nil {
		return fmt.Errorf("table %d: throw %s: %w", table, p, err)
	}
	return nil
}
