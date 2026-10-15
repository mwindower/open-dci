# Internals

What open-dci changes in the kernel and in FRR, and how readiness, health and the ingress
filter work. The user's view of all this: the [website](https://mwindower.github.io/open-dci/).


## Kernel (idempotent)

- sysctls: forwarding, `seg6_enabled`, `net.vrf.strict_mode=1` (required for End.DT46), and
  `net.ipv6.seg6_flowlabel=1` (the outer flow label is derived from the inner flow, so ECMP
  along the transport spreads the flows instead of putting all traffic between two gateways
  on one path)
- the loopback (`gateway.loopback`, default `<locator>::1`) on `lo`
- the ingress filter, an nftables table `ip6 open-dci` (see
  [the SRv6 domain and its edge](https://mwindower.github.io/open-dci/docs/operation/srv6-domain-edge))

In DCI network mode additionally:
- a veth pair `dci0` (default VRF, `fe80::1`) ↔ `dci1` (DCI VRF, `fe80::2`)
- the DCI network's device chain raised to `transport.mtu`. The chain (VRF → SVI → bridge →
  VXLAN port) is discovered from the kernel, not from device names. This matters because
  a base config with 9000 there **silently black-holes** full-size SRv6 packets.
- the `lookup local` ip rule moved behind the l3mdev rule

Why a veth and not route leaking: see the [Phase 0b findings](phase0b-findings.md).

## Provisioned networks

For every network, open-dci creates and maintains:
- the VRF (table `table`, default the VNI)
- a bridge `dcibr<vni>` in the VRF. It is not VLAN-aware and serves as the L3VNI's SVI, so
  no VLAN IDs are needed.
- a VXLAN device `dcivx<vni>` (source: `gateway.vtep` or the router-id, port 4789, no
  learning) as the bridge's port, MTU `tenantMTU`
- in the VRF's table: `unreachable default` (IPv4 and IPv6, metric 4278198272), so that a
  lookup finding nothing never falls through to the main table, where the other tenants'
  SIDs live; and `throw <locatorBlock>`. The kernel routes the outer packet of the SRv6
  encapsulation in the tenant VRF's table, so the block, and only the block, has to
  continue in main. Tenants themselves never get there: the ingress filter drops their
  packets to the block.

Each device gets the interface alias `open-dci`. That is how open-dci recognizes its own
devices: it only changes or deletes devices with this alias, and it refuses to provision a
network whose VRF or devices exist without it.

When a network is dropped from the config, open-dci removes it in the order FRR
accepts:
1. `no vni` in the FRR VRF
2. `no router bgp <asn> vrf <name>`, once zebra has released the L3VNI (retried briefly)
3. the kernel devices (VXLAN device, bridge, VRF)
4. `no vrf <name>`, which FRR only accepts once the kernel VRF is gone

## FRR (via `vtysh`, never touching `frr.conf`)

1. Discovers the ASN, router-id, `advertise-all-vni` and the transport VRF's BGP instance
   from the running config.
2. Renders its lines (`open-dci render`) in FRR's canonical form.
3. Applies them only when some are missing from the running config.
4. Removes lines it applied earlier that are no longer desired (state in `--state`). Block
   headers of the base config are never removed. The tenant VRFs, and open-dci's own
   route-maps (all named `DCI-...`), are removed as a whole (see [Provisioned networks](#provisioned-networks)).

A reconcile holds an exclusive `flock` on `lock` next to the state file, so `run` and a
`drain`, `undrain` or `apply` from the command line never overlap: both would send the same
removals, and vtysh fails the second batch for lines that are already gone (seen in CI:
`drain` 7 ms after the sidecar). The second one waits and then plans against what the first
one left.

The lines for the lab's gw-a1 in DCI network mode (full version:
[`internal/frr/testdata/gw-a1.golden`](https://github.com/mwindower/open-dci/blob/main/internal/frr/testdata/gw-a1.golden)):

```
vrf vrf3981                               (one per network)
 vni 3981
exit-vrf
!
segment-routing
 srv6
  encapsulation
   source-address fd00:dc1:ff::a1          ! gateway.loopback
  exit
  locators
   locator DCI
    prefix fd00:dc1:a::/48 block-len 32 node-len 16
   ...
router bgp 4200000016
 bgp disable-ebgp-connected-route-check   ! the SID as next hop of single-hop eBGP routes
 segment-routing srv6
  locator DCI
 address-family ipv4 vpn                  (and ipv6 vpn)
  neighbor uplink0 activate               ! peers[].interface: the base config's session to the exit
  neighbor uplink0 route-map DCI-PEER-IN in       ! only configured RTs
  neighbor uplink0 maximum-prefix 10000
                                          (with peers[].address instead: neighbor ... remote-as,
                                           ebgp-multihop, update-source <loopback>,
                                           capability extended-nexthop, and no ipv4 unicast)
!
router bgp 4200000016 vrf vrf3981          (one per network)
 bgp router-id 10.0.0.16
 sid vpn per-vrf export 3981             ! networks[].sid (default: the VNI) → fd00:dc1:a:f8d::
 address-family ipv4 unicast              (and ipv6 unicast)
  network 10.0.16.0/24 route-map DCI-AGG   ! networks[].aggregates with hosts from the own
                                          !   fabric (10.0.32.0/24 is partition B's: not here)
  rd vpn export 10.0.0.16:3981
  rt vpn both 65535:1001
  route-map vpn import DCI-vrf3981-v4     (-v6 in ipv6 unicast)
  route-map vpn export DCI-vrf3981-v4
  export vpn
  import vpn
 address-family l2vpn evpn
  advertise ipv4 unicast route-map DCI-ADV   ! type-5 into the own fabric, see DCI-ADV
  advertise ipv6 unicast route-map DCI-ADV
!
router bgp 4200000016 vrf vrf104100
 address-family ipv6 unicast
  redistribute static
!
ipv6 route fd00:dc1::/32 fe80::2 dci0          ! remote locators → DCI VRF
ipv6 route fd00:dc1:a::/48 blackhole
vrf vrf3981
 ip route 10.0.16.0/24 blackhole               ! active aggregate: unused addresses end here
vrf vrf104100
 ipv6 route fd00:dc1:a::/48 fe80::1 dci1       ! own locator → default VRF (SIDs)
 ipv6 route fd00:dc1:ff::a1/128 fe80::1 dci1   ! own loopback, if outside the locator
!
ip prefix-list DCI-vrf3981-v4 seq 663923921 permit 10.0.16.0/24   ! networks[].aggregates (exact) and
ip prefix-list DCI-vrf3981-v4 seq 438889611 permit 10.0.32.0/24   !   networks[].prefixes; seq from the
                                                                  !   entry's content, not its position
route-map DCI-vrf3981-v4 deny 5                 ! (in every filter route-map) a route with the
 match extcommunity DCI-SOO                     !   own site of origin came back from the own
                                                !   fabric: never into the VPN
route-map DCI-vrf3981-v4 permit 10
 match ip address prefix-list DCI-vrf3981-v4
 set large-comm-list DCI-AGG delete             ! the aggregate mark stays on the gateway
!                                               (IPv6 alike; a family without entries: "deny 10")
route-map DCI-vrf3981-v4-default permit 10      ! only with defaultRoute: the same allowlist,
 match ip address prefix-list DCI-vrf3981-v4    !   used for the VPN import (import) or
 set large-comm-list DCI-AGG delete             !   export (export) instead of DCI-vrf3981-v4
route-map DCI-vrf3981-v4-default permit 20      !   ... plus the default route
 match ip address prefix-list DCI-DEFAULT-v4
 set local-preference 50                        !   import only: behind a local default route
ip prefix-list DCI-DEFAULT-v4 seq 5 permit 0.0.0.0/0
bgp large-community-list standard DCI-AGG seq 5 permit 4200000016:0:1   ! only with aggregates:
route-map DCI-AGG permit 10                     !   marks the own aggregates ...
 set large-community 4200000016:0:1
route-map DCI-ADV deny 10                       !   ... and keeps them out of the own
 match large-community DCI-AGG                  !   partition's type-5 (it has the hosts)
route-map DCI-ADV permit 20                     ! type-5: drop every extended community the
 on-match next                                  !   route brought from the VPN (DCI RT, the
 set extended-comm-list DCI-ANY delete          !   remote partition's RTs and router MAC) ...
route-map DCI-ADV permit 30                     !   ... then mark it with the site of origin;
 set extcommunity soo 4200000016:10             !   FRR adds the L3VNI's RT and router MAC
bgp extcommunity-list expanded DCI-ANY seq 5 permit .*
bgp extcommunity-list standard DCI-SOO seq 5 permit soo 4200000016:10   ! gateway.siteOfOrigin
bgp extcommunity-list standard DCI-RT seq 1872273698 permit rt 65535:1001   ! one per route target,
bgp extcommunity-list standard DCI-RT seq 3319327117 permit rt 65535:26782482   !   seq from its content
route-map DCI-PEER-IN permit 10
 match extcommunity DCI-RT
```

In default-VRF mode ([`gw-b1.golden`](https://github.com/mwindower/open-dci/blob/main/internal/frr/testdata/gw-b1.golden)), the DCI VRF
part and the veth routes are replaced by the locator announcement:

```
router bgp 4200000026
 address-family ipv6 unicast
  network fd00:dc1:b::/48
  network fd00:dc1:ff::b1/128              ! own loopback, if outside the locator
!
ipv6 route fd00:dc1:b::/48 blackhole
```

## Readiness, health and failure semantics in detail

The website explains the behaviour ([Health and failover](https://mwindower.github.io/open-dci/docs/operation/health-and-failover)); this is how it works.

### Announcing the locator

A gateway announces its locator and loopback only when it can deliver what they attract.
After its EVPN sessions come up, the tenant VRFs are empty until the fabric's type-5 routes
have arrived. A locator announced earlier makes the exits send the gateway packets that
are decapsulated into an empty VRF and dropped. Its anycast partner carries the traffic
in the meantime.

- **Ready:** at least one established EVPN session has delivered its initial table, marked
  by End-of-RIB. FRR sends End-of-RIB by default (graceful-restart helper mode); a session
  without it counts as ready after 30 s.
- **Withheld:** until then, and whenever no EVPN session is established, open-dci leaves
  out the announcement:
  - DCI-network mode: `redistribute static` in the transport VRF's BGP instance
  - default-VRF mode: the `network` statements

  The SIDs, the VRFs and everything else stay configured.
- `run` checks readiness every 500 ms and acts once a change has held for 1.5 s. A link
  flap thus withdraws the locator before the sessions are back (unnumbered sessions wait
  for router advertisements, ~3 s). If FRR can't be queried, or answers without any
  neighbors (e.g. while the base system reloads it), the last state stays.

In the lab, this cut the loss when a gateway's links return from 1.5–2.3 s to under
0.1 s without load ([measurements](performance.md)).

### Withdrawing an unhealthy gateway

A gateway can fail in ways neither BGP nor BFD notice: its sessions are up, but it can't
forward. `run` checks every 2 s:
- every network's L3VNI is `Up` (fabric side)
- every network's SID is installed in the kernel as End.DT46 (decapsulation)
- no tenant VRF has prefixes with valid paths but no best path for 2 s or longer
- at least one remote SID the tenant VRFs encapsulate to has a usable route: one inside
  the locator block, not a blackhole or `unreachable`. In DCI-network mode the transport
  VRF's table is checked. If only some remote SIDs are unreachable, that's a remote
  partition's problem, and withdrawing would only spread it.

After two failed checks in a row, the gateway withdraws like a drain: no locator, no type-5
routes, and its anycast partner carries everything. After three good checks it announces
again. `status` shows `health UNHEALTHY: <reason>` and `locator WITHDRAWN` (from the
`withdrawn` marker `run` writes next to the state file) and exits non-zero; the log has
`locator withdrawn reason=...`.

**Prefixes without best path** (valid paths, but FRR never ran the selection: `version 0`,
nothing installed) appear when a reconcile runs while `frr-reload.py` rewrites the config.
FRR doesn't recover by itself, and soft resets don't help (the paths don't change). The
health check collects every such prefix of every network, and `run` re-runs the selection
for each right away: `clear bgp vrf V prefix P` for IPv4, `clear bgp vrf V ipv6 unicast
prefix P` for IPv6 (FRR's syntax differs), at most every 30 s per prefix, logged and
counted (`open_dci_bestpath_recoveries_total`). No session is touched. e2e
`TestRecoversFromNoBestPath` forces the race (reload and `apply` at the same time); in the
lab the gateway was healthy again within 4–12 s in every round.

A prefix only counts once it stays without best path for 2 s (`stuckGrace`): FRR selects
asynchronously, so a fresh update (e.g. a network coming back in another partition) is
valid but unselected for a moment. A single such reading made the status unhealthy until
the next collection (`TestAPI` failed on it in CI). `run` remembers since when each prefix
is stuck, across health checks and status collections; a one-off `open-dci status` reads
the VRF table again after 2 s, so it takes 2 s longer only while such a prefix exists.

A network counts for the withdrawal only once it has passed the checks: a network that is
still being provisioned fails them for its first seconds (L3VNI not up yet, no SID yet), and
both gateways of a pair provision a new network at the same time. Withdrawing for that would
take the partition down whenever a network is added; `status` shows the problem instead. A
network that leaves the config and comes back starts over.

`apply` and `status` run the checks too (every network counts there), but only `run`
withdraws.

### Failure semantics

Gateways and exits keep no per-flow state: no NAT, no connection tracking (the ingress
filter is stateless), no sequence numbers. Any surviving gateway or exit can carry any
packet of any flow. A failure therefore never resets a TCP connection; it only loses
packets until the routing has removed the failed node. TCP retransmits them. Which flows
are affected depends on ECMP hashing: a failed gateway only hits the flows hashed to it
(roughly half of its partition's), a failed exit hits every flow of the partition, since
leaves, spines, gateways and the core all spread over both exits.

Nothing outside the partition reacts to a gateway failure: both gateways own the same
locator and SIDs (anycast), so remote gateways keep their encapsulation routes unchanged.
The exits only drop the failed gateway's next hop for the locator, and the leaves drop it
for the tenant prefixes (EVPN type-5).

How long packets are lost depends on how the failure is detected. Measured in the lab
(`make lab-perf`: pings every 20 ms and 2 × 8 TCP streams between partitions while gw-b2 or
exit-b1 fails, then returns; per-flow results and method in
[Failure measurements](performance.md)):

| Failure | Detected by | Loss without BFD | Loss with BFD (lab) | Return |
|---|---|---|---|---|
| Gateway loses its links (crash, power, cable) | link down at the exits | ~0.15 s | ~0.15 s | ≤ 2.3 s loss |
| Gateway hangs, links stay up | BGP hold timer (8 s) or BFD | ~6.5–7.5 s, TCP stalls ~13 s | ~1 s, TCP ~1.2 s | ≤ 0.3 s loss |
| Exit loses its links | link down at its neighbours | ≤ 0.15 s | ≤ 0.15 s | ≤ 3 s loss |
| Exit hangs, links stay up | BGP hold timers (8–9 s) or BFD | ~7–8 s, all flows of the partition | ~0.9 s, TCP ~1.2 s | none |

- **TCP stalls longer than the loss** because of exponential retransmission backoff: after
  a 7.5 s outage, the next retransmission comes at ~12.6 s (200 ms RTO doubled per
  attempt). Connections survive; a connection only gives up after ~15 min
  (`tcp_retries2`), but applications with shorter timeouts may not.
- **A silent failure costs a hold time,** unless BFD detects it. The lab runs BFD (300 ms ×
  3) on every session of gateways and exits: gateway ↔ exit, exit ↔ spine, exit ↔ core.
  All of them are needed: a neighbour without BFD keeps forwarding into a hung node until
  its own hold timer expires. BFD is part of the base configs (and the exit switches);
  open-dci doesn't render it.
- **Recovery also loses packets** for a moment when a node's links come back: the returning
  node attracts traffic before all its routes are in place. The cause isn't analysed in
  detail yet; a hung node that returns, whose sessions come back with routes already in
  its tables, loses next to nothing.

## Route targets and the fabrics

- **Local part vs. VNIs.** EVPN auto RTs import by the local part only
  ([Phase 0 findings](phase0-findings.md#route-target-behaviour-frr-106-l3vni-tested-live-in-the-lab)),
  so a DCI RT `X:N` that reached a fabric would be imported by any partition VRF whose VNI
  is `N`. The gateways strip all extended communities a VPN route brought along before
  they announce it as type-5 (`DCI-ADV`, see [FRR](#frr-via-vtysh-never-touching-frrconf)),
  so no DCI RT reaches a fabric. As a second line of defence, local parts ≥ 2^24
  (16777216) can never collide with a 24-bit VNI.
- **Width of the local part.** A 2-byte admin ASN allows a 32-bit local part
  (`65535:16777217`). A 4-byte ASN or an IPv4 admin part allows only 16 bits; `validate`
  rejects wider ones.
- **The RD** is `<routerID>:<sid>` by default, independent of the RT, so any RT works
  without an explicit `rd`.

## The ingress filter in detail

The gateway rules match packets as they arrive (nftables prerouting), before routing and
before SRv6 processing; the gateway's own encapsulated packets are never affected. Exempt
from the source rules are the gateway's own packets (via `lo`) and link-local sources,
which only the attached link can send: the own DCI VRF via the veth, or the exit (e.g.
ICMPv6 errors to the encap source). The source rules can't
stop a spoofed source *inside* the block: that is what the exits' rules are for. In
default-VRF mode, the partition's underlay is part of the transport. Any device on it that
may be untrusted (e.g. a tenant's firewall peering with the leaves) must not reach the
block. Two things ensure that: the exits announce the locators only to gateways and core,
so no fabric device has a route into the block (e2e `TestFabricHasNoTransportRoutes`), and
the exit's fabric-side rule drops what is sent there anyway. In DCI-network mode the
transport VRF doesn't exist on fabric devices at all, which is why it is the recommended
mode.

The e2e tests `TestEdgeDropsForgedSRv6FromFabric`, `TestGatewayDropsSRv6FromOutsideBlock`
and `TestGatewayDropsTenantToTransport` forge such packets and check that the respective
rule counts them and the victim sees nothing.
