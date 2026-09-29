package frr

import (
	_ "embed"
	"fmt"
	"net/netip"
	"strings"
	"text/template"

	"github.com/mwindower/srv6-dci/internal/config"
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

// Identity is what srv6-dci needs to know about the existing BGP setup. It is
// taken from the config or discovered from the running FRR configuration.
type Identity struct {
	ASN      uint32
	RouterID string
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

// Render returns the FRR configuration srv6-dci adds for cfg.
func Render(cfg *config.Config, id Identity) (string, error) {
	if id.ASN == 0 || id.RouterID == "" {
		return "", fmt.Errorf("BGP ASN and router-id are unknown: set gateway.asn/gateway.routerID or let srv6-dci discover them from FRR")
	}
	block := netip.MustParsePrefix(cfg.Gateway.LocatorBlock)
	loc := netip.MustParsePrefix(cfg.Gateway.Locator)
	d := renderData{
		Config:       cfg,
		ASN:          id.ASN,
		RouterID:     id.RouterID,
		Loopback:     cfg.Gateway.Loopback(),
		Locator:      loc,
		LocatorBlock: block,
		LocatorName:  config.LocatorName,
		BlockLen:     block.Bits(),
		NodeLen:      cfg.Gateway.NodeLength,
		TransportVRF: cfg.Transport.VRF,
		Veth:         cfg.Transport.Veth,
		VethPeer:     cfg.Transport.VethPeer,
		VethLL:       VethLL,
		VethPeerLL:   VethPeerLL,
		Networks:     cfg.Networks,
	}
	tpl, err := template.New("dci").Funcs(template.FuncMap{
		"list": func(s ...string) []string { return s },
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
