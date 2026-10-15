package gateway

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/mwindower/open-dci/internal/config"
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

// checkHealth runs the checks once. all: every network counts (status);
// otherwise (Run's withdraw decision) a network counts only once it has
// passed its checks: a network that is still being provisioned (L3VNI not up
// yet, no SID yet) fails them for a few seconds, and both gateways of a pair
// provision it at the same time. Withdrawing for that would take the whole
// partition down whenever a network is added; status shows it instead.
func (g *Gateway) checkHealth(routerID string, all bool) Health {
	var sids map[string]struct {
		Context struct {
			VRFName string `json:"vrfName"`
		} `json:"context"`
	}
	if err := g.FRR.ShowJSON("show segment-routing srv6 sid", &sids); err != nil {
		return Health{Reason: "show segment-routing srv6 sid: " + err.Error()}
	}
	g.pruneSettled()
	g.stuck = nil
	problem := ""
	block := netip.MustParsePrefix(g.Config.Gateway.LocatorBlock)
	reachable := map[netip.Addr]bool{}
	for _, n := range g.Config.Networks {
		var sid netip.Addr
		for s, v := range sids {
			if v.Context.VRFName == n.VRF {
				sid, _ = netip.ParseAddr(s)
			}
		}
		// every network is checked, also after a problem: Run recovers all
		// prefixes without best path at once (g.stuck)
		if reason := g.networkProblem(n, routerID, sid); reason != "" {
			if (all || g.settled[n.VRF]) && problem == "" {
				problem = reason
			}
			continue // a broken (or still provisioned) network has no SIDs to check
		}
		if g.settled == nil {
			g.settled = map[string]bool{}
		}
		g.settled[n.VRF] = true
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
	if problem != "" {
		return Health{Reason: problem}
	}
	return transportVerdict(reachable)
}

// networkProblem checks one network's data plane; "" if it works.
func (g *Gateway) networkProblem(n config.Network, routerID string, sid netip.Addr) string {
	if st := g.l3vniState(n.VNI, routerID); st != "Up" {
		return fmt.Sprintf("%s: L3VNI %d %s", n.VRF, n.VNI, st)
	}
	if !sid.IsValid() {
		return n.VRF + ": no SID"
	}
	if !kernel.LocalSIDInstalled(sid) {
		return fmt.Sprintf("%s: SID %s not installed in the kernel", n.VRF, sid)
	}
	reason := ""
	for _, afi := range []string{"ipv4", "ipv6"} {
		if stuck := g.confirmStuck(n.VRF, afi, g.vrfTable(n.VRF, afi)); len(stuck) > 0 {
			for _, p := range stuck {
				g.stuck = append(g.stuck, stuckPrefix{VRF: n.VRF, AFI: afi, Prefix: p})
			}
			if reason == "" {
				reason = fmt.Sprintf("%s: %d %s prefix(es) without best path", n.VRF, len(stuck), afi)
			}
		}
	}
	return reason
}

// stuckPrefix is a prefix in a tenant VRF with valid paths but no best path:
// FRR never ran its best-path selection for it (version 0), so nothing is
// installed. Seen after a reconcile ran while frr-reload.py rewrote the
// config; FRR doesn't recover by itself, but re-running the selection for
// the prefix fixes it (recoverBestPaths).
type stuckPrefix struct {
	VRF, AFI, Prefix string
}

// stuckPrefixes lists the prefixes of a VRF table with valid paths but no
// best path.
func stuckPrefixes(routes vrfTable) []string {
	var out []string
	for prefix, paths := range routes {
		valid, best := false, false
		for _, p := range paths {
			valid = valid || p.Valid
			best = best || p.Best
		}
		if valid && !best {
			out = append(out, prefix)
		}
	}
	slices.Sort(out)
	return out
}

// stuckGrace: how long a prefix must stay without best path before it
// counts. FRR selects the best path asynchronously, so a prefix whose update
// has just arrived (e.g. a network that came back in another partition) is
// valid but not yet selected for a moment; one reading must not make the
// gateway unhealthy for that.
const stuckGrace = 2 * time.Second

// confirmStuck returns the prefixes of a VRF table that are without best
// path for at least stuckGrace. Run remembers since when (stuckSince,
// across health checks and status collections); a one-off check (status,
// outside Run) reads the table once more after stuckGrace.
func (g *Gateway) confirmStuck(vrf, afi string, table vrfTable) []string {
	return g.confirmStuckWith(vrf, afi, table, func() vrfTable { return g.vrfTable(vrf, afi) })
}

// confirmStuckWith is confirmStuck with the re-read of the table given.
func (g *Gateway) confirmStuckWith(vrf, afi string, table vrfTable, reread func() vrfTable) []string {
	now := g.clock()
	raw := stuckPrefixes(table)
	for s := range g.stuckSince {
		if s.VRF == vrf && s.AFI == afi && !slices.Contains(raw, s.Prefix) {
			delete(g.stuckSince, s)
		}
	}
	if len(raw) == 0 {
		return nil
	}
	if g.gate == nil {
		g.sleep(stuckGrace)
		again := stuckPrefixes(reread())
		return slices.DeleteFunc(raw, func(p string) bool { return !slices.Contains(again, p) })
	}
	if g.stuckSince == nil {
		g.stuckSince = map[stuckPrefix]time.Time{}
	}
	var out []string
	for _, p := range raw {
		k := stuckPrefix{VRF: vrf, AFI: afi, Prefix: p}
		since, ok := g.stuckSince[k]
		if !ok {
			g.stuckSince[k] = now
			continue
		}
		if now.Sub(since) >= stuckGrace {
			out = append(out, p)
		}
	}
	return out
}

func (g *Gateway) clock() time.Time {
	if g.now != nil {
		return g.now()
	}
	return time.Now()
}

func (g *Gateway) sleep(d time.Duration) {
	if g.sleepFn != nil {
		g.sleepFn(d)
		return
	}
	time.Sleep(d)
}

// bestPathRetry is how often the same stuck prefix is cleared at most.
const bestPathRetry = 30 * time.Second

// recoverBestPaths re-runs FRR's best-path selection for the stuck prefixes
// the last health check found ("clear bgp vrf V [ipv6 unicast] prefix P":
// no session is touched). It returns how many it cleared.
func (g *Gateway) recoverBestPaths(now time.Time) int {
	if g.cleared == nil {
		g.cleared = map[stuckPrefix]time.Time{}
	}
	n := 0
	for _, s := range g.stuck {
		if last, ok := g.cleared[s]; ok && now.Sub(last) < bestPathRetry {
			continue
		}
		g.cleared[s] = now
		// FRR's syntax differs per family: IPv4 without, IPv6 with it
		cmd := fmt.Sprintf("clear bgp vrf %s prefix %s", s.VRF, s.Prefix)
		if s.AFI == "ipv6" {
			cmd = fmt.Sprintf("clear bgp vrf %s ipv6 unicast prefix %s", s.VRF, s.Prefix)
		}
		if _, err := g.FRR.Show(cmd); err != nil {
			g.Log.Warn("no best path: clear failed", "vrf", s.VRF, "prefix", s.Prefix, "err", err)
			continue
		}
		g.Log.Info("no best path: re-running FRR's best-path selection", "vrf", s.VRF, "prefix", s.Prefix)
		n++
	}
	for s, t := range g.cleared {
		if now.Sub(t) > 10*bestPathRetry {
			delete(g.cleared, s)
		}
	}
	return n
}

// pruneSettled forgets networks that are no longer configured: one that
// comes back (e.g. its label returns) is provisioned anew.
func (g *Gateway) pruneSettled() {
	configured := func(vrf string) bool {
		return slices.ContainsFunc(g.Config.Networks, func(n config.Network) bool { return n.VRF == vrf })
	}
	for vrf := range g.settled {
		if !configured(vrf) {
			delete(g.settled, vrf)
		}
	}
	for s := range g.stuckSince {
		if !configured(s.VRF) {
			delete(g.stuckSince, s)
		}
	}
}
