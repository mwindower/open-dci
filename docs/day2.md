# Adding partitions and networks

What has to change where when the DCI grows, how the gateways' configs relate to each
other, and how route targets can be allocated. Today every gateway has its own hand-written
config, and nothing checks consistency *between* gateways. The last sections describe how
this could be automated (see the [roadmap](development.md#roadmap)).

## Global and local parts of the config

| Item | Scope | Rule across gateways |
|---|---|---|
| `gateway.locatorBlock` | global | identical on every gateway |
| `gateway.locator` | per gateway | unique inside the block |
| `peers[]` | per gateway | the session to the exit (`interface`); or, without relaying exits, every other gateway's loopback and ASN (a full mesh) |
| `networks[].routeTarget` | per stitched network | identical on all its members, unique per stitched network |
| `networks[].prefixes` | per stitched network | identical on all its members: the network's address space in all partitions |
| `networks[].vni` | local | the tenant's VNI in this partition |
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
  exits' topology (see [Configuration](configuration.md#requirements-on-the-environment)): in
  a ladder, insert the partition between two neighbours, which touches only their exits; with
  route servers, nothing else changes.
  Existing gateways don't change; with a full mesh of `address` peers instead, every
  existing gateway would have to add G1 and G2.
- **Exits:** extend the SRv6 domain's edge filtering (see
  [Operation](operation.md#the-srv6-domain-and-its-edge)) to the new partition's exits.
- **Transport:**
  - DCI network mode: the DCI network must exist in the new partition, and its exits must
    route it to the other partitions.
  - Default-VRF mode: the underlay and the core must carry the locator and both loopbacks.
  - In both modes, the other gateways' `locatorBlock` routes already cover the pair. The MTU rules in
    [Configuration](configuration.md#requirements-on-the-environment) apply to the new paths.
- **Partition fabric (leaves):** nothing. EVPN auto RTs work across VNIs and ASNs.

### A new stitched network

- The tenant has a network (an L3VNI) in each partition (metal-stack: one private network
  per partition). Its VNIs may differ.
- Allocate one new route target (see below) and write down the network's address space in
  all partitions. On each member gateway, add `{vrf, vni, routeTarget, prefixes}` with the
  tenant's VNI in that partition; open-dci provisions the VRF. `routeTarget` and
  `prefixes` are the same everywhere.
- A new prefix in one partition (e.g. a new private network range) must be added to
  `prefixes` on **all** member gateways, or it won't be exchanged.
- No peer changes. Gateways that aren't members stay untouched.
- The network's prefixes must be disjoint across partitions.

### Adding or removing one partition of an existing stitched network

Only that partition's gateway changes: add or remove the `networks` entry.

### Maintenance on a gateway

`open-dci drain` on the gateway, wait a few seconds, then reboot or upgrade it, and run
`open-dci undrain` once it's back (see [Operation](operation.md#planned-maintenance-drain)).
One gateway of a pair at a time.

### Removing a gateway

With exit peering, nothing changes on other gateways; with `address` peers, remove it from
every other gateway's `peers`. On each gateway, open-dci removes the lines it
had applied for it (see [Operation](operation.md#frr-via-vtysh-never-touching-frrconf)).

## Choosing route targets

Any value works as long as it is unique per stitched network and identical on all its
members. Two constraints of the current implementation limit the choice:

- **Local part vs. VNIs.** EVPN auto RTs import by the local part only
  ([Phase 0 findings](phase0-findings.md#route-target-behaviour-frr-106-l3vni-tested-live-in-the-lab)).
  Until Phase 2 strips the DCI RT from EVPN exports, a leaked `X:N` is imported by any
  partition VRF whose VNI is `N`. So an RT's local part must not equal a VNI in use.
  VNIs are 24 bit, so **local parts ≥ 2^24 (16777216) can never collide**.
- **Width of the local part.** A 2-byte admin ASN allows a 32-bit local part
  (`65535:16777217`). A 4-byte ASN or an IPv4 admin part allows only 16 bits.
- **The default RD.** It is `<routerID>:<RT local part>`, and an IPv4-based RD has a 16-bit
  local part. RT local parts above 65535 therefore need an explicit `rd`. `validate` doesn't
  catch this yet.

## Deriving route targets automatically

This is not implemented yet. The idea: if every stitched network has a stable **name**,
its RT can be computed instead of allocated:

```
RT = <admin ASN2> : 2^24 + (fnv32(name) mod (2^32 − 2^24))
```

- The RT is deterministic, and no registry is needed.
- It can't collide with a VNI.
- Hash collisions between two names are unlikely but possible. Only a component with a
  global view (the inventory file or metal-api, see below) can detect them, so that component
  must reject them.

For metal-stack, the name can come from metal-stack entities:
- **A label on the private networks**, e.g. `dci.metal-stack.io/group: prod`. Labels are
  scoped to the project, so the name is `<projectID>/<group>`. This allows several stitched
  networks per project, and it makes stitching an explicit opt-in.
- **The project itself** (name = project ID) when "stitch all private networks of the
  project" is the intent and the project has one private network per partition.
- **The VRF ID of a designated anchor network** as the RT local part. It is unique within one
  metal-api without hashing. But the RT depends on the anchor network staying alive, and it
  is not unique across several metal-api instances. Hashing a name is preferred.

The RD should then no longer be derived from the RT. For example,
`<routerID>:<pinned SID function>` fits into 16 bits and ties in with pinned SIDs (Phase 2).

## Keeping the locations in sync

These options build on each other and are not implemented yet:

1. **One shared inventory file.** It is identical on every gateway and distributed via
   GitOps or configuration management. It contains the gateways
   (`{name, locator, asn, partition}`) and the stitched networks
   (`{name, routeTarget?, members: {gateway: vrf}}`). Each gateway finds its own entry (by a
   flag, or by the discovered ASN, router-id or loopback) and derives the rest:
   - with `address` peers: the gateways that share at least one stitched network with it
   - its `networks`, and RTs computed from the names
   - checks that locators and RTs are unique across all gateways

   Adding a partition becomes a single change in one file.
2. **No full mesh** (implemented): gateways peer only with their exit (`peers[].interface`),
   and the exits relay the VPN routes, in the lab in a ladder. Among many exits, two route
   servers (accepting every exit with `bgp listen range`) could replace the ladder, so a
   new partition's exits need no change on the others.
3. **Config from metal-api** (Phase 4). The inventory, or each gateway's config, is
   generated from metal-api: labelled private networks (project, partition, VRF ID) become
   stitched networks, and their VRF IDs the gateways' `vni`s.

   Still open: where this generator runs. It stays outside open-dci itself, which keeps
   discovering everything else from kernel and FRR. A proposal (a `dci-controller` and a
   network stitch entity in metal-api): [metal-stack.md](metal-stack.md).
