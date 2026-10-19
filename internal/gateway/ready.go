package gateway

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mwindower/open-dci/internal/frr"
)

// The gateway announces its locator (and loopback) only while it can deliver
// what the locator attracts. After its EVPN sessions come up, the tenant VRFs
// are empty until the fabric's type-5 routes have arrived; a locator announced
// earlier makes the exits send packets that are decapsulated into an empty VRF
// and dropped (the lab measured 1.5-3 s of loss whenever a gateway's links
// came back). The anycast partner carries the traffic meanwhile.

// readyFallback: a session without End-of-RIB (graceful restart disabled on
// the peer) counts as converged after this long.
const readyFallback = 30 * time.Second

// readyPoll is how often Run checks readiness between reconciles, and
// readyDebounce how many polls in a row must agree before it acts: a link flap
// withdraws the locator after ~1.5 s, before unnumbered sessions are back
// (they wait for router advertisements, ~3 s).
const (
	readyPoll     = 500 * time.Millisecond
	readyDebounce = 3
)

// Readiness tells whether the gateway may announce its locator, and why not.
type Readiness struct {
	Ready  bool
	Reason string // why not ready
}

type neighborState struct {
	State  string                     `json:"bgpState"`
	UpMsec int64                      `json:"bgpTimerUpMsec"`
	AFI    map[string]json.RawMessage `json:"addressFamilyInfo"`
	GR     struct {
		EndOfRibRecv map[string]bool `json:"endOfRibRecv"`
	} `json:"gracefulRestartInfo"`
}

// evpnReady: at least one established EVPN session (of the default BGP
// instance) has delivered its initial table, marked by End-of-RIB, or has been
// up for readyFallback.
func evpnReady(neighbors map[string]neighborState) Readiness {
	if len(neighbors) == 0 {
		return Readiness{Reason: "no BGP neighbors"}
	}
	waiting := false
	for _, n := range neighbors {
		if _, evpn := n.AFI["l2VpnEvpn"]; !evpn || n.State != "Established" {
			continue
		}
		if n.GR.EndOfRibRecv["l2VpnEvpn"] || time.Duration(n.UpMsec)*time.Millisecond >= readyFallback {
			return Readiness{Ready: true}
		}
		waiting = true
	}
	if waiting {
		return Readiness{Reason: "waiting for the fabric's EVPN routes (End-of-RIB)"}
	}
	return Readiness{Reason: "no established EVPN session"}
}

// readiness evaluates the BGP neighbors. If FRR can't be asked (e.g. while the
// base system reloads it), the last known state stays: a failed query is no
// reason to withdraw, nor to announce.
func (g *Gateway) readiness() Readiness {
	var nb map[string]neighborState
	err := g.FRR.ShowJSON("show bgp neighbors", &nb)
	if err == nil && len(nb) == 0 {
		// a base config always has neighbors: an empty answer is FRR in
		// the middle of a reload, not a gateway without sessions
		err = fmt.Errorf("no neighbors")
	}
	if err != nil {
		r := g.lastReadiness
		if !r.Ready {
			r.Reason = "show bgp neighbors: " + err.Error()
		}
		return r
	}
	g.lastReadiness = evpnReady(nb)
	g.evpnSessions = map[string]bool{}
	for name, n := range nb {
		if _, evpn := n.AFI["l2VpnEvpn"]; evpn && n.State == "Established" {
			g.evpnSessions[name] = true
		}
	}
	return g.lastReadiness
}

// resetsSessions tells whether applying the missing lines resets one of the
// established EVPN sessions: FRR resets a session that gets a new address
// family (here the VPN families on the base session to the exit, at the first
// apply). The readiness was judged on the session before the reset; once it
// is back, it needs to converge again.
func resetsSessions(missing []frr.Line, evpnSessions map[string]bool) bool {
	for _, l := range missing {
		f := strings.Fields(l.Text)
		if len(f) == 3 && f[0] == "neighbor" && f[2] == "activate" && evpnSessions[f[1]] &&
			len(l.Context) == 2 && !strings.Contains(l.Context[0], " vrf ") && strings.HasPrefix(l.Context[1], "address-family ") {
			return true
		}
	}
	return false
}
