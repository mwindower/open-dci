# Configuration

`open-dci` reads one YAML file, by default `/etc/open-dci/config.yaml`. Unknown fields are
rejected. `open-dci validate -c FILE` checks a file without touching the system.

## Example

The lab's gw-a1 (`lab/configs/gw-a1/open-dci.yaml`), one of partition A's redundant pair,
with the transport in a DCI network:

```yaml
gateway:
  locator: fd00:dc1:a::/48        # shared with gw-a2 (anycast); SIDs fd00:dc1:a:<sid>::
  loopback: fd00:dc1:ff::a1       # own address: SRv6 encap source
  locatorBlock: fd00:dc1::/32     # all gateways' locators and loopbacks
transport:
  vrf: vrf104100                  # DCI network of the base config; omit it for the default VRF
peers:                            # the sessions to both exits; the exits relay
  - interface: uplink0            # the VPN routes between the partitions
  - interface: uplink1
networks:
  - vrf: vrf3981                  # created by open-dci
    vni: 3981                     # the tenant's VNI in this partition
    routeTarget: "65535:1001"     # the stitched network's identity, same on all gateways
    aggregates:                   # its ranges, one per partition, same on all gateways
      - 10.0.16.0/24              #   partition A
      - 10.0.32.0/24              #   partition B
      - 2001:db8:16::/48
      - 2001:db8:32::/48
  - vrf: vrf3982
    vni: 3982
    routeTarget: "65535:1002"
    aggregates: [10.0.17.0/24, 10.0.33.0/24, 2001:db8:17::/48, 2001:db8:33::/48]
```

For the default VRF as transport, leave out the `transport` section (see the lab's gw-b1).

## Reference

### `gateway`

| Field | Default | Meaning |
|---|---|---|
| `locator` | required | The gateway's SRv6 locator, e.g. `fd00:dc1:a::/48`. The redundant gateways of a partition share it (anycast): they announce the same locator and, with the same `sid`s, the same SIDs. |
| `loopback` | `<locator>::1` | The gateway's own address: SRv6 encap source, and session endpoint for `address` peers. The default lies inside the locator (function 0 is never a SID), which only works for a single gateway. A redundant pair needs a unique loopback per gateway, inside `locatorBlock` but outside the locator; open-dci announces it next to the locator. |
| `locatorBlock` | required | Contains the locators of all gateways, e.g. `fd00:dc1::/32`. Traffic to it is routed into the transport. |
| `nodeLength` | `16` | Node bits of the locator. Block length + node length must equal the locator's prefix length. 16 function bits follow. |
| `asn` | discovered | ASN of the existing default BGP instance. If set, it must match the running FRR. |
| `routerID` | discovered | Router-id of the existing BGP instance, used for route distinguishers. If set, it must match the running FRR. |
| `vtep` | router-id | VXLAN source address (IPv4) of the provisioned L3VNIs. |

### `transport`

| Field | Default | Meaning |
|---|---|---|
| `vrf` | empty | EVPN VRF of the base config (a "DCI network") that carries the SRv6 transport. Empty: the transport is routed in the default VRF. |
| `mtu` | `9166` | DCI network mode only: MTU for the DCI VRF's bridge, VXLAN port, SVI and the veth pair. It must be ≥ `tenantMTU` + 48. |
| `tenantMTU` | `9000` | Largest tenant packet. Used to validate `mtu`, and as MTU of the provisioned L3VNIs' bridge and VXLAN device. |
| `veth`, `vethPeer` | `dci0`, `dci1` | DCI network mode only: names of the veth ends in the default VRF and in the DCI VRF. |

### `peers[]`

The BGP sessions that carry the VPN routes, in one of two forms:

| Field | Meaning |
|---|---|
| `interface` | **To the exit (recommended).** The base config's existing session over this interface (e.g. the unnumbered `uplink0` to the exit). open-dci only activates VPNv4/v6 on it, with its filters. The exits relay the VPN routes between the partitions, so a gateway needs no session to remote gateways, and a new partition touches no existing gateway. |
| `address`, `asn` | **Direct to a remote gateway (full mesh).** Its loopback and ASN; a different ASN gives eBGP multihop (`update-source` = own loopback), an equal one iBGP. The address must be inside `locatorBlock` and not inside the own locator. |
| `maxPrefixes` | Maximum VPN prefixes accepted from the peer per address family (default `10000`). FRR tears the session down when it is exceeded; it stays down until `clear bgp <neighbor>`. A session to the exit carries the routes of **all** partitions: size it accordingly. |

Every peer only delivers routes that carry one of the configured `routeTarget`s (an inbound
route-map `DCI-PEER-IN` on the VPN sessions); routes with other RTs are dropped at the
session, before any VRF import.

### `networks[]`

| Field | Default | Meaning |
|---|---|---|
| `vrf` | required | Tenant VRF to stitch. open-dci creates it; a VRF of that name that open-dci didn't create is refused. |
| `routeTarget` | required | `<asn>:<nn>` or `<ipv4>:<nn>`. It identifies the stitched network across all partitions and must be the same on all its gateways. |
| `rd` | `<routerID>:<nn>` | Route distinguisher for the VPN export. The default takes `<nn>` from `routeTarget`. |
| `vni` | required | The tenant's L3VNI in this partition (1 – 16777215). open-dci provisions the VRF with it: VRF, bridge `dcibr<vni>`, VXLAN device `dcivx<vni>`, the FRR VRF with `vni`, and a BGP instance that advertises the routes as type-5. |
| `table` | the VNI | Kernel routing table of the VRF. |
| `sid` | the VNI | Function part of the network's End.DT46 SID, `<locator>:<sid in hex>::` (1 – 65535). Pinned, so the SID survives restarts and is identical on both gateways of a pair. Must be set if the VNI doesn't fit 16 bits. |
| `aggregates` | – | The stitched network's address ranges, plain prefixes (`10.0.16.0/24`), **each in exactly one partition**. The gateways of the partition whose tenant VRF holds more specific routes from its own fabric in a range (the machines' /32 and /128 from EVPN) announce the range instead of them and drop traffic to its unused addresses (blackhole route). A range without such routes isn't announced, so the list is the same on all gateways of the network. open-dci checks this on every reconcile (`run -i`, default 10 s): the first host in a range, or the last one leaving it, takes effect within one interval. Aggregates are part of the allowlist (matched exactly). A range used in two partitions doesn't work: each pair prefers its own aggregate. The own aggregate isn't announced back into the own partition (it has the host routes). Adding the first aggregate to a network, or removing the last one, briefly withdraws the gateway's type-5 routes of that network (its `advertise` lines change); the partner gateway carries the traffic meanwhile, so change one gateway of a pair at a time. |
| `prefixes` | – | Further routes passed as they are, in FRR prefix-list syntax: `PREFIX [ge N] [le N]`. A bare prefix matches exactly, `ge`/`le` extend it to more-specific ones (`10.0.16.0/24 le 32`: the /24 and every host in it). Only routes matching an aggregate or a prefix are exported from and imported into the VRF. Everything else stays in its partition, including a default route unless `0.0.0.0/0` / `::/0` is listed. Like `routeTarget`, the list is the same on all gateways of the network. A family without entries in `aggregates` and `prefixes` is not exchanged at all. At least one of the two lists is required. |

## Validation

Besides syntax, `validate` (and every other command) rejects:
- a locator outside `locatorBlock`, with host bits set, or not matching `nodeLength`
- peers with both `interface` and `address`, duplicated interfaces, invalid names
- address peers inside the own locator, outside the block, duplicated or without ASN
- VRFs used twice, or a tenant VRF that is also the transport VRF
- a transport MTU that can't carry `tenantMTU` + 48 B
- invalid route targets or distinguishers
- a missing `vni`, one out of range or used twice, a reserved or duplicate table
- a `sid` out of range or used twice, a `loopback` inside the locator or outside the block
- a network without `aggregates` and `prefixes`; malformed aggregates (host bits set,
  `ge`/`le`, a default or host route) or overlapping ones; malformed prefixes (host bits
  set, `ge`/`le` out of range, `ge` > `le`) or duplicates, also of an aggregate; and a
  `maxPrefixes` below 1

At runtime, `apply`/`run`/`diff`/`status` also check the system:
- the transport VRF (if any) exists in the kernel, with a BGP instance
- ASN and router-id match the config, if they are set there
- `advertise-all-vni` in the default instance
- a network's devices, if they exist, were created by open-dci (see
  [Operation](operation.md#provisioned-networks)); it never takes over other devices

## Requirements on the environment

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
  (see the [Phase 0 findings](phase0-findings.md)).

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
  ([Operation](operation.md#withdrawing-an-unhealthy-gateway)). Both gateways of a pair
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
  ([Operation](operation.md#announcing-the-locator)). FRR peers send it by default; with
  graceful restart disabled on the exits, the gateway waits 30 s instead.
- FRR nodes on the transport path (gateways, and exits, spines and core if they run
  FRR) need `no zebra nexthop kernel enable`. With kernel nexthop groups, zebra keeps a
  next hop whose link went down in the group it reuses for the route BGP re-sent without
  it. When the link comes back, zebra revives it before BGP has a path through it, so
  traffic goes to a neighbour that isn't ready yet. This defeats the locator gate above.
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
([performance.md](performance.md#bfd-intervals)).

**The edge of the SRv6 domain** (see [Operation](operation.md#the-srv6-domain-and-its-edge)):
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
