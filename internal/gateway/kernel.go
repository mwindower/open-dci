package gateway

import (
	"fmt"

	"github.com/vishvananda/netlink"

	"github.com/mwindower/srv6-dci/internal/config"
	"github.com/mwindower/srv6-dci/internal/frr"
	"github.com/mwindower/srv6-dci/internal/kernel"
)

var sysctls = []struct{ key, value string }{
	{"net.ipv4.ip_forward", "1"},
	{"net.ipv6.conf.all.forwarding", "1"},
	{"net.ipv6.conf.all.seg6_enabled", "1"},
	{"net.ipv6.conf.default.seg6_enabled", "1"},
	// required for End.DT4/DT46; only exists once a VRF exists (checked before)
	{"net.vrf.strict_mode", "1"},
}

// preflightKernel verifies that the VRFs srv6-dci augments exist. srv6-dci
// never creates them: they belong to the base system (metal-networker).
func preflightKernel(cfg *config.Config) error {
	for _, vrf := range append([]string{cfg.DCINetwork.VRF}, vrfNames(cfg)...) {
		ok, err := kernel.IsVRF(vrf)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("vrf %s does not exist (srv6-dci only augments existing VRFs)", vrf)
		}
	}
	return nil
}

// ensureKernel applies the kernel part of the gateway. It is idempotent.
//
//   - sysctls: forwarding, seg6_enabled, net.vrf.strict_mode
//   - the gateway loopback (<locator>::1) on lo
//   - the veth pair joining the default VRF and the DCI VRF, with fixed
//     link-local addresses used as static-route next hops
//   - MTU of the DCI network's device chain raised to cfg.DCINetwork.MTU
//     (metal-networker pins 9000, which black-holes full-size SRv6 packets)
//   - the "lookup local" ip rule moved behind the l3mdev rule, so traffic to
//     the loopback that arrives in the DCI VRF crosses the veth instead of
//     being answered inside the VRF
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
	path, err := kernel.FindVNIPath(cfg.DCINetwork.VRF)
	if err != nil {
		return fmt.Errorf("dci network: %w", err)
	}
	if err := path.RaiseMTU(cfg.DCINetwork.MTU); err != nil {
		return fmt.Errorf("dci network: %w", err)
	}
	if err := kernel.EnsureVeth(kernel.Veth{
		Name:               cfg.DCINetwork.Veth,
		Addresses:          []string{frr.VethLL + "/64"},
		Peer:               cfg.DCINetwork.VethPeer,
		PeerVRF:            cfg.DCINetwork.VRF,
		PeerAddresses:      []string{frr.VethPeerLL + "/64"},
		MTU:                cfg.DCINetwork.MTU,
		NoLinkLocalAutoGen: true,
	}); err != nil {
		return err
	}
	return kernel.MoveLocalRule()
}

func vrfNames(cfg *config.Config) []string {
	var out []string
	for _, n := range cfg.Networks {
		out = append(out, n.VRF)
	}
	return out
}
