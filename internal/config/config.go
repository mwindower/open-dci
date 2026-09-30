// Package config defines the open-dci gateway configuration: which tenant VRFs
// of an existing EVPN VTEP (e.g. a metal-stack firewall) are stitched via SRv6
// L3VPN, where the SRv6 transport runs, and who the remote gateways are.
package config

import (
	"fmt"
	"net/netip"
	"os"
	"regexp"
	"strconv"
	"strings"

	"sigs.k8s.io/yaml"
)

// Config is the content of the open-dci config file.
type Config struct {
	// Gateway identifies this gateway.
	Gateway Gateway `json:"gateway"`
	// Transport defines where the SRv6 transport (locators, gateway loopbacks)
	// is routed: in an EVPN VRF (a DCI network) or in the default VRF.
	Transport Transport `json:"transport"`
	// Peers are the remote gateways (VPNv4/v6 sessions).
	Peers []Peer `json:"peers"`
	// Networks are the tenant VRFs to stitch.
	Networks []Network `json:"networks"`
}

type Gateway struct {
	// ASN of the existing "router bgp" instance. Optional: discovered from FRR.
	ASN uint32 `json:"asn,omitempty"`
	// RouterID of the existing BGP instance, used for route distinguishers.
	// Optional: discovered from FRR.
	RouterID string `json:"routerID,omitempty"`
	// Locator is this gateway's SRv6 locator, e.g. fd00:dc1:a::/48. The
	// gateway's loopback (VPN session endpoint, SRv6 encap source) is the
	// first address in it (<locator>::1): function 0 is never used for SIDs.
	Locator string `json:"locator"`
	// LocatorBlock contains the locators of all gateways, e.g. fd00:dc1::/32.
	// Traffic to it is routed into the DCI network.
	LocatorBlock string `json:"locatorBlock"`
	// NodeLength is the locator's node part in bits (default 16). Together
	// with the block length it must equal the locator prefix length.
	NodeLength int `json:"nodeLength,omitempty"`
}

type Transport struct {
	// VRF is an existing EVPN VRF (a "DCI network", e.g. vrf104100) carrying
	// the SRv6 transport through the fabric. open-dci joins it to the default
	// VRF with a veth pair. Empty: the transport is routed in the default VRF
	// (the locator is announced to the default BGP instance's IPv6 peers).
	VRF string `json:"vrf,omitempty"`
	// MTU for the transport VRF's devices (bridge, vxlan, SVI, veth). SRv6
	// adds 48 B (IPv6 + SRH) to tenant packets. Default 9166 (9216 - VXLAN).
	// Only used with VRF; in the default VRF the uplinks' MTU is not managed.
	MTU int `json:"mtu,omitempty"`
	// TenantMTU is the largest tenant packet (default 9000). MTU must be at
	// least TenantMTU + 48, otherwise full-size packets are black-holed.
	TenantMTU int `json:"tenantMTU,omitempty"`
	// Veth names the veth pair between the default VRF and the transport VRF
	// (default dci0 / dci1).
	Veth     string `json:"veth,omitempty"`
	VethPeer string `json:"vethPeer,omitempty"`
}

// InVRF reports whether the transport runs in a (DCI network) VRF.
func (t Transport) InVRF() bool { return t.VRF != "" }

type Peer struct {
	// Address is the remote gateway's loopback (<its locator>::1).
	Address string `json:"address"`
	ASN     uint32 `json:"asn"`
}

type Network struct {
	// VRF is the existing tenant VRF, e.g. vrf3981.
	VRF string `json:"vrf"`
	// RouteTarget identifies the stitched network across all partitions,
	// e.g. 65535:1001. It must be the same on all gateways of this network.
	RouteTarget string `json:"routeTarget"`
	// RD is the route distinguisher. Default: <routerID>:<RT local part>.
	RD string `json:"rd,omitempty"`
}

const (
	SRv6Overhead     = 48 // IPv6 header + SRH with one segment
	DefaultMTU       = 9166
	DefaultTenantMTU = 9000
	LocatorName      = "DCI"
)

// Load reads, defaults and validates a config file.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(raw)
}

// Parse parses, defaults and validates raw YAML.
func Parse(raw []byte) (*Config, error) {
	var c Config
	if err := yaml.UnmarshalStrict(raw, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	c.Default()
	return &c, c.Validate()
}

// Default fills in default values.
func (c *Config) Default() {
	if c.Gateway.NodeLength == 0 {
		c.Gateway.NodeLength = 16
	}
	if c.Transport.MTU == 0 {
		c.Transport.MTU = DefaultMTU
	}
	if c.Transport.TenantMTU == 0 {
		c.Transport.TenantMTU = DefaultTenantMTU
	}
	if c.Transport.Veth == "" {
		c.Transport.Veth = "dci0"
	}
	if c.Transport.VethPeer == "" {
		c.Transport.VethPeer = "dci1"
	}
}

var (
	vrfName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,14}$`)
	ifName  = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,15}$`)
)

// Validate checks the config for consistency. It does not look at the system;
// see the gateway package for checks against the running kernel and FRR.
func (c *Config) Validate() error {
	var errs []string
	fail := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }

	block, err := netip.ParsePrefix(c.Gateway.LocatorBlock)
	if err != nil || !block.Addr().Is6() {
		fail("gateway.locatorBlock: must be an IPv6 prefix: %q", c.Gateway.LocatorBlock)
	}
	loc, err := netip.ParsePrefix(c.Gateway.Locator)
	if err != nil || !loc.Addr().Is6() {
		fail("gateway.locator: must be an IPv6 prefix: %q", c.Gateway.Locator)
	} else {
		if loc != loc.Masked() {
			fail("gateway.locator: %s has host bits set", loc)
		}
		if block.IsValid() && (loc.Bits() <= block.Bits() || !block.Contains(loc.Addr())) {
			fail("gateway.locator: %s is not inside gateway.locatorBlock %s", loc, block)
		}
		if block.IsValid() && block.Bits()+c.Gateway.NodeLength != loc.Bits() {
			fail("gateway.nodeLength: block /%d + node %d bits must equal the locator length /%d", block.Bits(), c.Gateway.NodeLength, loc.Bits())
		}
		if loc.Bits()+16 > 128 {
			fail("gateway.locator: /%d leaves no room for 16 function bits", loc.Bits())
		}
	}
	if c.Gateway.RouterID != "" {
		if a, err := netip.ParseAddr(c.Gateway.RouterID); err != nil || !a.Is4() {
			fail("gateway.routerID: must be an IPv4 address: %q", c.Gateway.RouterID)
		}
	}

	if c.Transport.InVRF() && !vrfName.MatchString(c.Transport.VRF) {
		fail("transport.vrf: invalid VRF name %q", c.Transport.VRF)
	}
	if min := c.Transport.TenantMTU + SRv6Overhead; c.Transport.MTU < min {
		fail("transport.mtu: %d is too small, tenant MTU %d + %d B SRv6 needs at least %d", c.Transport.MTU, c.Transport.TenantMTU, SRv6Overhead, min)
	}
	for _, n := range []string{c.Transport.Veth, c.Transport.VethPeer} {
		if !ifName.MatchString(n) {
			fail("transport: invalid veth name %q", n)
		}
	}
	if c.Transport.Veth == c.Transport.VethPeer {
		fail("transport: veth and vethPeer must differ")
	}

	if len(c.Peers) == 0 {
		fail("peers: at least one remote gateway is required")
	}
	seenPeer := map[netip.Addr]bool{}
	for i, p := range c.Peers {
		a, err := netip.ParseAddr(p.Address)
		switch {
		case err != nil || !a.Is6():
			fail("peers[%d].address: must be an IPv6 address: %q", i, p.Address)
		case seenPeer[a]:
			fail("peers[%d].address: duplicate %s", i, a)
		case loc.IsValid() && loc.Contains(a):
			fail("peers[%d].address: %s is inside the own locator", i, a)
		case block.IsValid() && !block.Contains(a):
			fail("peers[%d].address: %s is outside gateway.locatorBlock %s (remote loopbacks live in their locators)", i, a, block)
		}
		seenPeer[a] = true
		if p.ASN == 0 {
			fail("peers[%d].asn: required", i)
		}
	}

	if len(c.Networks) == 0 {
		fail("networks: at least one tenant VRF is required")
	}
	seenVRF := map[string]bool{}
	if c.Transport.InVRF() {
		seenVRF[c.Transport.VRF] = true
	}
	for i, n := range c.Networks {
		if !vrfName.MatchString(n.VRF) {
			fail("networks[%d].vrf: invalid VRF name %q", i, n.VRF)
		}
		if seenVRF[n.VRF] {
			fail("networks[%d].vrf: %q used twice (or is the transport VRF)", i, n.VRF)
		}
		seenVRF[n.VRF] = true
		if _, _, err := splitCommunity(n.RouteTarget); err != nil {
			fail("networks[%d].routeTarget: %v", i, err)
		}
		if n.RD != "" {
			if _, _, err := splitCommunity(n.RD); err != nil {
				fail("networks[%d].rd: %v", i, err)
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("invalid config:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

// Loopback returns the gateway loopback, the first address in the locator.
func (g Gateway) Loopback() netip.Addr {
	return netip.MustParsePrefix(g.Locator).Masked().Addr().Next()
}

// RDFor returns the network's route distinguisher (explicit or derived).
func (c *Config) RDFor(n Network, routerID string) string {
	if n.RD != "" {
		return n.RD
	}
	_, local, _ := splitCommunity(n.RouteTarget)
	return routerID + ":" + local
}

// splitCommunity accepts ASN:NN or IPv4:NN.
func splitCommunity(s string) (string, string, error) {
	admin, local, ok := strings.Cut(s, ":")
	if !ok {
		return "", "", fmt.Errorf("%q: want <asn>:<nn> or <ipv4>:<nn>", s)
	}
	if _, err := strconv.ParseUint(local, 10, 32); err != nil {
		return "", "", fmt.Errorf("%q: local part must be a number", s)
	}
	if _, err := strconv.ParseUint(admin, 10, 32); err != nil {
		if a, err := netip.ParseAddr(admin); err != nil || !a.Is4() {
			return "", "", fmt.Errorf("%q: admin part must be an ASN or IPv4 address", s)
		}
	}
	return admin, local, nil
}
