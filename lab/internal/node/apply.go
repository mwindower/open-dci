package node

import (
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/vishvananda/netlink"

	"github.com/mwindower/open-dci/internal/kernel"
)

var defaultSysctls = []struct{ key, value string }{
	{"net.ipv4.ip_forward", "1"},
	{"net.ipv6.conf.all.forwarding", "1"},
}

// Apply configures the kernel of the current network namespace according to s.
func Apply(s *Spec, log *slog.Logger) error {
	if err := waitInterfaces(s.WaitInterfaces, 60*time.Second); err != nil {
		return err
	}
	for _, kv := range defaultSysctls {
		if err := kernel.Sysctl(kv.key, kv.value); err != nil {
			return err
		}
	}
	lo, err := netlink.LinkByName("lo")
	if err != nil {
		return err
	}
	if err := kernel.EnsureAddrs(lo, s.Loopback...); err != nil {
		return err
	}
	for _, v := range s.VRFs {
		if err := kernel.EnsureLink(&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: v.Name}, Table: v.Table}, 0); err != nil {
			return fmt.Errorf("vrf %s: %w", v.Name, err)
		}
		log.Info("vrf", "name", v.Name, "table", v.Table)
	}
	if err := applyBridge(s, log); err != nil {
		return err
	}
	for _, i := range s.Interfaces {
		if err := applyInterface(i); err != nil {
			return fmt.Errorf("interface %s: %w", i.Name, err)
		}
		log.Info("interface", "name", i.Name, "vrf", i.VRF, "mtu", i.MTU)
	}
	var rules []kernel.DropRule
	for _, f := range s.EdgeFilter {
		r, _ := f.DropRule() // validated
		rules = append(rules, r)
	}
	if err := kernel.EnsureFilter("lab-edge", rules); err != nil {
		return err
	}
	for k, v := range s.Sysctls {
		if err := kernel.Sysctl(k, v); err != nil {
			return err
		}
	}
	return nil
}

// applyBridge builds metal-networker's layout: one VLAN-aware bridge and per
// L3VNI a vxlan port (VLAN as PVID) plus an SVI in the VRF.
func applyBridge(s *Spec, log *slog.Logger) error {
	if s.Bridge == nil {
		return nil
	}
	yes, none := true, uint16(0)
	br := &netlink.Bridge{
		LinkAttrs:       netlink.LinkAttrs{Name: s.Bridge.Name},
		VlanFiltering:   &yes,
		VlanDefaultPVID: &none,
	}
	if err := kernel.EnsureLink(br, s.Bridge.MTU); err != nil {
		return fmt.Errorf("bridge: %w", err)
	}
	for _, v := range s.VNIs {
		vx := &netlink.Vxlan{
			LinkAttrs: netlink.LinkAttrs{Name: v.VxlanName(), MasterIndex: br.Index},
			VxlanId:   int(v.VNI),
			SrcAddr:   net.ParseIP(v.Local),
			Port:      4789,
			Learning:  false,
		}
		if err := kernel.EnsureLink(vx, v.MTU); err != nil {
			return fmt.Errorf("%s: %w", v.VxlanName(), err)
		}
		// vxlan port: VLAN is PVID + egress untagged; bridge itself: VLAN for the SVI
		if err := netlink.BridgeVlanAdd(vx, v.VLAN, true, true, false, true); err != nil {
			return fmt.Errorf("%s vlan: %w", v.VxlanName(), err)
		}
		if err := netlink.BridgeVlanAdd(br, v.VLAN, false, false, true, false); err != nil {
			return fmt.Errorf("bridge self vlan %d: %w", v.VLAN, err)
		}
		vrf, err := netlink.LinkByName(v.VRF)
		if err != nil {
			return err
		}
		svi := &netlink.Vlan{
			LinkAttrs: netlink.LinkAttrs{Name: v.SVIName(), ParentIndex: br.Index, MasterIndex: vrf.Attrs().Index},
			VlanId:    int(v.VLAN),
		}
		if err := kernel.EnsureLink(svi, v.MTU); err != nil {
			return fmt.Errorf("%s: %w", v.SVIName(), err)
		}
		if err := kernel.EnsureAddrs(svi, v.SVIAddresses...); err != nil {
			return err
		}
		log.Info("vni", "vni", v.VNI, "vlan", v.VLAN, "vrf", v.VRF, "mtu", v.MTU)
	}
	return nil
}

func applyInterface(i Interface) error {
	l, err := netlink.LinkByName(i.Name)
	if err != nil {
		return err
	}
	if err := kernel.SetMTU(l, i.MTU); err != nil {
		return err
	}
	if i.VRF != "" {
		if err := kernel.EnsureMaster(l, i.VRF); err != nil {
			return err
		}
	}
	if err := kernel.EnsureAddrs(l, i.Addresses...); err != nil {
		return err
	}
	return netlink.LinkSetUp(l)
}

func waitInterfaces(names []string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		var missing []string
		for _, n := range names {
			if _, err := netlink.LinkByName(n); err != nil {
				missing = append(missing, n)
			}
		}
		if len(missing) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("interfaces not plumbed after %s: %v", timeout, missing)
		}
		time.Sleep(500 * time.Millisecond)
	}
}
