# Adding partitions and networks

What has to change where when the DCI grows, how the gateways' configs relate to each
other, and how route targets can be allocated. Today every gateway has its own hand-written
config, and nothing checks consistency *between* gateways. The last sections describe how
this could be automated (see the [roadmap](development.md#roadmap)). Where the gateways
should run at all: [placement.md](placement.md).

## Global and local parts of the config

| Item | Scope | Rule across gateways |
|---|---|---|
| `gateway.locatorBlock` | global | identical on every gateway |
| `gateway.locator` | per gateway | unique inside the block |
| `peers[]` | per gateway | every other gateway's loopback (`<locator>::1`) and ASN: a full mesh |
| `networks[].routeTarget` | per stitched network | identical on all its members, unique per stitched network |
| `networks[].vrf`, `transport` | local | none |

## Checklists

### A new partition (a new gateway G)

- **On G:**
  - pick a free locator inside the block
  - choose the transport mode
  - list all other gateways as `peers`
  - list its tenant VRFs with the RTs of the networks they join
- **On every existing gateway:** add G to `peers`. This is the only change that touches all
  locations.
- **Transport:**
  - DCI network mode: the DCI network must exist in G's partition, and its exits must route
    it to the other partitions.
  - Default-VRF mode: the underlay and the core must carry G's locator prefix.
  - In both modes, the other gateways' `locatorBlock` routes already cover G. The MTU rules in
    [Configuration](configuration.md#requirements-on-the-environment) apply to the new paths.
- **Partition fabric (leaves):** nothing. EVPN auto RTs work across VNIs and ASNs.

### A new stitched network

- Its VRFs must already exist on the member gateways (metal-stack: one private network per
  partition). Their VNIs may differ.
- Allocate one new route target (see below). On each member gateway, add
  `{vrf, routeTarget}`.
- No peer changes. Gateways that aren't members stay untouched.
- The network's prefixes must be disjoint across partitions.

### Adding or removing one partition of an existing stitched network

Only that partition's gateway changes: add or remove the `networks` entry.

### Removing a gateway

Remove it from every other gateway's `peers`. On each gateway, open-dci removes the lines it
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
   - its `peers`: the gateways that share at least one stitched network with it
   - its `networks`, and RTs computed from the names
   - checks that locators and RTs are unique across all gateways

   Adding a partition becomes a single change in one file.
2. **Route reflectors instead of the full mesh.** One or two VPNv4/v6 route reflectors
   accept every gateway with `bgp listen range <locatorBlock> peer-group GW`. Gateways peer
   only with the reflectors, so a new gateway needs no change on any other gateway. This also
   handles metal-stack firewall rolling updates, where a second firewall with its own ASN
   and router-id exists for a while. Still to verify: FRR reflects SRv6 VPN routes
   unchanged.
3. **Config from metal-api** (Phase 4). The inventory or the per-firewall config is
   generated from metal-api:
   - labelled private networks (project, partition, VRF ID) become stitched networks and
     their VRFs
   - firewalls become gateways

   Still open: where the locator's node ID comes from, and whether this runs in the
   firewall-controller or in a central generator. It stays outside open-dci itself, which
   keeps discovering everything else from kernel and FRR.
