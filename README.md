# open-dci

[![ci](https://github.com/mwindower/open-dci/actions/workflows/ci.yaml/badge.svg)](https://github.com/mwindower/open-dci/actions/workflows/ci.yaml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**Stitch EVPN tenant VRFs across independent EVPN/VXLAN domains using SRv6 L3VPN.**

`open-dci` turns an FRR-based Linux box at the exit of an EVPN fabric, e.g. a
[metal-stack](https://metal-stack.io) partition, into a DCI gateway:
- it provisions the tenant VRFs as EVPN L3VNIs (VRF, bridge, VXLAN device) with the
  tenant's VNI in that partition, so the fabric sees an ordinary VTEP
- the VRFs are exported as VPNv4/v6 with an SRv6 End.DT46 SID
- remote routes come back as EVPN type-5
- each partition keeps its own VNIs, RTs and ASNs

It never rewrites `frr.conf`. It adds its lines via `vtysh` to the gateway's base config
and puts them back whenever the base system reloads its config.

> [!WARNING]
> **Alpha.** open-dci is at an alpha stage: configuration and behaviour may still change
> incompatibly, and it is not ready for production. It is tested end to end in a
> [containerlab lab](lab/README.md) that runs in CI; redundancy and RT hygiene are next on
> the [roadmap](docs/development.md#roadmap).
>
> **L3 stitching only.** Tenant IP prefixes are routed between partitions (EVPN type-5 ↔
> VPNv4/v6). **L2 is not supported**: no stretched subnets, no MAC/IP (type-2) routes, no
> L2VNIs across partitions.

<p align="center">
  <img src="docs/packet-flow.svg" width="960" alt="Animated packet flow: each partition has two exits and a redundant gateway pair attached to both. A tenant packet from m-a travels over VXLAN with VNI 3981 through leaf-a and exit-a1 to gw-a1, which provisioned the tenant VRF. gw-a1 encapsulates it in SRv6 to partition B's anycast SID and sends it in the DCI network (VNI 104100) via the other exit, exit-a2, into the core. exit-b1 delivers it to gw-b2, which decapsulates it (End.DT46) and forwards it with partition B's VNI 4011 via exit-b2 and leaf-b to m-b.">
</p>

Between partitions only IPv6 is needed: exits and core see one locator prefix per gateway,
and nothing of tenants or VNIs. The SRv6 transport runs in one of two modes, which can be
mixed freely:

| Mode | SRv6 transport | Fits |
|---|---|---|
| DCI network (`transport.vrf`) | in an EVPN VRF of the base config, over VXLAN through the fabric | gateways that reach the core only through the fabric |
| Default VRF | in the IPv6 underlay | gateways whose uplink carries IPv6 towards the core |

## What can be stitched

| Scenario | Supported | In the lab |
|---|---|---|
| Different VNIs per partition (3981 ↔ 4011) | yes: VNIs are local, the route target is the identity | yes |
| The same VNI in both partitions | yes, same as above | no |
| Different ASNs per partition and per router, auto RTs on the EVPN side | yes | yes |
| Several tenants per gateway, isolated from each other | yes, one VRF each | yes (2) |
| Overlapping prefixes of *different* tenants | yes, by design (separate VRFs) | no |
| One network across more than two partitions | yes, with a peer per remote gateway | no |
| IPv4 and IPv6 | yes (End.DT46) | yes |
| Mixed transport modes (DCI network ↔ default VRF) | yes | yes |
| Overlapping prefixes *within* one stitched network | no: the partitions share one routing domain | – |
| Explicit (non-auto) RTs on the leaves | not yet: the gateway's L3VNIs use auto RTs | – |
| Redundant gateways per partition (anycast locator, failover without BGP changes) | yes | yes |
| Gateways attached to two exits each (ECMP; a whole exit can fail) | yes | yes |
| L2: stretched subnets, MAC/IP routes | no | – |

## Scale and limits

Hard limits come from the design; everything else is bounded by the gateway's CPU, memory
and FRR, and has **not been measured yet** (the lab runs 2 tenants on 2 pairs; a scale test
is on the [roadmap](docs/development.md#roadmap)).

| What | Limit | Where it comes from |
|---|---|---|
| Stitched networks (tenant VRFs) per gateway | 65535 by design; practically far less, untested | 16 function bits per locator give one End.DT46 SID per network (`networks[].sid`). Each network costs a VRF, a bridge and a VXLAN device, an FRR VRF with its own BGP instance, prefix-lists and route-maps. |
| Partitions (gateway pairs) | 65536 with the default `/32` block and 16 node bits | One locator (`/48`) per pair, shared by both gateways. |
| BGP sessions per gateway | 1 per exit it is attached to (the lab: 2) | Gateways only peer with their exits; the exits relay the VPN routes between the partitions (mesh, ring or route reflectors among the exits). A full mesh between gateways (`peers[].address`) is still possible. |
| VNIs | 24 bit | VXLAN. VNIs are local to a partition, so they don't add up. |
| VPN prefixes per peer | `maxPrefixes`, default 10000 per address family | Safety net; exceeding it tears the session down. |
| Throughput | CPU-bound, not measured | Encap and decap are done by the Linux kernel in software (no XDP, no offload); see [scaling bandwidth](#scaling-bandwidth). |
| Overhead per packet | +48 B (IPv6 + SRH), +50 B more in a DCI network | Every hop must fit tenant MTU + overhead; open-dci validates and raises the DCI devices. |
| Reconcile | every 10 s (`run -i`) | Each run reads the whole FRR running-config; its cost grows with the number of networks. |

### Scaling bandwidth

Every packet between two partitions crosses one gateway in each partition. On a gateway it
arrives as VXLAN from the fabric and leaves as SRv6 (or the reverse), so its uplinks carry
its share **twice**: N Gbit/s of stitched traffic through a gateway need about 2N Gbit/s of
uplink capacity. Exits and core carry it once per direction, plus 48 B per packet (SRv6) and
50 B more inside a DCI network.

Ways to add bandwidth, from the cheapest:

1. **Bigger gateways.** Faster NICs and more cores: multi-queue NICs spread the flows over
   the cores (RSS), using the VXLAN source port and the SRv6 flow label as entropy.
2. **Both gateways of a pair are active.** With the anycast locator, the leaves spread
   tenant traffic over both gateways' VTEPs, and the exits spread SRv6 traffic to the
   locator over both gateways (ECMP). A pair carries about twice one gateway, and the
   survivor all of it after a failure.
3. **Dual attachment.** Each gateway uses both uplinks, to two different exits, for the
   fabric, the transport and the VPN routes (ECMP in both directions). That doubles its
   uplink capacity and survives the loss of a link or a whole exit (the lab does this).
4. **More gateways per locator.** Nothing in open-dci limits an anycast group to two:
   N gateways with the same `locator`, `networks` and ASN give N-way ECMP. The limit is the
   ECMP width of leaves and exits (`maximum-paths`). Tested with 2.
5. **Shard the tenants.** Several independent gateway groups per partition, each with its
   own locator, each stitching a subset of the networks. Capacity and blast radius are then
   per group; the groups don't need to know each other, the exits relay all of them.

What all of these need is **flow entropy**: ECMP hashes per flow, and all traffic between
two gateways has the same outer addresses (loopback → SID). open-dci therefore sets
`net.ipv6.seg6_flowlabel=1`, so the outer IPv6 flow label is derived from the inner flow.
Exits and core must include the IPv6 flow label in their ECMP hash (Linux does; check the
switches' hash settings). Without it, everything between two gateways takes one path. A
single flow is never split: it is limited by one path and one core.

None of this is measured yet. The scale test on the
[roadmap](docs/development.md#roadmap) should measure packets per second per core, per
gateway and per pair.

### Why not on the switches?

The stitching could run on the exits or leaves themselves (SONiC uses FRR, too). open-dci
deliberately puts it on dedicated Linux gateways:

- **Data-plane support.** End.DT46 decap plus SRv6 encap with VPN SIDs, in the same box as
  EVPN/VXLAN, needs ASIC support. Many datacenter switch ASICs don't support SRv6 VPN at all
  or only in recent generations, and SONiC's SRv6 support covers only some platforms. The
  Linux kernel supports it on any server.
- **Hardware tables.** On a switch, every tenant VRF, L3VNI, VXLAN tunnel and SRv6 encap
  entry competes for fixed tables (VRF IDs, next hops, tunnel and TCAM entries, shared LPM
  space). Depending on the ASIC, VRFs are typically limited to hundreds or a few thousand.
  Stitching N tenants across partitions adds N VRFs plus their remote routes to every
  switch that does it. A server's limits are memory and CPU, and are easy to grow.
- **Blast radius and ownership.** The exits carry the whole partition's fabric and
  internet traffic and are managed by the fabric's own tooling (e.g. metal-core). Putting
  per-tenant DCI state on them couples every tenant change to the fabric. Dedicated gateways
  add to an untouched fabric, can be updated, restarted or replaced pair by pair, and fail
  over without the fabric noticing.
- **Scale-out.** When one pair isn't enough, add gateways to the anycast group, another
  group (tenants spread across groups) or bigger servers, instead of upgrading switches
  (see [scaling bandwidth](#scaling-bandwidth)).

The price is an extra hop through a server and software forwarding, which is why its
throughput needs measuring before production use.

## Why this design

- **L3 only, SRv6 L3VPN between domains.** Stretching EVPN would couple the partitions'
  VNIs, RTs, ASNs and failure domains. Exchanging only type-5 prefixes as VPNv4/v6 keeps
  every EVPN domain independent. The inter-partition network needs nothing but IPv6 and
  one locator prefix per gateway: no tenant state, no VNIs, no MPLS.
- **Stock FRR and the Linux kernel.** End.DT46 in the kernel and FRR's EVPN ↔ VPN
  re-origination already do the job ([Phase 0](docs/phase0-findings.md)). No custom
  data plane means nothing to maintain beyond configuration.
- **Dedicated, provider-owned gateways.** Only these boxes speak VPN and SRv6, so tenants
  never reach a SID or the transport, and a few stable nodes per partition keep locators
  and peers static ([day-2 operations](docs/day2.md)). The gateway joins the fabric like
  any VTEP; the fabric needs no changes beyond passing the tenant VNIs' routes.
- **Redundancy by anycast.** Both gateways of a partition own the same locator and SIDs.
  Remote gateways don't need to know which one is alive: the transport delivers to
  whichever is reachable, so a failure needs no BGP reconvergence of the VPN routes.
- **Add to the base config, own only what it provisions.** The operator's base config
  (underlay, EVPN, optionally the DCI network) stays theirs. open-dci discovers ASN,
  router-id and devices from kernel and FRR, creates only the tenant VRFs it stitches
  (tagged as its own), and reconciles continuously instead of owning `frr.conf`.
- **Independent of metal-stack.** metal-stack is the first target, not a dependency: the
  code assumes no device names or metal-stack APIs, so any FRR-based EVPN fabric works.

## Quick start

The gateway's base FRR config peers EVPN with the fabric and has `advertise-all-vni` (see
[Configuration](docs/configuration.md)). Each network is a tenant VRF that open-dci
provisions with the tenant's VNI in that partition. Each partition runs a redundant pair of
gateways that share the locator (anycast) and pinned SIDs (the VNI by default), each with
its own loopback, and each attached to both exits of the partition (`uplink0`, `uplink1`). Two of the lab's four gateways
(`/etc/open-dci/config.yaml`, from `lab/configs/gw-*/open-dci.yaml`; gw-a2 and gw-b2 differ
only in `loopback`):

<table>
<tr>
<th>gw-a1: partition A (pair with gw-a2), transport in a DCI network</th>
<th>gw-b1: partition B (pair with gw-b2), transport in the default VRF</th>
</tr>
<tr>
<td>

```yaml
gateway:
  locator: fd00:dc1:a::/48      # shared by the pair
  loopback: fd00:dc1:ff::a1     # own: encap source
  locatorBlock: fd00:dc1::/32   # all gateways
transport:
  vrf: vrf104100                # DCI network
peers:                          # only the two exits; they
  - interface: uplink0          # relay the VPN routes
  - interface: uplink1
networks:
  - vrf: vrf3981
    vni: 3981                   # tenant 1 in A, SID f8d
    routeTarget: "65535:1001"
    prefixes:                   # tenant 1, all partitions
      - 10.0.16.0/24 le 32
      - 10.0.32.0/24 le 32
      - 2001:db8:16::/48 le 128
      - 2001:db8:32::/48 le 128
  - vrf: vrf3982
    vni: 3982                   # tenant 2 in A, SID f8e
    routeTarget: "65535:1002"
    prefixes:                   # tenant 2, all partitions
      - 10.0.17.0/24 le 32
      - 10.0.33.0/24 le 32
      - 2001:db8:17::/48 le 128
      - 2001:db8:33::/48 le 128
```

</td>
<td>

```yaml
gateway:
  locator: fd00:dc1:b::/48      # shared by the pair
  loopback: fd00:dc1:ff::b1     # own: encap source
  locatorBlock: fd00:dc1::/32   # same everywhere
# no transport: default VRF (underlay)

peers:                          # only the two exits; they
  - interface: uplink0          # relay the VPN routes
  - interface: uplink1
networks:
  - vrf: vrf4011
    vni: 4011                   # tenant 1 in B, SID fab
    routeTarget: "65535:1001"   # = gw-a1's
    prefixes:                   # tenant 1, all partitions
      - 10.0.16.0/24 le 32
      - 10.0.32.0/24 le 32
      - 2001:db8:16::/48 le 128
      - 2001:db8:32::/48 le 128
  - vrf: vrf4012
    vni: 4012                   # tenant 2 in B, SID fac
    routeTarget: "65535:1002"   # = gw-a1's
    prefixes:                   # tenant 2, all partitions
      - 10.0.17.0/24 le 32
      - 10.0.33.0/24 le 32
      - 2001:db8:17::/48 le 128
      - 2001:db8:33::/48 le 128
```

</td>
</tr>
</table>

What must match across gateways: the `locatorBlock`, and per stitched network the
`routeTarget` and the `prefixes`. Within a pair, also the `locator`,
the `sid`s and the BGP ASN (which prevents loops). The VNIs are local to each partition.

**Safety net.**
- Only prefixes in a network's `prefixes` leave or enter its VRF; anything else, including a
  default route unless listed, stays in its partition.
- Each peer only delivers routes with a configured route target, up to `maxPrefixes`
  (default 10000) per address family. See [Configuration](docs/configuration.md#networks).
- Forged SRv6 packets never reach a SID: the gateway drops packets from tenants to the
  locator block and packets to its locator from outside the block, the tenant VRFs have no
  fall-through to the main table, and the exits filter the edge of the SRv6 domain (see
  [Operation](docs/operation.md#the-srv6-domain-and-its-edge)).

```sh
open-dci validate -c /etc/open-dci/config.yaml
open-dci render   -c /etc/open-dci/config.yaml   # the FRR lines it will add
open-dci run      -c /etc/open-dci/config.yaml   # reconcile continuously
open-dci status   -c /etc/open-dci/config.yaml
```

## Documentation

| | |
|---|---|
| [Installation](docs/installation.md) | binary + systemd, container, metal-stack notes |
| [Configuration](docs/configuration.md) | all fields, validation, requirements per mode |
| [Operation](docs/operation.md) | commands, `status`, what exactly is changed in kernel and FRR |
| [Adding partitions and networks](docs/day2.md) | what changes where, route targets, keeping locations in sync |
| [Lab](lab/README.md) | the containerlab lab and its e2e tests |
| [Routing tables](docs/lab-routing.md) | which node knows which routes, in both modes |
| [Development](docs/development.md) | layout, tests, CI, releases, roadmap |
| Design findings (history) | [Phase 0](docs/phase0-findings.md): EVPN ↔ SRv6 feasibility, RT behaviour · [Phase 0b](docs/phase0b-findings.md): the DCI network design, from the dropped firewall placement |

## License

[MIT](LICENSE)
