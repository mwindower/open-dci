package gateway

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/mwindower/open-dci/internal/config"
)

// Routes lists a network's prefixes (status --routes): what the tenant VRF
// holds from the own partition and from the remote ones, and the EVPN type-5
// routes of its L3VNI.
type Routes struct {
	// Local: prefixes from the own partition (EVPN) and the own aggregates
	Local []LocalRoute
	// Remote: prefixes imported from other partitions via the VPN
	Remote []RemoteRoute
	// EVPN: the L3VNI's type-5 routes, received from the fabric or advertised
	// into it by this gateway
	EVPN []EVPNRoute
}

type LocalRoute struct {
	Prefix  string
	NextHop string `json:",omitempty"` // the VTEP; "" for the gateway's own routes
	ASPath  string `json:",omitempty"`
	// Aggregate: one of networks[].aggregates, announced by this gateway
	Aggregate bool `json:",omitempty"`
	// Exported: in the VPN under the network's RD
	Exported bool
	// Installed: zebra installed a route for the prefix in the kernel
	Installed  bool
	NoBestPath bool `json:",omitempty"`
}

type RemoteRoute struct {
	Prefix string
	// SIDs the kernel route encapsulates to (from zebra)
	SIDs   []string
	ASPath string
	// Paths: VPN paths to the prefix (via both exits, from both gateways of
	// the remote pair)
	Paths     int
	Installed bool
}

type EVPNRoute struct {
	// Advertised by this gateway (the L3VNI's RD); received from the fabric otherwise
	Advertised bool
	RD         string
	Prefix     string
	NextHop    string // the VTEP
	ASPath     string `json:",omitempty"`
	RTs        []string
	Paths      int
	// NoBestPath: received, but no path is valid and selected
	NoBestPath bool `json:",omitempty"`
}

// vrfTable is a VRF's BGP table: prefix → paths.
type vrfTable map[string][]vrfPath

// zebraRoute is an entry of zebra's RIB ("show ip route vrf X json").
type zebraRoute struct {
	Protocol  string `json:"protocol"`
	Installed bool   `json:"installed"`
	Nexthops  []struct {
		Seg6 struct {
			Segs string `json:"segs"`
		} `json:"seg6"`
	} `json:"nexthops"`
}

// evpnPath is a path of a type-5 route in the global EVPN table.
type evpnPath struct {
	Valid     bool   `json:"valid"`
	Best      bool   `json:"bestpath"`
	IP        string `json:"ip"`
	IPLen     int    `json:"ipLen"`
	ASPath    string `json:"path"`
	ExtCommty struct {
		String string `json:"string"`
	} `json:"extendedCommunity"`
	Nexthops []struct {
		IP string `json:"ip"`
	} `json:"nexthops"`
}

// evpnTable is the global EVPN table of type-5 routes: RD → prefix → paths.
type evpnTable map[string]map[string][][]evpnPath

// l3vniInfo is what bgpd knows about a VRF's L3VNI.
type l3vniInfo struct {
	RD        string   `json:"rd"`
	ImportRTs []string `json:"import-rts"`
}

// SelectNetworks returns the networks a status --routes argument names:
// "all", a VRF name or a VNI.
func SelectNetworks(c *config.Config, sel string) ([]config.Network, error) {
	if sel == "" {
		return nil, nil
	}
	if sel == "all" {
		return c.Networks, nil
	}
	for _, n := range c.Networks {
		if n.VRF == sel || strconv.FormatUint(uint64(n.VNI), 10) == sel {
			return []config.Network{n}, nil
		}
	}
	return nil, fmt.Errorf("no network with VRF or VNI %q", sel)
}

// routes collects a network's routes from FRR. bgp holds the VRF's BGP
// tables per address family ("ipv4", "ipv6"), evpn the global type-5 table.
func (g *Gateway) routes(n config.Network, rd string, bgp map[string]vrfTable, evpn evpnTable) *Routes {
	exported := map[string]bool{}
	zebra := map[string][]zebraRoute{}
	for _, af := range []string{"ipv4", "ipv6"} {
		var vpn struct {
			Routes struct {
				RDs map[string]vrfTable `json:"routeDistinguishers"`
			} `json:"routes"`
		}
		if g.FRR.ShowJSON(fmt.Sprintf("show bgp %s vpn rd %s", af, rd), &vpn) == nil {
			for p := range vpn.Routes.RDs[rd] {
				exported[p] = true
			}
		}
		cmd := "show ip route vrf " + n.VRF
		if af == "ipv6" {
			cmd = "show ipv6 route vrf " + n.VRF
		}
		var z map[string][]zebraRoute
		if g.FRR.ShowJSON(cmd, &z) == nil {
			for p, r := range z {
				zebra[p] = r
			}
		}
	}
	var info l3vniInfo
	_ = g.FRR.ShowJSON("show bgp vrf "+n.VRF+" vni", &info)
	if info.RD == "" {
		info.RD = "-" // matches no RD: nothing counts as advertised
	}

	v4, v6 := n.AggregatePrefixes()
	aggregates := map[string]bool{}
	for _, p := range append(v4, v6...) {
		aggregates[p.String()] = true
	}

	r := &Routes{}
	for _, af := range []string{"ipv4", "ipv6"} {
		l, rem := splitRoutes(bgp[af], aggregates, exported, zebra)
		r.Local, r.Remote = append(r.Local, l...), append(r.Remote, rem...)
	}
	r.EVPN = evpnRoutes(evpn, info, n.VNI)
	sortRoutes(r)
	return r
}

// splitRoutes sorts a VRF table's prefixes into local and remote ones, by
// their best path (see tallyRoutes). Prefixes without a best path count as
// local: their origin is unknown, and they are what needs attention.
func splitRoutes(t vrfTable, aggregates, exported map[string]bool, zebra map[string][]zebraRoute) (local []LocalRoute, remote []RemoteRoute) {
	for prefix, paths := range t {
		installed, sids := false, []string{}
		for _, z := range zebra[prefix] {
			if !z.Installed {
				continue
			}
			installed = true
			for _, nh := range z.Nexthops {
				if s := nh.Seg6.Segs; s != "" && !slices.Contains(sids, s) {
					sids = append(sids, s)
				}
			}
		}
		i := slices.IndexFunc(paths, func(p vrfPath) bool { return p.Best })
		if i >= 0 && paths[i].NHVrfName != "" {
			remote = append(remote, RemoteRoute{Prefix: prefix, SIDs: sids, ASPath: paths[i].ASPath, Paths: len(paths), Installed: installed})
			continue
		}
		lr := LocalRoute{Prefix: prefix, Aggregate: aggregates[prefix], Exported: exported[prefix], Installed: installed, NoBestPath: i < 0}
		if i >= 0 {
			lr.ASPath = paths[i].ASPath
			if len(paths[i].Nexthops) > 0 {
				lr.NextHop = vtep(paths[i].Nexthops[0].IP)
			}
		}
		local = append(local, lr)
	}
	return local, remote
}

// vtep returns a local route's next hop as printed: IPv6 routes from EVPN
// carry the VTEP as IPv4-mapped address, the gateway's own routes none.
func vtep(ip string) string {
	a, err := netip.ParseAddr(ip)
	switch {
	case err != nil:
		return ip
	case a.IsUnspecified():
		return ""
	}
	return a.Unmap().String()
}

// evpnRoutes picks the type-5 routes of an L3VNI from the global EVPN table:
// those under its RD (advertised by this gateway) and those received with
// one of its import RTs. Auto-derived RTs carry the originator's ASN, and
// FRR imports them by the VNI alone; so does this.
func evpnRoutes(t evpnTable, info l3vniInfo, vni uint32) []EVPNRoute {
	imports := map[string]bool{}
	for _, rt := range info.ImportRTs {
		imports[strings.TrimPrefix(rt, "RT:")] = true
	}
	matches := func(rts []string) bool {
		for _, rt := range rts {
			_, local, _ := strings.Cut(rt, ":")
			if imports[rt] || local == strconv.FormatUint(uint64(vni), 10) {
				return true
			}
		}
		return false
	}
	var out []EVPNRoute
	for rd, prefixes := range t {
		for _, paths := range prefixes {
			var all []evpnPath
			for _, ps := range paths {
				all = append(all, ps...)
			}
			if len(all) == 0 {
				continue
			}
			p := all[0]
			best := false
			for _, q := range all {
				if q.Best && q.Valid {
					p, best = q, true
					break
				}
			}
			e := EVPNRoute{Advertised: rd == info.RD, RD: rd, Prefix: fmt.Sprintf("%s/%d", p.IP, p.IPLen), ASPath: p.ASPath, Paths: len(all), NoBestPath: !best}
			for _, c := range strings.Fields(p.ExtCommty.String) {
				if rt, ok := strings.CutPrefix(c, "RT:"); ok {
					e.RTs = append(e.RTs, rt)
				}
			}
			if !e.Advertised && !matches(e.RTs) {
				continue
			}
			if len(p.Nexthops) > 0 {
				e.NextHop = p.Nexthops[0].IP
			}
			out = append(out, e)
		}
	}
	return out
}

// fetchEVPN reads the global table of type-5 routes. Its JSON has RD blocks
// (an "rd" field next to the prefixes) and counters at the top level.
func (g *Gateway) fetchEVPN() evpnTable {
	out, err := g.FRR.Show("show bgp l2vpn evpn route type prefix json")
	if err != nil {
		return nil
	}
	t, _ := decodeEVPN([]byte(out))
	return t
}

func decodeEVPN(data []byte) (evpnTable, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, err
	}
	t := evpnTable{}
	for rd, raw := range top {
		var block map[string]json.RawMessage
		if json.Unmarshal(raw, &block) != nil { // a counter
			continue
		}
		for prefix, raw := range block {
			var r struct {
				Paths [][]evpnPath `json:"paths"`
			}
			if prefix == "rd" || json.Unmarshal(raw, &r) != nil {
				continue
			}
			if t[rd] == nil {
				t[rd] = map[string][][]evpnPath{}
			}
			t[rd][prefix] = r.Paths
		}
	}
	return t, nil
}

func sortRoutes(r *Routes) {
	byPrefix := func(a, b string) int {
		pa, ea := netip.ParsePrefix(a)
		pb, eb := netip.ParsePrefix(b)
		if ea != nil || eb != nil {
			return strings.Compare(a, b)
		}
		if pa.Addr().Is4() != pb.Addr().Is4() {
			if pa.Addr().Is4() {
				return -1
			}
			return 1
		}
		if c := pa.Addr().Compare(pb.Addr()); c != 0 {
			return c
		}
		return pa.Bits() - pb.Bits()
	}
	slices.SortFunc(r.Local, func(a, b LocalRoute) int { return byPrefix(a.Prefix, b.Prefix) })
	slices.SortFunc(r.Remote, func(a, b RemoteRoute) int { return byPrefix(a.Prefix, b.Prefix) })
	slices.SortFunc(r.EVPN, func(a, b EVPNRoute) int {
		if a.Advertised != b.Advertised {
			if a.Advertised {
				return 1
			}
			return -1
		}
		if c := byPrefix(a.Prefix, b.Prefix); c != 0 {
			return c
		}
		return strings.Compare(a.RD, b.RD)
	})
}
