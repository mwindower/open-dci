# open-dci in metal-stack

open-dci works with metal-stack as it is: no changes to metal-stack are needed.

- **Gateways** are dedicated servers at the exits of each partition. They peer EVPN with
  the exits like any fabric member. Each tenant's VNI in a partition is the VRF ID of the
  tenant's private network there.
- **The fabric** passes these VNIs' routes between leaves and gateways, as it does for any
  VTEP.
- **The exits** connect the partitions: they route the gateways' transport (the DCI
  network, or the IPv6 underlay in default-VRF mode) to the other partitions, and relay
  the VPN routes. See the [requirements](../getting-started/requirements.md).

## Stitching driven by metal-apiserver

The gateways can read the networks to stitch from metal-apiserver instead of a config
file: tenants label their private networks (e.g. `open-dci.io/stitch: prod`), and the
gateways of every partition with such a network stitch them together. Labels can be
added and removed at any time, without interrupting the other networks. Setup and
behaviour: [Networks from metal-apiserver](../configuration/metal-apiserver.md).

Compared to a metal-stack-native integration, this has limits: stitching never spans
projects, there are no quotas or permissions beyond who may label a network, and
conflicts (two networks with the same value in one partition) are reported rather than
rejected when they are created.

How open-dci could become part of metal-stack itself (a gateway role, a network stitch
entity, a controller): [Integration proposal](https://github.com/mwindower/open-dci/blob/main/docs/metal-stack-proposal.md).
