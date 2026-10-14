# Requirements

open-dci only configures the gateways. Everything around them is the operator's base
configuration, and must provide the following. The reasons behind each item, the exit
topologies and more detail per transport mode:
[Requirements in detail](https://github.com/mwindower/open-dci/blob/main/docs/requirements.md).

**Gateway host**
- Linux ≥ 5.14 (End.DT46) with VRF, VXLAN and nf_tables.
- FRR 10.4 (tested 10.4.1) with bgpd, zebra and staticd, configured through `vtysh`.
  **Not FRR 10.5–10.7** (tested 10.5.1, 10.6.0, 10.6.2, 10.7.1): they keep the VPN and type-5 routes leaked from a tenant VRF after
  their source is withdrawn, so a removed prefix stays routed, into a black hole, in every
  other partition.

**Gateway base FRR config**
- A default BGP instance with a router-id. It peers with each exit the gateway is attached
  to: unnumbered eBGP per uplink, or `address` peers.
- `l2vpn evpn` is activated towards the exits, with `advertise-all-vni`.
- Default-VRF mode only: `ipv6 unicast` is activated towards the exits, to announce the
  locator and the loopback.
- A non-transit outbound filter in `ipv4`/`ipv6 unicast` (only own prefixes, e.g. empty
  AS path), so a dual-attached gateway never routes between its exits.
- Both gateways of a pair use the same ASN.
- DCI-network mode only: the DCI VRF as an EVPN L3VNI (VRF, SVI, VXLAN) with its own BGP
  instance, whose `l2vpn evpn` has `advertise ipv4 unicast` and `advertise ipv6 unicast`.
- open-dci adds everything else: VPN address families, SRv6, tenant VRFs, filters.

**Exits** (towards their gateways)
- `l2vpn evpn` passes the tenant VNIs' type-5 routes and the gateways' VTEPs (underlay).
- `ipv4 vpn` and `ipv6 vpn` are activated on the gateway sessions, with `allowas-in 1`:
  the gateways' VPN routes carry the exit's ASN, since they were learned via EVPN through it.
- ECMP over both gateways of a pair. The locator is shared (anycast).

**Exits** (towards each other and the core)
- `ipv4 vpn` and `ipv6 vpn` relay the VPN routes between the partitions: eBGP multihop
  between exit loopbacks, as a full mesh, a ladder or route servers. Nothing is imported.
  FRR has the VPN families only in the default instance, so the exits need a default-VRF
  path to each other.
- The locators and gateway loopbacks are routed between the partitions as IPv6: in the DCI
  VRF (DCI-network mode) or in the default VRF (default-VRF mode).
- The locator block is never announced into the fabric.

**Exits** (the edge of the SRv6 domain)
- ACLs drop anything from the fabric addressed into the locator block, and anything from
  the core into the block with a source outside it
  ([why](../operation/srv6-domain-edge.md)).

**Core**
- Plain IPv6 with no SRv6 support needed. It carries the locators, gateway loopbacks and
  exit loopbacks, and nothing of the tenants.

**Every link on the transport path**
- MTU ≥ tenant MTU + 48 B (SRv6), plus 50 B where the transport runs in VXLAN (DCI
  network).

**FRR on the transport path** (gateways; exits, spines and core if they run FRR)
- `no zebra nexthop kernel enable`. FRR (10.4, 10.6) otherwise revives a withdrawn next hop when
  its link comes back, and sends traffic to a gateway or exit that isn't ready yet
  ([why](https://github.com/mwindower/open-dci/blob/main/docs/requirements.md#returning-nodes)).

**Recommended: BFD**
- On every session of gateways and exits: gateway ↔ exit, exit ↔ spine, exit ↔ core.
  Without it, a node that hangs with its links up costs a BGP hold time of packet loss
  (lab: ~7–8 s, TCP stalls ~13 s), with it under a second at 300 ms × 3, ~0.3–0.5 s at
  100 ms × 3 ([measurements](https://github.com/mwindower/open-dci/blob/main/docs/performance.md)).
