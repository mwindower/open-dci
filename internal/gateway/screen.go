package gateway

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/vishvananda/netlink"

	"github.com/mwindower/open-dci/internal/config"
	"github.com/mwindower/open-dci/internal/frr"
	"github.com/mwindower/open-dci/internal/kernel"
)

// screen sets aside the API networks that can't be provisioned on this
// gateway because the kernel or FRR already use their names, VNI or table
// for something open-dci didn't create (e.g. a VRF of the base config).
// Provisioning one of them would fail the whole reconcile, every round, and
// leave all other networks unreconciled: one bad write through the API
// would freeze the gateway. The networks of the config file aren't
// screened: a conflict there is the operator's error and stops the
// reconcile loudly.
//
// Plan screens the wanted config (unscreened) every round, so a network
// applies once its conflict is gone; Config is the screened config until
// takeAPI adopts a new one. Must run on Run's goroutine (or a command's).
func (g *Gateway) screen(running string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	wanted := g.Config
	if g.unscreened != nil && g.Config == g.screened {
		wanted = g.unscreened
	}
	g.Config, g.unscreened, g.screened, g.blocked = wanted, nil, nil, nil
	if g.static == nil || len(wanted.Networks) == len(g.static.Networks) {
		return
	}
	var links []netlink.Link
	if !g.SkipKernel {
		links, _ = netlink.LinkList() // without it, the kernel checks pass; the reconcile reports the error
	}
	vnis := frrVNIs(running)
	prev, _ := os.ReadFile(g.StateFile)
	asn := ""
	if b, err := frr.DiscoverBase(running); err == nil {
		asn = strconv.FormatUint(uint64(b.ASN), 10)
	}
	out := *wanted
	out.Networks = nil
	for _, n := range wanted.Networks {
		if slices.ContainsFunc(g.static.Networks, func(s config.Network) bool { return s.VRF == n.VRF }) {
			out.Networks = append(out.Networks, n)
			continue
		}
		reason := kernelConflict(n, links, g.SkipKernel)
		if reason == "" {
			reason = frrConflict(n, wanted, vnis, running, string(prev), asn, links, g.SkipKernel)
		}
		if reason == "" {
			out.Networks = append(out.Networks, n)
			continue
		}
		g.blocked = append(g.blocked, config.Skipped{VRF: n.VRF, Group: n.Group, Reason: reason})
	}
	if len(g.blocked) == 0 {
		return
	}
	if !slices.Equal(g.blocked, g.lastBlocked) {
		for _, b := range g.blocked {
			g.Log.Warn("api: network not applied", "vrf", b.VRF, "group", b.Group, "reason", b.Reason)
		}
	}
	g.lastBlocked = g.blocked
	g.Config, g.unscreened, g.screened = &out, wanted, &out
}

// kernelConflict: a device open-dci would create for n exists without its
// tag, or a foreign device uses n's VNI or table.
func kernelConflict(n config.Network, links []netlink.Link, skip bool) string {
	if skip {
		return ""
	}
	names := map[string]bool{n.VRF: true, n.BridgeName(): true, n.VxlanName(): true}
	for _, l := range links {
		a := l.Attrs()
		if a.Alias == kernel.OwnerAlias {
			continue
		}
		switch {
		case names[a.Name]:
			return fmt.Sprintf("%s exists but was not created by open-dci", a.Name)
		case l.Type() == "vxlan" && uint32(l.(*netlink.Vxlan).VxlanId) == n.VNI:
			return fmt.Sprintf("VNI %d is used by %s, which open-dci didn't create", n.VNI, a.Name)
		case l.Type() == "vrf" && l.(*netlink.Vrf).Table == n.Table:
			return fmt.Sprintf("table %d is used by vrf %s, which open-dci didn't create", n.Table, a.Name)
		}
	}
	return ""
}

// frrConflict: FRR maps n's VNI to a VRF that isn't one of open-dci's, or
// has a BGP instance for n's VRF that open-dci didn't add.
func frrConflict(n config.Network, cfg *config.Config, vnis map[uint32]string, running, prev, asn string, links []netlink.Link, skip bool) string {
	if vrf, ok := vnis[n.VNI]; ok && vrf != n.VRF && !slices.ContainsFunc(cfg.Networks, func(o config.Network) bool { return o.VRF == vrf }) {
		return fmt.Sprintf("VNI %d belongs to FRR vrf %s", n.VNI, vrf)
	}
	instance := "router bgp " + asn + " vrf " + n.VRF
	if asn == "" || !hasLine(running, instance) || hasLine(prev, instance) {
		return ""
	}
	if !skip && slices.ContainsFunc(links, func(l netlink.Link) bool {
		return l.Attrs().Name == n.VRF && l.Attrs().Alias == kernel.OwnerAlias
	}) {
		return "" // provisioned by open-dci (the state file is gone)
	}
	return fmt.Sprintf("FRR has a BGP instance for vrf %s that open-dci didn't add", n.VRF)
}

// frrVNIs returns the VNIs of the FRR VRFs ("vrf X" / " vni N").
func frrVNIs(running string) map[uint32]string {
	out := map[uint32]string{}
	for _, l := range frr.Parse(running) {
		if len(l.Context) != 1 || !strings.HasPrefix(l.Context[0], "vrf ") {
			continue
		}
		f := strings.Fields(l.Text)
		if len(f) < 2 || f[0] != "vni" {
			continue
		}
		if v, err := strconv.ParseUint(f[1], 10, 32); err == nil {
			out[uint32(v)] = strings.TrimPrefix(l.Context[0], "vrf ")
		}
	}
	return out
}

// hasLine reports whether a top-level line exists in an FRR configuration.
func hasLine(cfg, line string) bool {
	for _, l := range strings.Split(cfg, "\n") {
		if strings.TrimRight(l, " ") == line {
			return true
		}
	}
	return false
}

// Blocked returns the API networks that the last reconcile set aside.
func (g *Gateway) Blocked() []config.Skipped {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.blocked)
}
