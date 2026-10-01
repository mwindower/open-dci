package kernel

import (
	"net"
	"net/netip"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// EncapSIDs returns the SIDs the routes of a (tenant VRF) table encapsulate
// to: the first segment of every seg6 encap route, without duplicates.
func EncapSIDs(table uint32) ([]netip.Addr, error) {
	seen := map[netip.Addr]bool{}
	var out []netip.Addr
	for _, family := range []int{netlink.FAMILY_V4, netlink.FAMILY_V6} {
		routes, err := netlink.RouteListFiltered(family, &netlink.Route{Table: int(table)}, netlink.RT_FILTER_TABLE)
		if err != nil {
			return nil, err
		}
		for _, r := range routes {
			enc, ok := r.Encap.(*netlink.SEG6Encap)
			if !ok || len(enc.Segments) == 0 {
				continue
			}
			if a, ok := netip.AddrFromSlice(enc.Segments[0]); ok && !seen[a] {
				seen[a] = true
				out = append(out, a)
			}
		}
	}
	return out, nil
}

// LocalSIDInstalled reports whether the main table has an seg6local route
// (e.g. End.DT46) for the SID.
func LocalSIDInstalled(sid netip.Addr) bool {
	dst := &net.IPNet{IP: sid.AsSlice(), Mask: net.CIDRMask(128, 128)}
	routes, err := netlink.RouteListFiltered(netlink.FAMILY_V6, &netlink.Route{Table: unix.RT_TABLE_MAIN, Dst: dst}, netlink.RT_FILTER_TABLE|netlink.RT_FILTER_DST)
	if err != nil {
		return false
	}
	for _, r := range routes {
		if _, ok := r.Encap.(*netlink.SEG6LocalEncap); ok {
			return true
		}
	}
	return false
}
