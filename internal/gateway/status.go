package gateway

import (
	"fmt"
	"slices"
	"time"

	"github.com/vishvananda/netlink"

	"github.com/mwindower/open-dci/internal/config"
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
	Drained bool
	// Health: the data-plane checks open-dci run withdraws on
	Health   Health
	Kernel   KernelStatus
	Peers    []PeerStatus
	Networks []NetworkStatus
	// Source: the network source, if one is configured
	Source *SourceStatus `json:",omitempty"`
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
	// Group: the source's name for the network ("" = from the config file)
	Group         string `json:",omitempty"`
	SID, Behavior string
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
	// Routes lists the prefixes (status --routes)
	Routes *Routes `json:",omitempty"`
}

// Status collects the gateway's operational state from FRR and the kernel.
// For the networks routes selects (see SelectNetworks) it lists the routes.
func (g *Gateway) Status(routes string) (*Status, error) {
	withRoutes, err := SelectNetworks(g.Config, routes)
	if err != nil {
		return nil, err
	}
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
		Health:         g.checkHealth(plan.Identity.RouterID),
		Kernel:         g.kernelStatus(),
	}

	if g.Source != nil {
		src := g.source
		st.Source = &src
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
	var evpn evpnTable
	if len(withRoutes) > 0 {
		evpn = g.fetchEVPN()
	}

	for _, n := range g.Config.Networks {
		ns := NetworkStatus{VRF: n.VRF, RouteTarget: n.RouteTarget, RD: g.Config.RDFor(n, st.RouterID), Group: n.Group}
		for sid, s := range sids {
			if s.Context.VRFName == n.VRF {
				ns.SID, ns.Behavior = sid, s.Behavior
			}
		}
		ns.VNI, ns.L3VNIState = n.VNI, g.l3vniState(n.VNI, st.RouterID)
		bgp := map[string]vrfTable{"ipv4": g.vrfTable(n.VRF, "ipv4"), "ipv6": g.vrfTable(n.VRF, "ipv6")}
		var stuck4, stuck6 int
		ns.LocalV4, ns.RemoteV4, stuck4 = tallyRoutes(bgp["ipv4"])
		ns.LocalV6, ns.RemoteV6, stuck6 = tallyRoutes(bgp["ipv6"])
		ns.NoBestPath = stuck4 + stuck6
		if slices.ContainsFunc(withRoutes, func(w config.Network) bool { return w.VRF == n.VRF }) {
			ns.Routes = g.routes(n, ns.RD, bgp, evpn)
		}
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
	ASPath    string `json:"path"`
	Nexthops  []struct {
		IP string `json:"ip"`
	} `json:"nexthops"`
}

// vrfTable reads a VRF's BGP table; nil if FRR can't be asked.
func (g *Gateway) vrfTable(vrf, afi string) vrfTable {
	var t struct {
		Routes vrfTable `json:"routes"`
	}
	if err := g.FRR.ShowJSON(fmt.Sprintf("show bgp vrf %s %s unicast", vrf, afi), &t); err != nil {
		return nil
	}
	return t.Routes
}

// tallyRoutes counts best paths: routes imported from the VPN carry the VRF
// their next hop is resolved in (nhVrfName), local ones do not. noBest counts
// prefixes that have valid paths but no best path.
func tallyRoutes(routes vrfTable) (local, remote, noBest int) {
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
func (s *Status) Healthy() bool { return len(s.Problems()) == 0 }

// Warnings lists what needs attention but doesn't make the gateway
// unhealthy: a stale network source (its last answer stays in place) and
// groups it can't stitch.
func (s *Status) Warnings(now time.Time) []string {
	src := s.Source
	if src == nil {
		return nil
	}
	var out []string
	if src.Stale(now) {
		last := "never"
		if !src.Synced.IsZero() {
			last = src.Synced.UTC().Format(time.RFC3339)
		}
		out = append(out, fmt.Sprintf("source: no answer since %s, keeping the last one: %s", last, src.LastError))
	}
	for _, sk := range src.Skipped {
		out = append(out, fmt.Sprintf("source: %s not stitched: %s", sk.Group, sk.Reason))
	}
	return out
}

// Problems lists what is not in place, empty if the gateway is healthy.
func (s *Status) Problems() []string {
	var out []string
	add := func(format string, a ...any) { out = append(out, fmt.Sprintf(format, a...)) }
	k := s.Kernel
	if s.MissingLines > 0 {
		add("frr: %d line(s) missing", s.MissingLines)
	}
	if !s.Announced && !s.Drained {
		add("locator withheld: %s", s.WithheldReason)
	}
	if !s.Health.OK {
		add("unhealthy: %s", s.Health.Reason)
	}
	if k.StrictMode != "1" {
		add("net.vrf.strict_mode is %q, not 1", k.StrictMode)
	}
	if len(k.Filter) != k.FilterRequired {
		add("ingress filter: %d of %d rules", len(k.Filter), k.FilterRequired)
	}
	if k.TransportVRF != "" {
		if !k.VethUp {
			add("transport: veth down")
		}
		if k.DCIPathMTU < k.RequiredMTU {
			add("transport: path MTU %d below %d", k.DCIPathMTU, k.RequiredMTU)
		}
		if !k.LocalRuleLast {
			add("transport: the local ip rule is not behind l3mdev")
		}
	}
	for _, p := range s.Peers {
		if p.State != "Established" {
			add("peer %s: %s", p.Address, p.State)
		}
	}
	for _, n := range s.Networks {
		switch {
		case n.SID == "":
			add("%s: no SID", n.VRF)
		case n.L3VNIState != "Up":
			add("%s: L3VNI %d %s", n.VRF, n.VNI, n.L3VNIState)
		case n.NoBestPath > 0:
			add("%s: %d prefix(es) without best path", n.VRF, n.NoBestPath)
		}
	}
	return out
}
