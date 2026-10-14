# Requirements

open-dci only configures the gateways. Everything around them is the operator's base
configuration, and must provide the following (details per mode:
[below](#in-detail)).

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
  ([why](#in-detail)).

**Recommended: BFD**
- On every session of gateways and exits: gateway ↔ exit, exit ↔ spine, exit ↔ core.
  Without it, a node that hangs with its links up costs a BGP hold time of packet loss
  (lab: ~7–8 s, TCP stalls ~13 s), with it under a second at 300 ms × 3, ~0.3–0.5 s at
  100 ms × 3 ([measurements](../operation/failure-measurements.md)).

## In detail

**Both modes:**
- FRR 10.4 (tested 10.4.1) with bgpd, zebra and staticd, and the integrated config
  (`vtysh`). 10.5.1, 10.6.0, 10.6.2 and 10.7.1 don't withdraw the VPN and type-5 routes
  they leaked from a tenant VRF when the source goes away (e2e `TestWithdrawal`).
- Linux with nf_tables (the gateway's ingress filter).
  Linux ≥ 5.14 (End.DT46).
- The base FRR config has a default BGP instance with a router-id that peers EVPN with the
  fabric (e.g. with the exit) and has `advertise-all-vni` in its `l2vpn evpn` address
  family.
- The gateway's VTEP address (router-id or `gateway.vtep`) is reachable in the underlay,
  and the fabric passes the tenant VNIs' type-5 routes to the gateway.
- The underlay carries VXLAN with the tenant MTU: ≥ tenant MTU + 50 B.
- No route targets are needed on the EVPN side: auto RTs work across partitions and ASNs
  (see the [Phase 0 findings](../development/phase0-findings.md)).

**DCI network mode:**
- The base config has the DCI VRF as an EVPN L3VNI (VRF + SVI on a VLAN-aware bridge +
  VXLAN port) with a BGP instance.
- The fabric passes the DCI VNI's routes between gateway and exit.
- The exits route the DCI VRF to the other partitions.
- Every fabric link on the DCI path must carry ≥ tenant MTU + 98 B (SRv6 + VXLAN), e.g.
  9098 for 9000 B tenants.

**Redundant gateways (a pair per partition):**
- Both gateways have the same `locator`, `networks` (so the same SIDs), and the same BGP
  ASN in their base config. The shared ASN makes each drop the routes the other one
  re-announced into the fabric, so neither re-exports or detours through its partner.
- Each has its own `loopback` and router-id. With `address` peers, remote gateways list both
  as `peers`; with `interface` peers, nothing changes elsewhere.
- The fabric (exit, core) spreads traffic to the shared locator over both gateways (ECMP)
  and falls back to the survivor.
- A gateway that can't forward although its sessions are up (L3VNI down, SID not
  installed, no remote locator reachable, routes without best path) withdraws itself
  ([Operation](../operation/health-and-failover.md#withdrawing-an-unhealthy-gateway)). Both gateways of a pair
  failing the same way disconnect the partition: they couldn't forward anyway.

**Peering with the exit (`peers[].interface`):**
- One `interface` peer per exit the gateway is attached to. With two exits per partition
  (the lab), each gateway has two, gets every route twice, and keeps working when one exit
  fails. `maxPrefixes` applies per session.
- A gateway attached to two exits must not become a transit router between them: its base
  config announces only its own prefixes in IPv4/IPv6 unicast (e.g. an `only-self-out`
  route-map: AS path empty). open-dci's locator and loopback are originated locally and
  pass; VPN and EVPN must stay unfiltered (the re-announced tenant routes carry longer
  paths).
- open-dci adds `bgp disable-ebgp-connected-route-check` to the gateway's default instance:
  FRR tracks the remote SID as next hop of imported SRv6 VPN routes, and requires it to be
  directly connected for single-hop eBGP sessions, which a SID never is.
- The exits (their base config, not open-dci):
  - activate `ipv4 vpn` and `ipv6 vpn` towards their gateways, with `allowas-in 1`: the
    gateways' VPN routes carry the exit's own ASN, since they were learned via EVPN through
    the exit.
  - relay the VPN routes to each other (eBGP multihop between their loopbacks, VPN only).
    They import none of them. Topologies between the exits:

    | Exits | Topology | Sessions per exit | A new partition touches |
    |---|---|---|---|
    | few | full mesh | all other exits | every exit |
    | any (the lab) | **ladder**: the partitions form a ring, each exit peers with both exits of the neighbouring partitions | 4 | the exits of its two neighbour partitions |
    | many, or partitions coming and going | 2 route servers (FRR `route-server-client`, exits accepted via `bgp listen range`) | 2 | nothing else |

    With three partitions, the closed ladder is the full partition mesh; it only saves
    sessions from four partitions on. A plain ring (one session to each neighbour exit) also works, but two failures split
    it. The ladder survives a whole partition failing, since each partition is connected
    to both exits of each neighbour. Like any ring, it gets slower to converge as it grows
    (routes travel up to half the ring).
  - need a default-VRF path to each other for these sessions: in FRR, the VPN address
    families only exist in the default BGP instance. In DCI-network partitions, where the
    exit's core link sits in the DCI VRF, that means an extra link or path (the lab's
    exit-a1/exit-a2 ↔ core `swp5`).
- Routes pass the exits with RD, RT and SID unchanged; the BGP next hop becomes the exit,
  which doesn't matter, since SRv6 forwards by the SID.

**Returning nodes:**
- open-dci withholds the locator until an EVPN session has delivered End-of-RIB
  ([Operation](../operation/health-and-failover.md#announcing-the-locator)). FRR peers send it by default; with
  graceful restart disabled on the exits, the gateway waits 30 s instead.
- FRR nodes on the transport path (gateways, and exits, spines and core if they run
  FRR) need `no zebra nexthop kernel enable`. With kernel nexthop groups, zebra keeps a
  next hop whose link went down in the group it reuses for the route BGP re-sent without
  it. When the link comes back, zebra revives it before BGP has a path through it, so
  traffic goes to a neighbour that isn't ready yet. This defeats the [locator gate](../operation/health-and-failover.md#announcing-the-locator).
- Don't enable `bgp suppress-fib-pending` on the exits: FRR then never announced the
  anycast locator, learned as type-5 with two next hops, into the core.

**Failure detection (recommended: BFD):** without BFD, a gateway or exit that stops
forwarding while its links stay up is only noticed when the BGP hold timer expires, and
traffic is lost for that long. Run BFD on every session of the gateways and exits: gateway
↔ exit (the gateway's base config, e.g. on its fabric peer-group), exit ↔ spine and exit ↔
core. A node needs BFD towards *all* its neighbours. A neighbour without it keeps
forwarding into a hung node until its hold timer expires. open-dci doesn't render BFD; the
lab uses `bfd profile dci` (300 ms × 3). Shorter intervals (100–200 ms) cut the loss to
0.3–0.7 s and raised no false alarm in the lab, but test them under peak load first
([Failure measurements](../operation/failure-measurements.md#bfd-intervals)).

**The edge of the SRv6 domain** (see [Operation](../operation/srv6-domain-edge.md)):
the exits must keep the locator block unreachable from anything but the gateways and the
core, and drop packets from the core into the block with a source outside it.

**Default-VRF mode:**

The transport shares the partition's underlay with every fabric device. It is only as safe
as the following configuration on the exits and gateways; if the fabric's underlay holds
untrusted devices (e.g. tenant firewalls), prefer DCI-network mode, which isolates the
transport by construction.
- IPv6 unicast is activated only between the gateways, their exits and the core.
- The exits announce the locators and gateway loopbacks only to the core (and their
  gateways), **never into the fabric**: no leaf, spine or tenant firewall may have a route
  into the locator block. Activating IPv6 for a whole fabric peer-group would leak them.
- The exits' edge filter drops anything from the fabric side addressed into the block.
- Outbound filters on the gateway must let the locator and loopback pass (they are
  originated locally).
- Every link on the path must carry ≥ tenant MTU + 48 B.
