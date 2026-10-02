package gateway

import (
	"github.com/mwindower/open-dci/internal/frr"
)

// An aggregate is announced while the own partition has hosts in its range:
// more specific routes in the tenant VRF that came from the fabric (EVPN), not
// from the VPN (a remote partition). open-dci tracks that itself instead of
// using FRR's aggregate-address, whose VPN copy FRR 10.4.1 leaves behind when
// the last host goes away.

type aggregatePath struct {
	// NhVrfName is set on routes imported from the VPN (the next hop's VRF).
	NhVrfName string `json:"nhVrfName"`
}

// hasHosts: some route in the aggregate's range, other than the aggregate
// itself, came from the own fabric.
func hasHosts(routes map[string][]aggregatePath, prefix string) bool {
	for p, paths := range routes {
		if p == prefix {
			continue
		}
		for _, path := range paths {
			if path.NhVrfName == "" {
				return true
			}
		}
	}
	return false
}

// activeAggregates asks FRR which aggregates have hosts. A failed query keeps
// the aggregate's last known state: FRR in the middle of a reload is no reason
// to withdraw, nor to announce.
func (g *Gateway) activeAggregates() map[string]bool {
	active := map[string]bool{}
	for _, n := range g.Config.Networks {
		v4, v6 := n.AggregatePrefixes()
		for _, p := range append(v4, v6...) {
			af := "ipv4"
			if p.Addr().Is6() {
				af = "ipv6"
			}
			key := frr.AggregateKey(n.VRF, p)
			var out struct {
				Routes map[string][]aggregatePath `json:"routes"`
			}
			if err := g.FRR.ShowJSON("show bgp vrf "+n.VRF+" "+af+" unicast "+p.String()+" longer-prefixes", &out); err != nil {
				active[key] = g.lastAggregates[key]
				continue
			}
			active[key] = hasHosts(out.Routes, p.String())
		}
	}
	g.lastAggregates = active
	return active
}
