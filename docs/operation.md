# Operation

## Commands

```sh
open-dci validate -c config.yaml        # check the config
open-dci render   -c config.yaml        # FRR lines open-dci adds (--asn/--router-id to render offline)
open-dci diff     -c config.yaml        # what is missing in the running FRR, and what is stale
open-dci apply    -c config.yaml        # reconcile kernel + FRR once
open-dci run      -c config.yaml -i 10s # reconcile continuously (service / sidecar)
open-dci status   -c config.yaml        # exits non-zero if not healthy
open-dci drain    -c config.yaml        # planned maintenance: the partner takes over
open-dci undrain  -c config.yaml        # announce again
open-dci version
```

Common flags:

| Flag | Default |
|---|---|
| `--vtysh` | `vtysh` |
| `--state` | `/var/lib/open-dci/applied.conf` |
| `-v` | off (debug logging) |

`run` is meant to run permanently, as a service or as a sidecar next to FRR. It retries until
FRR is up, and it re-applies its configuration whenever the base system (e.g. a
configuration management running `frr-reload.py`) removes it. In the lab, that takes one
interval.

## Status

`status` on the lab's gw-a1:

```
gateway   fd00:dc1:ff::a1  AS 4200000016  router-id 10.0.0.16  locator fd00:dc1:a::/48
frr       in sync
locator   announced
transport vrf vrf104100  veth up: yes  path MTU: 9166 (need 9166)  local rule last: yes  vrf strict_mode: 1
filter    dropped: tenant-to-transport 0, locator-from-outside 0, loopback-from-outside 0

PEER     AS          STATE        UP        VPNv4 RCVD/SENT  VPNv6 RCVD/SENT
uplink0  4200000014  Established  00:00:29  4/6              4/6
uplink1  4200000018  Established  00:00:29  4/6              4/6

VRF      RT          RD              SID                          L3VNI    LOCAL v4/v6  REMOTE v4/v6
vrf3981  65535:1001  10.0.0.16:1001  fd00:dc1:a:f8d:: (End.DT46)  3981 Up  1/1          1/1
vrf3982  65535:1002  10.0.0.16:1002  fd00:dc1:a:f8e:: (End.DT46)  3982 Up  1/1          1/1
```

- `locator` is `announced`, or `WITHHELD` with the reason (see
  [Announcing the locator](#announcing-the-locator)).
- `filter` shows the ingress filter's rules with their drop counters (see below).
- `L3VNI` shows each network's VNI with zebra's state of it.
- The only peers are the sessions to the two exits (`peers[].interface`); `AS` comes from
  FRR.
  `RCVD` counts the remote gateways' routes (2 tenants × 2 gateways of pair B). `SENT`
  includes the relayed routes a gateway passes back to its exit. The exit keeps them as
  longer, never-best paths, and the other exits and the partner gateway would drop them
  anyway (own ASN in the path): noise, not loops.

- A gateway in default-VRF mode shows `transport default VRF` instead.
- `OPEN_DCI_OUTPUT=json open-dci status` prints the same as JSON.
- The exit code is non-zero unless all of these hold:
  - there is no drift
  - the locator is announced
  - the health checks pass (see [below](#withdrawing-an-unhealthy-gateway))
  - the kernel parts are in place
  - every peer is Established
  - every network has a SID
  - every network's L3VNI is `Up`
  - no prefix in a tenant VRF has valid paths but no best path (FRR installs nothing for
    it; seen rarely after `frr-reload.py`, fixed by a BGP session reset)
  - the ingress filter is installed

## What `apply` / `run` change

### Kernel (idempotent)

- sysctls: forwarding, `seg6_enabled`, `net.vrf.strict_mode=1` (required for End.DT46), and
  `net.ipv6.seg6_flowlabel=1` (the outer flow label is derived from the inner flow, so ECMP
  along the transport spreads the flows instead of putting all traffic between two gateways
  on one path)
- the loopback (`gateway.loopback`, default `<locator>::1`) on `lo`
- the ingress filter, an nftables table `ip6 open-dci` (see
  [the SRv6 domain and its edge](#the-srv6-domain-and-its-edge))

In DCI network mode additionally:
- a veth pair `dci0` (default VRF, `fe80::1`) ↔ `dci1` (DCI VRF, `fe80::2`)
- the DCI network's device chain raised to `transport.mtu`. The chain (VRF → SVI → bridge →
  VXLAN port) is discovered from the kernel, not from device names. This matters because
  a base config with 9000 there **silently black-holes** full-size SRv6 packets.
- the `lookup local` ip rule moved behind the l3mdev rule

Why a veth and not route leaking: see the [Phase 0b findings](phase0b-findings.md).

### Provisioned networks

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

### FRR (via `vtysh`, never touching `frr.conf`)

1. Discovers the ASN, router-id, `advertise-all-vni` and the transport VRF's BGP instance
   from the running config.
2. Renders its lines (`open-dci render`) in FRR's canonical form.
3. Applies them only when some are missing from the running config.
4. Removes lines it applied earlier that are no longer desired (state in `--state`). Block
   headers of the base config are never removed. The tenant VRFs, and open-dci's own
   route-maps (all named `DCI-...`), are removed as a whole (see above).

The lines for the lab's gw-a1 in DCI network mode (full version:
[`internal/frr/testdata/gw-a1.golden`](../internal/frr/testdata/gw-a1.golden)):

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
  rd vpn export 10.0.0.16:1001
  rt vpn both 65535:1001
  route-map vpn import DCI-vrf3981-v4     (-v6 in ipv6 unicast)
  route-map vpn export DCI-vrf3981-v4
  export vpn
  import vpn
 address-family l2vpn evpn
  advertise ipv4 unicast
  advertise ipv6 unicast
!
router bgp 4200000016 vrf vrf104100
 address-family ipv6 unicast
  redistribute static
!
ipv6 route fd00:dc1::/32 fe80::2 dci0          ! remote locators → DCI VRF
ipv6 route fd00:dc1:a::/48 blackhole
vrf vrf104100
 ipv6 route fd00:dc1:a::/48 fe80::1 dci1       ! own locator → default VRF (SIDs)
 ipv6 route fd00:dc1:ff::a1/128 fe80::1 dci1   ! own loopback, if outside the locator
!
ip prefix-list DCI-vrf3981-v4 seq 5 permit 10.0.16.0/24 le 32      ! networks[].prefixes
ip prefix-list DCI-vrf3981-v4 seq 10 permit 10.0.32.0/24 le 32
route-map DCI-vrf3981-v4 permit 10
 match ip address prefix-list DCI-vrf3981-v4
!                                               (IPv6 alike; a family without prefixes: "deny 10")
bgp extcommunity-list standard DCI-RT seq 5 permit rt 65535:1001   ! one per route target
bgp extcommunity-list standard DCI-RT seq 10 permit rt 65535:1002
route-map DCI-PEER-IN permit 10
 match extcommunity DCI-RT
```

In default-VRF mode ([`gw-b1.golden`](../internal/frr/testdata/gw-b1.golden)), the DCI VRF
part and the veth routes are replaced by the locator announcement:

```
router bgp 4200000026
 address-family ipv6 unicast
  network fd00:dc1:b::/48
  network fd00:dc1:ff::b1/128              ! own loopback, if outside the locator
!
ipv6 route fd00:dc1:b::/48 blackhole
```

### Planned maintenance: drain

`open-dci drain` makes the gateway announce nothing:
- no locator and loopback (as while not ready, see below)
- no type-5 routes from its tenant VRFs (`advertise ipv4/ipv6 unicast` in their
  `l2vpn evpn` family)

The exits and the leaves stop sending it traffic within seconds, and its anycast partner
carries everything. The gateway can then be rebooted, upgraded or unplugged. Its VRFs,
SIDs and sessions stay in place.

- The state is a marker file next to the state file (`/var/lib/open-dci/drained`). A
  running `open-dci run` keeps the gateway drained across reconciles and restarts.
- `status` shows `locator DRAINED`; a drained gateway counts as healthy.
- `open-dci undrain` removes the marker. The gateway announces again as soon as it is
  ready.
- Drain one gateway of a pair at a time; draining both disconnects the partition.

Measured in the lab: drain, wait 3 s, take the links down, bring them back, undrain. No
flow lost a packet ([measurements](performance.md)).

### Withdrawing an unhealthy gateway

A gateway can fail in ways neither BGP nor BFD notice: its sessions are up, but it can't
forward. `run` checks every 2 s:
- every network's L3VNI is `Up` (fabric side)
- every network's SID is installed in the kernel as End.DT46 (decapsulation)
- no tenant VRF has prefixes with valid paths but no best path
- at least one remote SID the tenant VRFs encapsulate to has a usable route: one inside
  the locator block, not a blackhole or `unreachable`. In DCI-network mode the transport
  VRF's table is checked. If only some remote SIDs are unreachable, that's a remote
  partition's problem, and withdrawing would only spread it.

After two failed checks in a row, the gateway withdraws like a drain: no locator, no type-5
routes, and its anycast partner carries everything. After three good checks it announces
again. `status` shows `health UNHEALTHY: <reason>` and exits non-zero; the log has
`locator withdrawn reason=...`.

`apply` and `status` run the checks too, but only `run` withdraws.

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

## The SRv6 domain and its edge

End.DT46 decapsulates every packet addressed to a SID into the SID's VRF, whatever its
source. Whoever can send packets to a SID can inject traffic into that tenant. The SRv6
domain (gateways, their transport, exits, core) is therefore closed at its edge
(RFC 8754, section 5.1):

| Where | What is dropped | Who implements it |
|---|---|---|
| Gateway, from its tenants | packets arriving on a tenant bridge (`dcibr*`) addressed into the locator block | open-dci (`tenant-to-transport`) |
| Gateway, towards its SIDs | packets to its locator from a source outside the block | open-dci (`locator-from-outside`) |
| Gateway, towards its loopback | packets to its own loopback from outside the block (anycast mode) | open-dci (`loopback-from-outside`) |
| Exit, fabric side | anything entering the block from the fabric, and any source inside the block | the exit's ACLs (lab: `edgeFilter` in `node.yaml`) |
| Exit, core side | packets into the block with a source outside it | the exit's ACLs |

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

## Failure semantics

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
[performance.md](performance.md)):

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

## What the network sees

A tenant packet on the wire from gw-a1 to exit-a1 (DCI network mode, `make lab-capture`):

```
IP 10.0.0.16 > 10.0.0.14.4789: VXLAN vni 104100                   ← gw-a1 → exit-a1, DCI network
  IP6 fd00:dc1:ff::a1 > fd00:dc1:b:fab::: RT6 (type=4, segleft=0)  ← SRv6 to pair B's anycast SID
    IP 10.0.16.10 > 10.0.32.10: ICMP echo request                  ← tenant packet
```

Which node knows which routes, for both modes: [lab-routing.md](lab-routing.md).

## Adding partitions and networks

What to change on which gateway when a partition joins or a network is stitched, and how to
choose route targets: [day2.md](day2.md).
