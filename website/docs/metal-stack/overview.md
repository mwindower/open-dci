# open-dci in metal-stack

The gateway needs no metal-stack changes. It peers EVPN with the exit like any fabric
member, and open-dci provisions the tenant VRFs from its config: each tenant's VNI in that
partition (the VRF ID of the tenant's metal-stack private network) as `networks[].vni`.
- The fabric must pass these VNIs' type-5 routes between leaves and gateway.
- **The exits** route the DCI network, or the IPv6 underlay in default-VRF mode, to the
  other partitions.

Instead of writing `networks` by hand, the gateways can read them from metal-apiserver
([`source.metalStack`](../configuration/metal-apiserver.md)): give each
gateway its partition, the network label that marks networks to stitch, and a read-only API
token (admin viewer) in `tokenFile`. `run` caches the
last answer in its state directory (`/var/lib/open-dci/`), so keep that directory
persistent.

The [lab](../lab/overview.md) reproduces this setup. How open-dci could become part of
metal-stack itself (a gateway role, a network stitch entity in metal-api, a controller):
[Integration proposal](integration-proposal.md).

## Networks from metal-apiserver (implemented)

This needs no change to metal-stack at all: open-dci can read the networks it stitches from
metal-apiserver itself ([`source.metalStack`](../configuration/metal-apiserver.md)).
It's optional; without it, the config file's `networks` are all there is. Only what
metal-apiserver already has is used: private networks with project, partition, VRF ID,
prefixes and labels. What belongs together is said by a label: only labelled networks are
stitched, and a project's networks with the same value (e.g. `open-dci.io/stitch: prod`)
form one stitched network. Tenants opt in themselves, a project can have several stitched
networks, and nothing is stitched across projects (the project is part of the group). Labels
can come and go while traffic flows: a change only touches the network whose label changed
([Label changes](../configuration/metal-apiserver.md#label-changes)).

Each gateway polls metal-apiserver with a read-only token and derives its own tenant
VRFs: the VRF ID of the network in its partition is the VNI, the prefixes of all the group's
networks are the aggregates, and the route target is hashed from the group's name. The
static config (gateway, transport, peers) stays a file. The limits compared to the full
[integration proposal](integration-proposal.md):
- no stitching across projects, and no quotas or permissions of its own: whoever may label
  a network may stitch it;
- a group with two networks with the same value in one partition is skipped;
- the global checks (RT collisions, overlapping prefixes) happen on every gateway, which
  reports and skips what doesn't fit, instead of rejecting it when it is created;
- renaming a label value gives the stitched network a new route target (a short
  interruption).

> [!NOTE]
> Statements about metal-stack internals (metal-api, metal-core, metal-networker,
> metal-roles) reflect the general architecture of metal-stack, not a review of its code.
> Check them against the version in use before building on them.
