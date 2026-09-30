// Package node describes and applies the kernel network setup of a single lab
// container: every node gets a declarative node.yaml that labnode applies via
// netlink before starting FRR.
//
// The device layout follows metal-networker's convention: one VLAN-aware
// bridge ("bridge"), and per EVPN network a vxlan device "vni<VNI>", an SVI
// "vlan<VNI>" and a VRF "vrf<VNI>". On the gateways, node.yaml holds only the
// operator's base setup; the tenant VRFs and DCI parts are added by open-dci at
// runtime (see Sidecars).
package node

import (
	"fmt"
	"net/netip"
	"os"

	"sigs.k8s.io/yaml"
)

// Spec is the content of a node.yaml.
type Spec struct {
	// WaitInterfaces lists interfaces containerlab plumbs after container start.
	WaitInterfaces []string `json:"waitInterfaces,omitempty"`
	// Sysctls are applied in addition to the defaults (forwarding).
	Sysctls map[string]string `json:"sysctls,omitempty"`
	// Loopback addresses (CIDR) added to lo.
	Loopback []string `json:"loopback,omitempty"`
	// VRFs to create.
	VRFs []VRF `json:"vrfs,omitempty"`
	// Bridge is the single VLAN-aware bridge carrying all VNIs.
	Bridge *Bridge `json:"bridge,omitempty"`
	// VNIs are EVPN L3VNIs: vxlan device + bridge VLAN + SVI in a VRF.
	VNIs []VNI `json:"vnis,omitempty"`
	// Interfaces are existing (containerlab) interfaces to configure.
	Interfaces []Interface `json:"interfaces,omitempty"`
	// Sidecars are commands started in the background right before FRR,
	// e.g. open-dci on the gateways. Their output goes to the container log.
	Sidecars [][]string `json:"sidecars,omitempty"`
}

type VRF struct {
	Name  string `json:"name"`
	Table uint32 `json:"table"`
}

type Bridge struct {
	Name string `json:"name"`
	MTU  int    `json:"mtu,omitempty"`
}

type VNI struct {
	VNI          uint32   `json:"vni"`
	VLAN         uint16   `json:"vlan"`
	VRF          string   `json:"vrf"`
	Local        string   `json:"local"` // VTEP source address
	MTU          int      `json:"mtu,omitempty"`
	SVIAddresses []string `json:"sviAddresses,omitempty"`
}

func (v VNI) VxlanName() string { return fmt.Sprintf("vni%d", v.VNI) }
func (v VNI) SVIName() string   { return fmt.Sprintf("vlan%d", v.VNI) }

type Interface struct {
	Name      string   `json:"name"`
	MTU       int      `json:"mtu,omitempty"`
	VRF       string   `json:"vrf,omitempty"`
	Addresses []string `json:"addresses,omitempty"`
}

// Load reads and validates a node.yaml.
func Load(path string) (*Spec, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Spec
	if err := yaml.UnmarshalStrict(raw, &s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &s, s.Validate()
}

// Validate catches config mistakes before anything touches the kernel.
func (s *Spec) Validate() error {
	vrfs := map[string]bool{}
	tables := map[uint32]bool{}
	for _, v := range s.VRFs {
		if vrfs[v.Name] || tables[v.Table] {
			return fmt.Errorf("duplicate vrf name or table: %s/%d", v.Name, v.Table)
		}
		vrfs[v.Name], tables[v.Table] = true, true
	}
	if len(s.VNIs) > 0 && s.Bridge == nil {
		return fmt.Errorf("vnis require a bridge")
	}
	for _, v := range s.VNIs {
		if !vrfs[v.VRF] {
			return fmt.Errorf("vni %d: unknown vrf %q", v.VNI, v.VRF)
		}
		if _, err := netip.ParseAddr(v.Local); err != nil {
			return fmt.Errorf("vni %d: local: %w", v.VNI, err)
		}
		if err := prefixes(v.SVIAddresses); err != nil {
			return fmt.Errorf("vni %d: %w", v.VNI, err)
		}
	}
	for _, i := range s.Interfaces {
		if i.VRF != "" && !vrfs[i.VRF] {
			return fmt.Errorf("interface %s: unknown vrf %q", i.Name, i.VRF)
		}
		if err := prefixes(i.Addresses); err != nil {
			return fmt.Errorf("interface %s: %w", i.Name, err)
		}
	}
	for _, c := range s.Sidecars {
		if len(c) == 0 {
			return fmt.Errorf("empty sidecar command")
		}
	}
	return prefixes(s.Loopback)
}

func prefixes(ps []string) error {
	for _, p := range ps {
		if _, err := netip.ParsePrefix(p); err != nil {
			return err
		}
	}
	return nil
}
