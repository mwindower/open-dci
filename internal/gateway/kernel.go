package gateway

import (
	"fmt"
	"net"

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

// preflightKernel verifies that the VRFs open-dci augments exist. It never
// creates them: they belong to the base system (e.g. metal-networker). Only
// provisioned networks (with a vni) get their VRF from open-dci.
func preflightKernel(cfg *config.Config) error {
	for _, vrf := range baseVRFs(cfg) {
		ok, err := kernel.IsVRF(vrf)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("vrf %s does not exist (open-dci only augments existing VRFs)", vrf)
		}
	}
	return nil
}

// ensureKernel applies the kernel part of the gateway. It is idempotent.
//
//   - provisioned L3VNIs: VRF, bridge and VXLAN device per network with a vni
//   - sysctls: forwarding, seg6_enabled, net.vrf.strict_mode
//   - the gateway loopback (<locator>::1) on lo
//
// With the transport in a VRF (DCI network) additionally:
//
//   - the veth pair joining the default VRF and the transport VRF, with fixed
//     link-local addresses used as static-route next hops
//   - MTU of the transport VRF's device chain raised to cfg.Transport.MTU
//     (metal-networker pins 9000, which black-holes full-size SRv6 packets)
//   - the "lookup local" ip rule moved behind the l3mdev rule, so traffic to
//     the loopback that arrives in the transport VRF crosses the veth instead
//     of being answered inside the VRF
func ensureKernel(cfg *config.Config, id frr.Identity) error {
	if err := preflightKernel(cfg); err != nil {
		return err
	}
	for _, v := range l3vnis(cfg, id) {
		if err := kernel.EnsureL3VNI(v); err != nil {
			return fmt.Errorf("vrf %s: %w", v.VRF, err)
		}
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

// baseVRFs returns the VRFs the base system must provide: the augmented
// tenant VRFs plus the transport VRF (if any).
func baseVRFs(cfg *config.Config) []string {
	var out []string
	if cfg.Transport.InVRF() {
		out = append(out, cfg.Transport.VRF)
	}
	for _, n := range cfg.Networks {
		if !n.Provisioned() {
			out = append(out, n.VRF)
		}
	}
	return out
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
		if n.Provisioned() {
			out = append(out, kernel.L3VNI{
				VRF: n.VRF, Table: n.Table, VNI: n.VNI,
				Bridge: n.BridgeName(), Vxlan: n.VxlanName(),
				VTEP: net.ParseIP(vtep), MTU: cfg.Transport.TenantMTU,
			})
		}
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
