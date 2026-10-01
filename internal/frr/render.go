package frr

import (
	_ "embed"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"text/template"

	"github.com/mwindower/open-dci/internal/config"
)

//go:embed dci.conf.tpl
var dciTemplate string

// Link-local addresses of the veth pair between the default VRF (VethLL) and
// the transport VRF (VethPeerLL). They are fixed because the veth is point-to-point
// and never leaves the box; link-local keeps them out of any redistribution.
const (
	VethLL     = "fe80::1"
	VethPeerLL = "fe80::2"
)

// Identity is what open-dci needs to know about the existing BGP setup. It is
// taken from the config or discovered from the running FRR configuration.
type Identity struct {
	ASN      uint32
	RouterID string
	// Withhold leaves out the announcement of locator and loopback while the
	// gateway can't deliver traffic yet: a locator announced before the tenant
	// VRFs hold the fabric's routes attracts packets that are decapsulated
	// into an empty VRF and dropped.
	Withhold bool
	// Drain also stops announcing the tenant VRFs' routes into the fabric
	// (type-5): the fabric and the exits send nothing to a drained gateway,
	// its partner carries everything (planned maintenance).
	Drain bool
}

// Names of the route-maps and lists open-dci renders. They all start with
// "DCI-", which marks them as open-dci's (see Removals).
const (
	PeerInRouteMap = "DCI-PEER-IN"
	RTList         = "DCI-RT"
)

// FilterName is the prefix-list and route-map that filter a network's VPN
// export and import per family ("v4", "v6").
func FilterName(vrf, family string) string { return "DCI-" + vrf + "-" + family }

// filter is one prefix-list plus route-map; no entries means deny all.
type filter struct {
	Name       string
	PrefixList string // "ip" or "ipv6"
	Entries    []string
}

type renderData struct {
	*config.Config
	ASN          uint32
	RouterID     string
	Loopback     netip.Addr
	Locator      netip.Prefix
	LocatorBlock netip.Prefix
	LocatorName  string
	BlockLen     int
	NodeLen      int
	TransportVRF string
	Veth         string
	VethPeer     string
	VethLL       string
	VethPeerLL   string
	Networks     []config.Network
	Withhold     bool
	Drain        bool
	Anycast      bool // the loopback is outside the (shared) locator
	AddressPeers bool // some peers are direct sessions to remote gateways
	// InterfacePeers: some peers are the single-hop session to the exit. FRR
	// tracks the remote SID as next hop of imported SRv6 VPN routes, and for
	// single-hop eBGP it requires that to be connected, which a SID never is.
	InterfacePeers bool
	Filters        []filter
	RTs            []string // all route targets, for the peers' inbound filter
}

func (d renderData) RD(vrf string) string {
	for _, n := range d.Config.Networks {
		if n.VRF == vrf {
			return d.RDFor(n, d.RouterID)
		}
	}
	return ""
}

func (d renderData) RT(vrf string) string {
	for _, n := range d.Config.Networks {
		if n.VRF == vrf {
			return n.RouteTarget
		}
	}
	return ""
}

// Render returns the FRR configuration open-dci adds for cfg.
func Render(cfg *config.Config, id Identity) (string, error) {
	if id.ASN == 0 || id.RouterID == "" {
		return "", fmt.Errorf("BGP ASN and router-id are unknown: set gateway.asn/gateway.routerID or let open-dci discover them from FRR")
	}
	block := netip.MustParsePrefix(cfg.Gateway.LocatorBlock)
	loc := netip.MustParsePrefix(cfg.Gateway.Locator)
	d := renderData{
		Config:         cfg,
		ASN:            id.ASN,
		RouterID:       id.RouterID,
		Loopback:       cfg.Gateway.Loopback(),
		Anycast:        cfg.Gateway.Anycast(),
		AddressPeers:   slices.ContainsFunc(cfg.Peers, func(p config.Peer) bool { return p.Address != "" }),
		InterfacePeers: slices.ContainsFunc(cfg.Peers, func(p config.Peer) bool { return p.Interface != "" }),
		Locator:        loc,
		LocatorBlock:   block,
		LocatorName:    config.LocatorName,
		BlockLen:       block.Bits(),
		NodeLen:        cfg.Gateway.NodeLength,
		TransportVRF:   cfg.Transport.VRF,
		Veth:           cfg.Transport.Veth,
		VethPeer:       cfg.Transport.VethPeer,
		VethLL:         VethLL,
		VethPeerLL:     VethPeerLL,
		Networks:       cfg.Networks,
		Withhold:       id.Withhold || id.Drain,
		Drain:          id.Drain,
	}
	seenRT := map[string]bool{}
	for _, n := range cfg.Networks {
		v4, v6 := n.PrefixRules()
		for _, f := range []struct {
			family, list string
			rules        []config.PrefixRule
		}{{"v4", "ip", v4}, {"v6", "ipv6", v6}} {
			flt := filter{Name: FilterName(n.VRF, f.family), PrefixList: f.list}
			for _, r := range f.rules {
				flt.Entries = append(flt.Entries, r.String())
			}
			d.Filters = append(d.Filters, flt)
		}
		if !seenRT[n.RouteTarget] {
			seenRT[n.RouteTarget] = true
			d.RTs = append(d.RTs, n.RouteTarget)
		}
	}
	tpl, err := template.New("dci").Funcs(template.FuncMap{
		"list":   func(s ...string) []string { return s },
		"seq":    func(i int) int { return (i + 1) * 5 },
		"filter": FilterName,
		"peerIn": func() string { return PeerInRouteMap },
		"rtList": func() string { return RTList },
		"familyOf": func(af string) string {
			if af == "ipv4" {
				return "v4"
			}
			return "v6"
		},
	}).Parse(dciTemplate)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if err := tpl.Execute(&b, d); err != nil {
		return "", err
	}
	return b.String(), nil
}
