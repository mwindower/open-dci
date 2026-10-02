// Package config defines the open-dci gateway configuration: which tenant VRFs
// the gateway provisions as EVPN L3VNIs and stitches via SRv6 L3VPN, where the
// SRv6 transport runs, and who the remote gateways are.
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
	// Locator is this gateway's SRv6 locator, e.g. fd00:dc1:a::/48. Redundant
	// gateways of a partition share it (anycast): with pinned SIDs
	// (networks[].sid) they announce identical SIDs.
	Locator string `json:"locator"`
	// LoopbackAddress is the gateway's own address: VPN session endpoint and
	// SRv6 encap source. Default: the first address in the locator
	// (<locator>::1; function 0 is never used for SIDs). Redundant gateways
	// sharing a locator need a unique one outside of it, inside the block.
	LoopbackAddress string `json:"loopback,omitempty"`
	// LocatorBlock contains the locators of all gateways, e.g. fd00:dc1::/32.
	// Traffic to it is routed into the DCI network.
	LocatorBlock string `json:"locatorBlock"`
	// NodeLength is the locator's node part in bits (default 16). Together
	// with the block length it must equal the locator prefix length.
	NodeLength int `json:"nodeLength,omitempty"`
	// VTEP is the VXLAN source address of the provisioned L3VNIs. Optional:
	// defaults to the BGP router-id.
	VTEP string `json:"vtep,omitempty"`
}

type Transport struct {
	// VRF is an existing EVPN VRF of the gateway's base config (a "DCI
	// network", e.g. vrf104100) carrying the SRv6 transport through the
	// fabric. open-dci joins it to the default
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

// Peer is a BGP session that carries the VPN routes, in one of two forms:
//   - Interface: the base config's existing session over that interface, to
//     the exit; the exits relay the VPN routes between partitions. open-dci
//     only activates VPNv4/v6 (with its filters) on it.
//   - Address + ASN: a direct (multihop) session to a remote gateway's
//     loopback, i.e. a full mesh between the gateways.
type Peer struct {
	Interface string `json:"interface,omitempty"`
	// Address is the remote gateway's loopback.
	Address string `json:"address,omitempty"`
	ASN     uint32 `json:"asn,omitempty"`
	// MaxPrefixes limits the VPN prefixes accepted from the peer per address
	// family (default 10000). FRR tears the session down when it is exceeded.
	// A session to the exit carries the routes of all partitions.
	MaxPrefixes int `json:"maxPrefixes,omitempty"`
}

type Network struct {
	// VRF is the tenant VRF open-dci creates, e.g. vrf3981.
	VRF string `json:"vrf"`
	// RouteTarget identifies the stitched network across all partitions,
	// e.g. 65535:1001. It must be the same on all gateways of this network.
	RouteTarget string `json:"routeTarget"`
	// RD is the route distinguisher. Default: <routerID>:<RT local part>.
	RD string `json:"rd,omitempty"`
	// Aggregates are address ranges of the stitched network, each in exactly
	// one partition ("10.0.16.0/24"). The gateways of the partition whose
	// tenant VRF has more specific routes in a range announce the range
	// instead of them and drop traffic to its unused addresses. A range
	// without such routes isn't announced, so the list is the same on all
	// gateways of the network. Aggregates are part of the allowlist.
	Aggregates []string `json:"aggregates,omitempty"`
	// Prefixes are further routes of the stitched network, passed as they
	// are, in FRR prefix-list syntax ("10.0.16.0/24 le 32"). Only routes
	// matching an aggregate or a prefix are exported from and imported into
	// the VRF; everything else, including a default route unless listed,
	// stays in its partition. Like the route target, the list is the same on
	// all gateways of the network.
	Prefixes []string `json:"prefixes,omitempty"`
	// DefaultRoute shares a default route (0.0.0.0/0, ::/0) between the
	// partitions of the network, set per gateway: "export" sends the default
	// route from the own fabric (e.g. the partition's internet breakout) into
	// the VPN, "import" takes over a remote partition's one as a fallback
	// (a default route from the own fabric wins). Empty: the default route
	// stays in its partition.
	DefaultRoute string `json:"defaultRoute,omitempty"`
	// VNI is the tenant's L3VNI in this partition. open-dci provisions the
	// VRF with it: VRF, bridge, VXLAN device and the FRR VRF with its BGP
	// instance, which joins the partition's EVPN.
	VNI uint32 `json:"vni"`
	// Table is the kernel routing table of the VRF (default: the VNI).
	Table uint32 `json:"table,omitempty"`
	// SID is the function part of the network's End.DT46 SID
	// (<locator>:<sid in hex>::), pinned so that it survives restarts and is
	// identical on redundant gateways. Default: the VNI (if it fits 16 bits).
	SID uint32 `json:"sid,omitempty"`
}

// Device names of a provisioned L3VNI: a plain (not VLAN-aware) bridge per VNI
// acts as the SVI, so no VLAN IDs have to be allocated.
func (n Network) BridgeName() string { return fmt.Sprintf("dcibr%d", n.VNI) } // kernel.L3VNIBridgePrefix
func (n Network) VxlanName() string  { return fmt.Sprintf("dcivx%d", n.VNI) }

const (
	SRv6Overhead     = 48 // IPv6 header + SRH with one segment
	DefaultMTU       = 9166
	DefaultTenantMTU = 9000
	LocatorName      = "DCI"
	// DefaultMaxPrefixes is the default VPN prefix limit per peer and family.
	DefaultMaxPrefixes = 10000
	MaxVNI             = 1<<24 - 1
	MaxSIDFunction     = 1<<16 - 1 // 16 function bits
)

// Tables the kernel reserves (unspec, default, main, local).
var reservedTables = map[uint32]bool{0: true, 253: true, 254: true, 255: true}

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
	for i := range c.Networks {
		n := &c.Networks[i]
		if n.Table == 0 {
			n.Table = n.VNI
		}
		if n.SID == 0 && n.VNI <= MaxSIDFunction {
			n.SID = n.VNI
		}
	}
	for i := range c.Peers {
		if p := &c.Peers[i]; p.MaxPrefixes == 0 {
			p.MaxPrefixes = DefaultMaxPrefixes
		}
	}
}

// PrefixRule is one entry of a network's prefix allowlist.
type PrefixRule struct {
	Prefix netip.Prefix
	Ge, Le int // 0 = not set
}

// String renders the rule in FRR's canonical prefix-list form.
func (r PrefixRule) String() string {
	s := r.Prefix.String()
	if r.Ge != 0 {
		s += fmt.Sprintf(" ge %d", r.Ge)
	}
	if r.Le != 0 {
		s += fmt.Sprintf(" le %d", r.Le)
	}
	return s
}

// ParsePrefixRule parses "PREFIX [ge N] [le N]": the prefix alone matches
// exactly, ge/le extend it to more-specific prefixes as in FRR.
func ParsePrefixRule(s string) (PrefixRule, error) {
	f := strings.Fields(s)
	if len(f) == 0 {
		return PrefixRule{}, fmt.Errorf("empty prefix")
	}
	var r PrefixRule
	p, err := netip.ParsePrefix(f[0])
	if err != nil {
		return r, fmt.Errorf("%q: %v", s, err)
	}
	if p != p.Masked() {
		return r, fmt.Errorf("%q: host bits set", s)
	}
	r.Prefix = p
	max := p.Addr().BitLen()
	for rest := f[1:]; len(rest) > 0; rest = rest[2:] {
		if len(rest) < 2 {
			return r, fmt.Errorf("%q: want PREFIX [ge N] [le N]", s)
		}
		n, err := strconv.Atoi(rest[1])
		if err != nil || n <= p.Bits() || n > max {
			return r, fmt.Errorf("%q: %s must be between %d and %d", s, rest[0], p.Bits()+1, max)
		}
		switch {
		case rest[0] == "ge" && r.Ge == 0 && r.Le == 0:
			r.Ge = n
		case rest[0] == "le" && r.Le == 0:
			r.Le = n
		default:
			return r, fmt.Errorf("%q: want PREFIX [ge N] [le N]", s)
		}
	}
	if r.Ge != 0 && r.Le != 0 && r.Ge > r.Le {
		return r, fmt.Errorf("%q: ge must not be larger than le", s)
	}
	return r, nil
}

// Values of Network.DefaultRoute.
const (
	DefaultExport = "export"
	DefaultImport = "import"
)

// AggregatePrefixes returns the parsed aggregates, IPv4 and IPv6 separately.
// The config must have been validated.
func (n Network) AggregatePrefixes() (v4, v6 []netip.Prefix) {
	for _, s := range n.Aggregates {
		p, _ := ParseAggregate(s)
		if p.Addr().Is4() {
			v4 = append(v4, p)
		} else {
			v6 = append(v6, p)
		}
	}
	return v4, v6
}

// ParseAggregate parses an aggregate: a prefix without host bits, shorter
// than a host route and not a default route.
func ParseAggregate(s string) (netip.Prefix, error) {
	p, err := netip.ParsePrefix(strings.TrimSpace(s))
	switch {
	case err != nil:
		return p, fmt.Errorf("%q: want a plain prefix: %v", s, err)
	case p != p.Masked():
		return p, fmt.Errorf("%q: host bits set", s)
	case p.Bits() == 0:
		return p, fmt.Errorf("%q: a default route can't be an aggregate", s)
	case p.Bits() == p.Addr().BitLen():
		return p, fmt.Errorf("%q: a host route can't be an aggregate", s)
	}
	return p, nil
}

// PrefixRules returns the parsed allowlist, IPv4 and IPv6 separately: the
// aggregates (exact match), then the prefixes. The config must have been
// validated.
func (n Network) PrefixRules() (v4, v6 []PrefixRule) {
	for _, s := range n.Aggregates {
		p, _ := ParseAggregate(s)
		if p.Addr().Is4() {
			v4 = append(v4, PrefixRule{Prefix: p})
		} else {
			v6 = append(v6, PrefixRule{Prefix: p})
		}
	}
	for _, s := range n.Prefixes {
		r, _ := ParsePrefixRule(s)
		if r.Prefix.Addr().Is4() {
			v4 = append(v4, r)
		} else {
			v6 = append(v6, r)
		}
	}
	return v4, v6
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
	if c.Gateway.LoopbackAddress != "" {
		a, err := netip.ParseAddr(c.Gateway.LoopbackAddress)
		switch {
		case err != nil || !a.Is6():
			fail("gateway.loopback: must be an IPv6 address: %q", c.Gateway.LoopbackAddress)
		case loc.IsValid() && loc.Contains(a):
			fail("gateway.loopback: %s is inside the locator; leave it empty for <locator>::1, or pick a unique address outside the (possibly shared) locator", a)
		case block.IsValid() && !block.Contains(a):
			fail("gateway.loopback: %s is outside gateway.locatorBlock %s", a, block)
		}
	}
	if c.Gateway.VTEP != "" {
		if a, err := netip.ParseAddr(c.Gateway.VTEP); err != nil || !a.Is4() {
			fail("gateway.vtep: must be an IPv4 address: %q", c.Gateway.VTEP)
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
	seenIf := map[string]bool{}
	for i, p := range c.Peers {
		if p.MaxPrefixes < 1 {
			fail("peers[%d].maxPrefixes: must be at least 1", i)
		}
		if p.Interface != "" {
			switch {
			case p.Address != "" || p.ASN != 0:
				fail("peers[%d]: either interface (the session to the exit) or address and asn", i)
			case !ifName.MatchString(p.Interface):
				fail("peers[%d].interface: invalid interface name %q", i, p.Interface)
			case seenIf[p.Interface]:
				fail("peers[%d].interface: %s used twice", i, p.Interface)
			}
			seenIf[p.Interface] = true
			continue
		}
		a, err := netip.ParseAddr(p.Address)
		switch {
		case err != nil || !a.Is6():
			fail("peers[%d].address: must be an IPv6 address: %q", i, p.Address)
		case seenPeer[a]:
			fail("peers[%d].address: duplicate %s", i, a)
		case loc.IsValid() && loc.Contains(a):
			fail("peers[%d].address: %s is inside the own locator", i, a)
		case c.Gateway.LoopbackAddress != "" && a.String() == c.Gateway.LoopbackAddress:
			fail("peers[%d].address: %s is the own loopback", i, a)
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
	seenVNI := map[uint32]bool{}
	seenSID := map[uint32]bool{}
	seenTable := map[uint32]bool{}
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
		switch {
		case n.VNI == 0:
			fail("networks[%d].vni: required", i)
		case n.VNI > MaxVNI:
			fail("networks[%d].vni: %d is larger than %d", i, n.VNI, MaxVNI)
		case seenVNI[n.VNI]:
			fail("networks[%d].vni: %d used twice", i, n.VNI)
		case reservedTables[n.Table]:
			fail("networks[%d].table: %d is reserved by the kernel, set another table", i, n.Table)
		case seenTable[n.Table]:
			fail("networks[%d].table: %d used twice", i, n.Table)
		}
		seenVNI[n.VNI], seenTable[n.Table] = true, true
		switch {
		case n.SID == 0:
			fail("networks[%d].sid: required when the vni doesn't fit 16 bits", i)
		case n.SID > MaxSIDFunction:
			fail("networks[%d].sid: %d is larger than %d (16 function bits)", i, n.SID, MaxSIDFunction)
		case seenSID[n.SID]:
			fail("networks[%d].sid: %d used twice", i, n.SID)
		}
		seenSID[n.SID] = true
		if len(n.Prefixes) == 0 && len(n.Aggregates) == 0 {
			fail("networks[%d]: at least one of aggregates and prefixes is required (nothing is exchanged otherwise)", i)
		}
		switch n.DefaultRoute {
		case "", DefaultExport, DefaultImport:
		default:
			fail("networks[%d].defaultRoute: %q, want %q or %q", i, n.DefaultRoute, DefaultExport, DefaultImport)
		}
		seenRule := map[PrefixRule]bool{}
		var aggs []netip.Prefix
		for j, s := range n.Aggregates {
			p, err := ParseAggregate(s)
			if err != nil {
				fail("networks[%d].aggregates[%d]: %v", i, j, err)
				continue
			}
			for _, q := range aggs {
				if p.Overlaps(q) {
					fail("networks[%d].aggregates[%d]: %s overlaps %s", i, j, p, q)
				}
			}
			aggs = append(aggs, p)
			seenRule[PrefixRule{Prefix: p}] = true
		}
		for j, s := range n.Prefixes {
			r, err := ParsePrefixRule(s)
			switch {
			case err != nil:
				fail("networks[%d].prefixes[%d]: %v", i, j, err)
			case seenRule[r]:
				fail("networks[%d].prefixes[%d]: %s listed twice (aggregates are part of the allowlist)", i, j, r)
			case n.DefaultRoute != "" && r.Prefix.Bits() == 0:
				fail("networks[%d].prefixes[%d]: %s matches the default route, which defaultRoute already handles", i, j, r)
			}
			seenRule[r] = true
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("invalid config:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

// Loopback returns the gateway loopback: the configured one, or the first
// address in the locator.
func (g Gateway) Loopback() netip.Addr {
	if g.LoopbackAddress != "" {
		return netip.MustParseAddr(g.LoopbackAddress)
	}
	return netip.MustParsePrefix(g.Locator).Masked().Addr().Next()
}

// Neighbor is the peer's name in FRR: the interface or the address.
func (p Peer) Neighbor() string {
	if p.Interface != "" {
		return p.Interface
	}
	return p.Address
}

// Anycast reports whether the loopback lies outside the locator, i.e. the
// locator may be shared with redundant gateways. The loopback then has to be
// announced on its own.
func (g Gateway) Anycast() bool { return g.LoopbackAddress != "" }

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
