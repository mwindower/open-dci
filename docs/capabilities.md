# Capabilities and limits

What open-dci can stitch, where its limits are, and how a setup grows in bandwidth. The
design behind it: [README](../README.md#design-decisions).

## What can be stitched

| Scenario | Supported | In the lab |
|---|---|---|
| Different VNIs per partition (3981 ↔ 4011) | yes: VNIs are local, the route target is the identity | yes |
| The same VNI in both partitions | yes, same as above | no |
| Different ASNs per partition and per router, auto RTs on the EVPN side | yes | yes |
| Several tenants per gateway, isolated from each other | yes, one VRF each | yes (2) |
| Overlapping prefixes of *different* tenants | yes, by design (separate VRFs) | no |
| One network across more than two partitions | yes: the exits relay the VPN routes, gateways keep their two sessions | yes (three) |
| IPv4 and IPv6 | yes (End.DT46) | yes |
| Mixed transport modes (DCI network ↔ default VRF) | yes | yes |
| Redundant gateways per partition (anycast locator, failover without BGP changes) | yes | yes |
| Gateways attached to two exits each (ECMP; a whole exit can fail) | yes | yes |
| Overlapping prefixes *within* one stitched network | no: the partitions share one routing domain | – |
| L2: stretched subnets, MAC/IP routes | no | – |

## Scale and limits

Hard limits come from the design; everything else is bounded by the gateway's CPU, memory
and FRR, and has **not been measured yet** (the lab runs 2 tenants on 2 pairs; a scale test
is on the [roadmap](development.md#roadmap)).

| What | Limit | Where it comes from |
|---|---|---|
| Stitched networks (tenant VRFs) per gateway | 65535 by design; practically far less, untested | 16 function bits per locator give one End.DT46 SID per network (`networks[].sid`). Each network costs a VRF, a bridge and a VXLAN device, an FRR VRF with its own BGP instance, prefix-lists and route-maps. |
| Partitions (gateway pairs) | 65536 with the default `/32` block and 16 node bits | One locator (`/48`) per pair, shared by both gateways. |
| BGP sessions per gateway | 1 per exit it is attached to (the lab: 2) | Gateways only peer with their exits; the exits relay the VPN routes between the partitions (a ladder in the lab; mesh or route servers, see [Configuration](configuration.md#requirements-on-the-environment)). A full mesh between gateways (`peers[].address`) is still possible. |
| VNIs | 24 bit | VXLAN. VNIs are local to a partition, so they don't add up. |
| VPN prefixes per peer | `maxPrefixes`, default 10000 per address family | Safety net; exceeding it tears the session down. |
| Throughput | CPU-bound, not measured | Encap and decap are done by the Linux kernel in software (no XDP, no offload); see [scaling bandwidth](#scaling-bandwidth). |
| Overhead per packet | +48 B (IPv6 + SRH), +50 B more in a DCI network | Every hop must fit tenant MTU + overhead; open-dci validates and raises the DCI devices. |
| Reconcile | every 10 s (`run -i`) | Each run reads the whole FRR running-config; its cost grows with the number of networks. |

## Scaling bandwidth

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
[roadmap](development.md#roadmap) should measure packets per second per core, per
gateway and per pair.
