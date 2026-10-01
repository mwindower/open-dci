package gateway

import (
	"fmt"

	"github.com/vishvananda/netlink"

	"github.com/mwindower/open-dci/internal/frr"
	"github.com/mwindower/open-dci/internal/kernel"
)

// Status is the operational view of a gateway.
type Status struct {
	ASN          uint32
	RouterID     string
	Loopback     string
	Locator      string
	MissingLines int // FRR drift: desired lines not in the running config
	// Announced: locator and loopback are announced; if not, WithheldReason
	// says why (the gateway waits for the fabric's EVPN routes)
	Announced      bool
	WithheldReason string `json:",omitempty"`
	// Drained: withdrawn on purpose (open-dci drain), counts as healthy
	Drained  bool
	Kernel   KernelStatus
	Peers    []PeerStatus
	Networks []NetworkStatus
}

type KernelStatus struct {
	TransportVRF  string // "" = default VRF: the fields below are not used
	VethUp        bool
	DCIPathMTU    int // smallest MTU on the transport VRF's bridge/vxlan/SVI
	RequiredMTU   int
	LocalRuleLast bool
	StrictMode    string
	// Filter: the ingress filter's rules and their drop counters
	Filter         []kernel.FilterCounter
	FilterRequired int
	Err            string `json:",omitempty"`
}

type PeerStatus struct {
	Address, State, Uptime string
	ASN                    uint32
	// accepted / sent prefixes per address family
	V4Accepted, V4Sent, V6Accepted, V6Sent int
}

type NetworkStatus struct {
	VRF, RouteTarget, RD string
	SID, Behavior        string
	// VNI and the EVPN state of the L3VNI as zebra sees it ("Up" once VRF,
	// bridge and VXLAN device are bound)
	VNI        uint32
	L3VNIState string
	// local = learned in this partition (exported), remote = imported via VPN
	LocalV4, LocalV6, RemoteV4, RemoteV6 int
	// NoBestPath counts prefixes with valid paths but none selected: FRR
	// installs nothing for them (seen after an frr-reload re-created the
	// VRF's BGP instance), a silent black hole
	NoBestPath int
}

// Status collects the gateway's operational state from FRR and the kernel.
func (g *Gateway) Status() (*Status, error) {
	_, plan, err := g.Plan()
	if err != nil {
		return nil, err
	}
	st := &Status{
		ASN:            plan.Identity.ASN,
		RouterID:       plan.Identity.RouterID,
		Loopback:       g.Config.Gateway.Loopback().String(),
		Locator:        g.Config.Gateway.Locator,
		MissingLines:   len(plan.Missing),
		Announced:      plan.Readiness.Ready,
		WithheldReason: plan.Readiness.Reason,
		Drained:        plan.Drained,
		Kernel:         g.kernelStatus(),
	}

	for _, p := range g.Config.Peers {
		ps := PeerStatus{Address: p.Neighbor(), ASN: p.ASN, State: "unknown"}
		var nb map[string]struct {
			BGPState string `json:"bgpState"`
			RemoteAS uint32 `json:"remoteAs"`
			Uptime   string `json:"bgpTimerUpString"`
			AFI      map[string]struct {
				Accepted int `json:"acceptedPrefixCounter"`
				Sent     int `json:"sentPrefixCounter"`
			} `json:"addressFamilyInfo"`
		}
		if err := g.FRR.ShowJSON("show bgp neighbors "+p.Neighbor(), &nb); err == nil {
			if n, ok := nb[p.Neighbor()]; ok {
				ps.State, ps.Uptime = n.BGPState, n.Uptime
				if ps.ASN == 0 { // interface peers: the base config knows the ASN
					ps.ASN = n.RemoteAS
				}
				ps.V4Accepted, ps.V4Sent = n.AFI["ipv4Vpn"].Accepted, n.AFI["ipv4Vpn"].Sent
				ps.V6Accepted, ps.V6Sent = n.AFI["ipv6Vpn"].Accepted, n.AFI["ipv6Vpn"].Sent
			}
		}
		st.Peers = append(st.Peers, ps)
	}

	var sids map[string]struct {
		Behavior string `json:"behavior"`
		Context  struct {
			VRFName string `json:"vrfName"`
		} `json:"context"`
	}
	_ = g.FRR.ShowJSON("show segment-routing srv6 sid", &sids)

	for _, n := range g.Config.Networks {
		ns := NetworkStatus{VRF: n.VRF, RouteTarget: n.RouteTarget, RD: g.Config.RDFor(n, st.RouterID)}
		for sid, s := range sids {
			if s.Context.VRFName == n.VRF {
				ns.SID, ns.Behavior = sid, s.Behavior
			}
		}
		ns.VNI, ns.L3VNIState = n.VNI, g.l3vniState(n.VNI, st.RouterID)
		var stuck4, stuck6 int
		ns.LocalV4, ns.RemoteV4, stuck4 = g.countRoutes(n.VRF, "ipv4")
		ns.LocalV6, ns.RemoteV6, stuck6 = g.countRoutes(n.VRF, "ipv6")
		ns.NoBestPath = stuck4 + stuck6
		st.Networks = append(st.Networks, ns)
	}
	return st, nil
}

// l3vniState returns zebra's state of an L3VNI, or "missing" if
// its kernel devices are not in place.
func (g *Gateway) l3vniState(vni uint32, routerID string) string {
	for _, v := range l3vnis(g.Config, frr.Identity{RouterID: routerID}) {
		if v.VNI == vni && !kernel.L3VNIUp(v) {
			return "missing"
		}
	}
	var s struct {
		State string `json:"state"`
	}
	if err := g.FRR.ShowJSON(fmt.Sprintf("show evpn vni %d", vni), &s); err != nil || s.State == "" {
		return "unknown"
	}
	return s.State
}

// vrfPath is a path of a VRF's BGP table, as far as status needs it.
type vrfPath struct {
	Valid     bool   `json:"valid"`
	Best      bool   `json:"bestpath"`
	NHVrfName string `json:"nhVrfName"`
}

// countRoutes counts best paths in a VRF (see tallyRoutes).
func (g *Gateway) countRoutes(vrf, afi string) (local, remote, noBest int) {
	var t struct {
		Routes map[string][]vrfPath `json:"routes"`
	}
	if err := g.FRR.ShowJSON(fmt.Sprintf("show bgp vrf %s %s unicast", vrf, afi), &t); err != nil {
		return 0, 0, 0
	}
	return tallyRoutes(t.Routes)
}

// tallyRoutes counts best paths: routes imported from the VPN carry the VRF
// their next hop is resolved in (nhVrfName), local ones do not. noBest counts
// prefixes that have valid paths but no best path.
func tallyRoutes(routes map[string][]vrfPath) (local, remote, noBest int) {
	for _, paths := range routes {
		valid, best := false, false
		for _, p := range paths {
			valid = valid || p.Valid
			best = best || p.Best
			switch {
			case !p.Best:
			case p.NHVrfName != "":
				remote++
			default:
				local++
			}
		}
		if valid && !best {
			noBest++
		}
	}
	return local, remote, noBest
}

func (g *Gateway) kernelStatus() KernelStatus {
	ks := KernelStatus{TransportVRF: g.Config.Transport.VRF, RequiredMTU: g.Config.Transport.MTU}
	ks.StrictMode, _ = kernel.GetSysctl("net.vrf.strict_mode")
	ks.Filter, _ = kernel.FilterCounters(FilterTable)
	ks.FilterRequired = len(FilterRules(g.Config))
	if !g.Config.Transport.InVRF() {
		return ks
	}
	if l, err := netlink.LinkByName(g.Config.Transport.Veth); err == nil {
		ks.VethUp = l.Attrs().OperState == netlink.OperUp
	}
	if p, err := kernel.FindVNIPath(g.Config.Transport.VRF); err == nil {
		ks.DCIPathMTU = p.MinMTU()
	} else {
		ks.Err = err.Error()
	}
	ks.LocalRuleLast, _ = kernel.LocalRuleLast()
	return ks
}

// Healthy reports whether everything open-dci is responsible for is in place.
func (s *Status) Healthy() bool {
	k := s.Kernel
	if s.MissingLines > 0 || !s.Announced && !s.Drained || k.StrictMode != "1" || len(k.Filter) != k.FilterRequired {
		return false
	}
	if k.TransportVRF != "" && (!k.VethUp || k.DCIPathMTU < k.RequiredMTU || !k.LocalRuleLast) {
		return false
	}
	for _, p := range s.Peers {
		if p.State != "Established" {
			return false
		}
	}
	for _, n := range s.Networks {
		if n.SID == "" || n.L3VNIState != "Up" || n.NoBestPath > 0 {
			return false
		}
	}
	return true
}
