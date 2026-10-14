# Capabilities and limits

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
| A default route (internet breakout) of one partition for the others | yes, opt-in per gateway (`networks[].defaultRoute`); a local default route wins | yes (C → A) |
| Overlapping prefixes *within* one stitched network | no: the partitions share one routing domain | – |
| L2: stretched subnets, MAC/IP routes | no | – |

## Scale

| What | Limit |
|---|---|
| Stitched networks per gateway | up to 65535 by design; practically fewer, not measured yet |
| Partitions (gateway pairs) | 65536 with the default locator plan |
| BGP sessions per gateway | one per exit it is attached to |
| Throughput | bound by the gateway's CPU (software forwarding), not measured yet |
| Overhead per packet | 48 B (SRv6), plus 50 B inside a DCI network |

## Growing bandwidth

Every stitched packet crosses one gateway in each partition, so a gateway's uplinks carry
its traffic twice (in from the fabric, out to the core). To add capacity: bigger gateways,
both gateways of a pair active (they are, by anycast), each gateway attached to two exits,
more gateways sharing a locator, or several gateway groups each stitching a share of the
tenants. The exits and the core must include the IPv6 flow label in their ECMP hash.

Details on each limit and on scaling: [Scale limits and scaling bandwidth](https://github.com/mwindower/open-dci/blob/main/docs/scaling.md).
