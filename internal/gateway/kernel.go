package gateway

import (
	"fmt"

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

// preflightKernel verifies that the VRFs open-dci augments exist. open-dci
// never creates them: they belong to the base system (e.g. metal-networker).
func preflightKernel(cfg *config.Config) error {
	for _, vrf := range allVRFs(cfg) {
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
func ensureKernel(cfg *config.Config) error {
	if err := preflightKernel(cfg); err != nil {
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

// allVRFs returns the tenant VRFs plus the transport VRF (if any).
func allVRFs(cfg *config.Config) []string {
	var out []string
	if cfg.Transport.InVRF() {
		out = append(out, cfg.Transport.VRF)
	}
	for _, n := range cfg.Networks {
		out = append(out, n.VRF)
	}
	return out
}
