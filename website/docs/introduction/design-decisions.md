# Design decisions

- **L3 only, SRv6 L3VPN between domains.** Each partition keeps its own EVPN domain: VNIs,
  route targets, ASNs and failure domain. Only tenant prefixes cross between partitions,
  and the network in between needs nothing but IPv6.
- **SRv6 rather than MPLS.** An SRv6 SID is address and label at once: it routes to a
  gateway pair and selects the tenant VRF. The core stays plain IPv6, redundancy is two
  gateways announcing the same locator, and one SID serves IPv4 and IPv6. The costs: 48 B
  per packet, and SIDs must be protected at the [edge of the SRv6 domain](../operation/srv6-domain-edge.md).
- **Stock FRR and the Linux kernel.** The kernel forwards SRv6, FRR translates between
  EVPN and VPN routes. There is no custom data plane to maintain.
- **Dedicated gateways, not the switches.** Tenants never reach the SRv6 transport, the
  fabric stays untouched, and gateways are updated or replaced pair by pair. Servers also
  have no fixed hardware tables for VRFs and tunnels, and SRv6 VPN support in switch chips
  is limited. The price is an extra hop through a server and software forwarding.
- **Redundancy by anycast.** Both gateways of a partition own the same locator and SIDs, so
  a gateway failure needs no change anywhere else.
- **Add to the base config, own only what it provisions.** Your FRR and network
  configuration stays yours. open-dci adds its parts to the running FRR, creates only the
  tenant VRFs it stitches, and puts its parts back when the base system reloads.
- **Independent of metal-stack.** metal-stack is the first target, not a dependency: any
  FRR-based EVPN fabric works. Optionally, the gateways read the networks to stitch from
  [metal-apiserver](../configuration/metal-apiserver.md).

The reasoning in full: [Design decisions in detail](https://github.com/mwindower/open-dci/blob/main/docs/design-decisions.md).
