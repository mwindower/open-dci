package gateway

import (
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/mwindower/open-dci/internal/kernel"
)

// A gateway whose BGP sessions are fine can still be unable to forward: an
// L3VNI that isn't up, a SID that isn't installed, routes that FRR never
// selected, or no usable route to any remote gateway. Neither BGP nor BFD
// notice that, but the gateway does. Run checks its health every healthPoll and
// withdraws like a drain (locator and type-5 routes) after healthDown failed
// checks in a row; after healthUp good ones it announces again. Its anycast
// partner carries the traffic meanwhile.
const (
	healthPoll = 2 * time.Second
	healthDown = 2
	healthUp   = 3
)

// Health is the result of a health check; Reason says what is broken.
type Health struct {
	OK     bool
	Reason string `json:",omitempty"`
}

// routeEntry is a route of FRR's "show ipv6 route <addr> json".
type routeEntry struct {
	Prefix   string `json:"prefix"`
	Selected bool   `json:"selected"`
	Nexthops []struct {
		Active      bool   `json:"active"`
		FIB         bool   `json:"fib"`
		Interface   string `json:"interfaceName"`
		Blackhole   bool   `json:"blackhole"`
		Unreachable bool   `json:"unreachable"`
		Reject      bool   `json:"reject"`
	} `json:"nexthops"`
}

// usable: the selected route is a route into the locator block (not a
// default or other covering route) with a working next hop.
func usable(routes map[string][]routeEntry, block netip.Prefix) bool {
	for _, rs := range routes {
		for _, r := range rs {
			if !r.Selected {
				continue
			}
			p, err := netip.ParsePrefix(r.Prefix)
			if err != nil || p.Bits() < block.Bits() || !block.Contains(p.Addr()) {
				return false
			}
			for _, nh := range r.Nexthops {
				if nh.Active && nh.FIB && nh.Interface != "" && !nh.Blackhole && !nh.Unreachable && !nh.Reject {
					return true
				}
			}
			return false
		}
	}
	return false
}

// transportVerdict: the own transport is broken only if no remote SID at all
// is reachable. Some unreachable ones are a remote partition's problem, and
// withdrawing everywhere because of it would take down every partition.
func transportVerdict(reachable map[netip.Addr]bool) Health {
	if len(reachable) == 0 {
		return Health{OK: true}
	}
	var dead []string
	for sid, ok := range reachable {
		if ok {
			return Health{OK: true}
		}
		dead = append(dead, sid.String())
	}
	return Health{Reason: "no remote SID reachable (" + strings.Join(dead, ", ") + ")"}
}

// checkHealth runs the checks once.
func (g *Gateway) checkHealth(routerID string) Health {
	var sids map[string]struct {
		Context struct {
			VRFName string `json:"vrfName"`
		} `json:"context"`
	}
	if err := g.FRR.ShowJSON("show segment-routing srv6 sid", &sids); err != nil {
		return Health{Reason: "show segment-routing srv6 sid: " + err.Error()}
	}
	block := netip.MustParsePrefix(g.Config.Gateway.LocatorBlock)
	reachable := map[netip.Addr]bool{}
	for _, n := range g.Config.Networks {
		if st := g.l3vniState(n.VNI, routerID); st != "Up" {
			return Health{Reason: fmt.Sprintf("%s: L3VNI %d %s", n.VRF, n.VNI, st)}
		}
		var sid netip.Addr
		for s, v := range sids {
			if v.Context.VRFName == n.VRF {
				sid, _ = netip.ParseAddr(s)
			}
		}
		if !sid.IsValid() {
			return Health{Reason: n.VRF + ": no SID"}
		}
		if !kernel.LocalSIDInstalled(sid) {
			return Health{Reason: fmt.Sprintf("%s: SID %s not installed in the kernel", n.VRF, sid)}
		}
		for _, afi := range []string{"ipv4", "ipv6"} {
			if _, _, stuck := g.countRoutes(n.VRF, afi); stuck > 0 {
				return Health{Reason: fmt.Sprintf("%s: %d %s prefix(es) without best path", n.VRF, stuck, afi)}
			}
		}
		remote, err := kernel.EncapSIDs(n.Table)
		if err != nil {
			return Health{Reason: n.VRF + ": " + err.Error()}
		}
		for _, r := range remote {
			if _, done := reachable[r]; done {
				continue
			}
			cmd := "show ipv6 route " + r.String()
			if tv := g.Config.Transport.VRF; tv != "" {
				cmd = "show ipv6 route vrf " + tv + " " + r.String()
			}
			var routes map[string][]routeEntry
			reachable[r] = g.FRR.ShowJSON(cmd, &routes) == nil && usable(routes, block)
		}
	}
	return transportVerdict(reachable)
}
