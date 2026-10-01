package gateway

import (
	"fmt"
	"net"
	"net/netip"

	"github.com/vishvananda/netlink"

	"github.com/mwindower/open-dci/internal/config"
	"github.com/mwindower/open-dci/internal/frr"
	"github.com/mwindower/open-dci/internal/kernel"
)

var sysctls = []struct{ key, value string }{
	{"net.ipv4.ip_forward", "1"},
	{"net.ipv6.conf.all.forwarding", "1"},
	{"net.ipv6.conf.all.seg6_enabled", "1"},
	{"net.ipv6.conf.default.seg6_enabled", "1"},
	// required for End.DT4/DT46; only exists once a VRF exists (checked before)
	{"net.vrf.strict_mode", "1"},
}

// preflightKernel verifies that the transport VRF (if any) exists. It belongs
// to the gateway's base config; open-dci only creates the tenant VRFs.
func preflightKernel(cfg *config.Config) error {
	if !cfg.Transport.InVRF() {
		return nil
	}
	ok, err := kernel.IsVRF(cfg.Transport.VRF)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("transport vrf %s does not exist (it belongs to the base config)", cfg.Transport.VRF)
	}
	return nil
}

// ensureKernel applies the kernel part of the gateway. It is idempotent.
//
//   - provisioned L3VNIs: VRF, bridge and VXLAN device per network; in their
//     tables "unreachable default" (no fall-through to main) and "throw
//     <locatorBlock>" (the outer lookup of SRv6 encapsulation needs main)
//   - the ingress filter at the gateway's edge of the SRv6 domain (FilterRules)
//   - sysctls: forwarding, seg6_enabled, net.vrf.strict_mode
//   - the gateway loopback (<locator>::1) on lo
//
// With the transport in a VRF (DCI network) additionally:
//
//   - the veth pair joining the default VRF and the transport VRF, with fixed
//     link-local addresses used as static-route next hops
//   - MTU of the transport VRF's device chain raised to cfg.Transport.MTU
//     (a base config with 9000 black-holes full-size SRv6 packets)
//   - the "lookup local" ip rule moved behind the l3mdev rule, so traffic to
//     the loopback that arrives in the transport VRF crosses the veth instead
//     of being answered inside the VRF
func ensureKernel(cfg *config.Config, id frr.Identity) error {
	if err := preflightKernel(cfg); err != nil {
		return err
	}
	block := netip.MustParsePrefix(cfg.Gateway.LocatorBlock)
	for _, v := range l3vnis(cfg, id) {
		if err := kernel.EnsureL3VNI(v); err != nil {
			return fmt.Errorf("vrf %s: %w", v.VRF, err)
		}
		if err := kernel.EnsureThrow(v.Table, block); err != nil {
			return fmt.Errorf("vrf %s: %w", v.VRF, err)
		}
	}
	if err := kernel.EnsureFilter(FilterTable, FilterRules(cfg)); err != nil {
		return err
	}
	for _, s := range sysctls {
		if err := kernel.Sysctl(s.key, s.value); err != nil {
			return err
		}
	}
	lo, err := netlink.LinkByName("lo")
	if err != nil {
		return err
	}
	if err := kernel.EnsureAddrs(lo, cfg.Gateway.Loopback().String()+"/128"); err != nil {
		return err
	}
	if !cfg.Transport.InVRF() {
		return nil
	}
	path, err := kernel.FindVNIPath(cfg.Transport.VRF)
	if err != nil {
		return fmt.Errorf("transport vrf: %w", err)
	}
	if err := path.RaiseMTU(cfg.Transport.MTU); err != nil {
		return fmt.Errorf("transport vrf: %w", err)
	}
	if err := kernel.EnsureVeth(kernel.Veth{
		Name:               cfg.Transport.Veth,
		Addresses:          []string{frr.VethLL + "/64"},
		Peer:               cfg.Transport.VethPeer,
		PeerVRF:            cfg.Transport.VRF,
		PeerAddresses:      []string{frr.VethPeerLL + "/64"},
		MTU:                cfg.Transport.MTU,
		NoLinkLocalAutoGen: true,
	}); err != nil {
		return err
	}
	return kernel.MoveLocalRule()
}

// FilterTable is the nftables (ip6) table of the gateway's ingress filter.
const FilterTable = "open-dci"

// FilterRules is the gateway's part of filtering at the edge of the SRv6
// domain. Tenants must never reach the transport (the other tenants' SIDs
// would decapsulate their packets into foreign VRFs), and the own SIDs and
// loopback only accept packets from inside the locator block. Exempt, since
// they can't come from outside the domain: the gateway's own packets (via lo),
// and link-local sources, which only the attached link can send (the own DCI
// VRF via the veth, or the exit), e.g. ICMPv6 errors to the encap source.
func FilterRules(cfg *config.Config) []kernel.DropRule {
	block := netip.MustParsePrefix(cfg.Gateway.LocatorBlock)
	ll := netip.MustParsePrefix("fe80::/10")
	rules := []kernel.DropRule{
		{Name: "tenant-to-transport", IifPrefix: kernel.L3VNIBridgePrefix, Daddr: block},
		{Name: "locator-from-outside", ExceptIif: "lo", Daddr: netip.MustParsePrefix(cfg.Gateway.Locator), SaddrNot: block, ExceptSaddr: ll},
	}
	if cfg.Gateway.Anycast() {
		rules = append(rules, kernel.DropRule{
			Name: "loopback-from-outside", ExceptIif: "lo", Daddr: netip.PrefixFrom(cfg.Gateway.Loopback(), 128), SaddrNot: block, ExceptSaddr: ll,
		})
	}
	return rules
}

// l3vnis returns the L3VNIs open-dci provisions. The VTEP defaults to the
// BGP router-id.
func l3vnis(cfg *config.Config, id frr.Identity) []kernel.L3VNI {
	vtep := cfg.Gateway.VTEP
	if vtep == "" {
		vtep = id.RouterID
	}
	var out []kernel.L3VNI
	for _, n := range cfg.Networks {
		out = append(out, kernel.L3VNI{
			VRF: n.VRF, Table: n.Table, VNI: n.VNI,
			Bridge: n.BridgeName(), Vxlan: n.VxlanName(),
			VTEP: net.ParseIP(vtep), MTU: cfg.Transport.TenantMTU,
		})
	}
	return out
}

// removeStaleL3VNIs deletes provisioned devices that are no longer
// configured and returns the deleted VRFs.
func removeStaleL3VNIs(cfg *config.Config, id frr.Identity) ([]string, error) {
	keep := map[string]bool{}
	for _, v := range l3vnis(cfg, id) {
		keep[v.VRF], keep[v.Bridge], keep[v.Vxlan] = true, true, true
	}
	return kernel.RemoveL3VNIs(keep)
}
