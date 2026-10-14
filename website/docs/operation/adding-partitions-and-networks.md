# Adding partitions and networks

What has to change where when the DCI grows, and how the gateways' configs relate to each
other. With hand-written configs, nothing checks consistency *between* gateways, so follow
the rules below. With [networks from metal-apiserver](../configuration/metal-apiserver.md),
adding a stitched network needs no config change at all.

## Global and local parts of the config

| Item | Scope | Rule across gateways |
|---|---|---|
| `gateway.locatorBlock` | global | identical on every gateway |
| `gateway.locator` | per gateway | unique inside the block |
| `peers[]` | per gateway | the session to the exit (`interface`); or, without relaying exits, every other gateway's loopback and ASN (a full mesh) |
| `networks[].routeTarget` | per stitched network | identical on all its members, unique per stitched network |
| `networks[].aggregates`, `networks[].prefixes` | per stitched network | identical on all its members: the network's address space in all partitions; each aggregate in exactly one partition |
| `networks[].vni` | local | the tenant's VNI in this partition |
| `networks[].defaultRoute` | per partition | the same on both gateways of a pair: `export` where the breakout is, `import` where it's used |
| `networks[].vrf`, `transport` | local | none |

## Checklists

### A new partition (a new gateway pair G1/G2)

- **On G1 and G2:**
  - pick a free locator inside the block, the same for both (anycast), and a unique
    `loopback` per gateway inside the block but outside the locator
  - give both the same BGP ASN in their base config (loop prevention)
  - choose the transport mode
  - `peers: [{interface: uplink0}]`: the session to the exit
  - list its tenant VRFs with their VNIs in G's partition and the RTs of the networks they
    join
- **On the new partition's exits:** relay VPN routes to and from the gateways and join the
  exits' topology (see [Requirements](../getting-started/requirements.md)): in
  a ladder, insert the partition between two neighbours, which touches only their exits; with
  route servers, nothing else changes.
  Existing gateways don't change; with a full mesh of `address` peers instead, every
  existing gateway would have to add G1 and G2.
- **Exits:** extend the SRv6 domain's edge filtering (see
  [The SRv6 domain and its edge](srv6-domain-edge.md)) to the new partition's exits.
- **Transport:**
  - DCI network mode: the DCI network must exist in the new partition, and its exits must
    route it to the other partitions.
  - Default-VRF mode: the underlay and the core must carry the locator and both loopbacks.
  - In both modes, the other gateways' `locatorBlock` routes already cover the pair. The MTU rules in
    [Requirements](../getting-started/requirements.md) apply to the new paths.
- **Partition fabric (leaves):** nothing. EVPN auto RTs work across VNIs and ASNs.

### A new stitched network

- The tenant has a network (an L3VNI) in each partition (metal-stack: one private network
  per partition). Its VNIs may differ.
- Allocate one new route target (see [Route targets](../configuration/route-targets.md)) and write down the network's address space in
  all partitions. On each member gateway, add `{vrf, vni, routeTarget, aggregates}` with the
  tenant's VNI in that partition; open-dci provisions the VRF. `routeTarget` and
  `aggregates` (or `prefixes`) are the same everywhere.
- A new prefix in one partition (e.g. a new private network range) must be added to
  `aggregates` on **all** member gateways, or it won't be exchanged.
- No peer changes. Gateways that aren't members stay untouched.
- The network's prefixes must be disjoint across partitions.

### Adding or removing one partition of an existing stitched network

Only that partition's gateway changes: add or remove the `networks` entry.

### Maintenance on a gateway

`open-dci drain` on the gateway, wait a few seconds, then reboot or upgrade it, and run
`open-dci undrain` once it's back (see [Maintenance](maintenance.md)).
One gateway of a pair at a time.

### Removing a gateway

With exit peering, nothing changes on other gateways; with `address` peers, remove it from
every other gateway's `peers`. On each gateway, open-dci removes what it had configured for it.
