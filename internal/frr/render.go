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
	// Aggregates are the active aggregates (AggregateKey): those with more
	// specific routes from the own partition's fabric in their range. Only
	// they are announced, so the same list works on all gateways.
	Aggregates map[string]bool
}

// AggregateKey identifies an aggregate of a VRF in Identity.Aggregates.
func AggregateKey(vrf string, p netip.Prefix) string { return vrf + " " + p.String() }

// Names of the route-maps and lists open-dci renders. They all start with
// "DCI-", which marks them as open-dci's (see Removals).
const (
	PeerInRouteMap = "DCI-PEER-IN"
	RTList         = "DCI-RT"
	// AggregateRouteMap marks the aggregates a gateway announces, with a
	// large community in AggregateList. AdvertiseRouteMap keeps marked
	// routes out of the type-5 announcement: the own partition has the host
	// routes already. The VPN export filter deletes the marker, so remote
	// partitions get the aggregate as type-5.
	AggregateRouteMap = "DCI-AGG"
	AggregateList     = "DCI-AGG"
	AdvertiseRouteMap = "DCI-ADV"
)

// FilterName is the prefix-list and route-map that filter a network's VPN
// export and import per family ("v4", "v6").
func FilterName(vrf, family string) string { return "DCI-" + vrf + "-" + family }

// DefaultFilterName is the route-map that also lets the default route through,
// in the direction networks[].defaultRoute names; DefaultList matches it.
func DefaultFilterName(vrf, family string) string { return FilterName(vrf, family) + "-default" }

func DefaultList(family string) string { return "DCI-DEFAULT-" + family }

// DefaultImportLocalPref puts an imported default route behind one from the
// own fabric (local preference 100).
const DefaultImportLocalPref = 50

// filter is one prefix-list plus route-map; no entries means deny all.
type filter struct {
	Name       string
	PrefixList string // "ip" or "ipv6"
	Entries    []string
	// DeleteMarker strips the aggregate marker on VPN export.
	DeleteMarker bool
	// Default ("export", "import") adds the route-map DefaultName, which
	// also passes the default route, for that direction.
	Default     string
	DefaultName string
	DefaultList string
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
	InterfacePeers  bool
	Filters         []filter
	RTs             []string // all route targets, for the peers' inbound filter
	Aggregating     bool     // some network has aggregates
	DefaultFamilies []string // families with a default prefix-list ("v4", "v6")
	active          map[string]bool
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

// Aggregates returns the active aggregates of a network in one family ("v4",
// "v6"; "" for both).
func (d renderData) Aggregates(vrf, family string) []netip.Prefix {
	var out []netip.Prefix
	for _, n := range d.Config.Networks {
		if n.VRF != vrf {
			continue
		}
		v4, v6 := n.AggregatePrefixes()
		var ps []netip.Prefix
		switch family {
		case "v4":
			ps = v4
		case "v6":
			ps = v6
		default:
			ps = append(v4, v6...)
		}
		for _, p := range ps {
			if d.active[AggregateKey(vrf, p)] {
				out = append(out, p)
			}
		}
	}
	return out
}

// VPNFilter is the route-map of a network's VPN import or export ("import",
// "export") in one family.
func (d renderData) VPNFilter(vrf, family, dir string) string {
	for _, n := range d.Config.Networks {
		if n.VRF == vrf && n.DefaultRoute == dir {
			return DefaultFilterName(vrf, family)
		}
	}
	return FilterName(vrf, family)
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
		active:         id.Aggregates,
	}
	seenRT := map[string]bool{}
	for _, n := range cfg.Networks {
		v4, v6 := n.PrefixRules()
		for _, f := range []struct {
			family, list string
			rules        []config.PrefixRule
		}{{"v4", "ip", v4}, {"v6", "ipv6", v6}} {
			flt := filter{Name: FilterName(n.VRF, f.family), PrefixList: f.list, DeleteMarker: len(n.Aggregates) > 0,
				Default: n.DefaultRoute, DefaultName: DefaultFilterName(n.VRF, f.family), DefaultList: DefaultList(f.family)}
			for _, r := range f.rules {
				flt.Entries = append(flt.Entries, r.String())
			}
			d.Filters = append(d.Filters, flt)
		}
		if len(n.Aggregates) > 0 {
			d.Aggregating = true
		}
		if n.DefaultRoute != "" && len(d.DefaultFamilies) == 0 {
			d.DefaultFamilies = []string{"v4", "v6"}
		}
		if !seenRT[n.RouteTarget] {
			seenRT[n.RouteTarget] = true
			d.RTs = append(d.RTs, n.RouteTarget)
		}
	}
	tpl, err := template.New("dci").Funcs(template.FuncMap{
		"list":        func(s ...string) []string { return s },
		"seq":         func(i int) int { return (i + 1) * 5 },
		"peerIn":      func() string { return PeerInRouteMap },
		"rtList":      func() string { return RTList },
		"aggMap":      func() string { return AggregateRouteMap },
		"aggList":     func() string { return AggregateList },
		"advMap":      func() string { return AdvertiseRouteMap },
		"defaultLP":   func() int { return DefaultImportLocalPref },
		"defaultList": DefaultList,
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
