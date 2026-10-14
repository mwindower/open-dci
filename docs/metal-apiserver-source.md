# The metal-apiserver source in detail

How open-dci derives networks from metal-apiserver, and why. The user's view:
[Networks from metal-apiserver](https://mwindower.github.io/open-dci/docs/configuration/metal-apiserver) on the website.
Code: `internal/source/metalstack`.

## Mapping

How the networks are derived. Both gateways of a pair compute the same result, and every
gateway the same route targets:
- **Groups:** `<project>/<label value>`.
- **One network per partition:** a group with two networks with the same value in one
  partition is stitched nowhere (reported as skipped) until one of them loses the label.
- A group is provisioned on the gateways of every partition it has a network in, even if
  that is the only partition (yet).
- `vrf` is `vrf<VRF ID>`, `vni` the VRF ID of the network in the own partition.
- `aggregates`: the prefixes of all the group's networks; each lies in one partition.
- `routeTarget`: `<routeTargetASN>:<2^24 + fnv32a(group) mod (2^32 − 2^24)>`, as in
  [below](#deriving-route-targets). It is outside the VNI range, so it
  can't collide with an EVPN auto RT. Groups whose route targets collide are skipped.
- `sid`: the VNI if it fits 16 bits and is free. metal-stack's VRF IDs go up to 131072 by
  default, so larger ones get a SID hashed from the VNI. Networks are served oldest first
  (`created_at`), so a new network never moves an existing SID; only deleting a network can
  move a younger one that collided with it (rare: it needs a hash collision). Wider SID
  functions would avoid hashing, but FRR 10.4.1 encodes functions beyond 16 bits wrongly
  into the VPN routes.
- Static `networks` win: a derived network that conflicts with one (VRF, VNI, SID, table,
  route target) or is invalid is skipped and reported; the others are stitched anyway.

What it means in operation:
- open-dci polls every `interval`. A change takes effect at the next reconcile, right after
  the poll.
- A failed poll changes nothing. open-dci keeps the last good answer, also across
  restarts: `run` caches it next to the state file
  (`/var/lib/open-dci/metal-stack-networks.json`). Only a successful answer without a
  network (or without its label) removes it.
- Without any answer and without a cache, open-dci doesn't reconcile at all, so it never
  removes networks it merely doesn't know about yet.
- `render`, `diff`, `apply` and `status` use the cache; `--refresh` polls first.
- `status` and the metrics show the source's state: [Metrics and health](https://mwindower.github.io/open-dci/docs/operation/metrics).


## Label changes in detail

Labels come and go while traffic flows. What each change touches (e2e
`TestSourceLabelChanges`, with continuous pings between the untouched members):

| Change | Effect |
|---|---|
| A network gets the label (new partition in the group) | That partition's gateways provision its VRF. The other members only get its prefixes added to their allowlists; their VRFs, SIDs and route targets stay, and no line of theirs is touched. |
| A network loses the label | That partition's gateways remove its VRF. The others only lose its prefixes. |
| Down to one partition | The last member stays provisioned (idle until a partner returns), so nothing is rebuilt when one does. |
| Another group appears or disappears | Nothing of the existing groups changes: the allowlists and the route-target filter number their entries by content, not by position, so adding or removing one never renumbers the others. |
| A network's label value changes | It leaves one group and joins the other: new route target for that network only. Its connectivity to the old group ends, by intent. |
| A second network in a partition gets the same value | **The whole group is skipped** until one of the two loses the label: the one change that interrupts a working stitched network. |

A newly provisioned network doesn't count for the [health checks](https://mwindower.github.io/open-dci/docs/operation/health-and-failover#an-unhealthy-gateway-withdraws-itself)
until it has passed them once: both gateways of a pair provision it at the same time, and
withdrawing for its first seconds would take the partition down for everyone.

## Deriving route targets

If every stitched network has a stable **name**, its RT can be computed instead of
allocated:

```
RT = <admin ASN2> : 2^24 + (fnv32a(name) mod (2^32 − 2^24))
```

- The RT is deterministic, and no registry is needed.
- It can't collide with a VNI (auto RTs on the EVPN side).
- The admin part is a 2-byte namespace (`routeTargetASN`), independent of the gateways'
  ASNs: after a 4-byte ASN the local part only has 16 bits, too few for hashing.
- Hash collisions between two names are unlikely but possible. Only a component with a
  global view can detect them. The source sees all groups, so every gateway skips the
  colliding ones, and reports them.

The source takes the name from a label on the private networks (`source.metalStack.label`,
e.g. `open-dci.io/stitch: prod`). Label values are only unique within a project, so the
name is `<projectID>/<value>`: nothing is stitched across projects, a project can have
several stitched networks, and stitching is an explicit opt-in by the tenant. Stitching all
private networks of a project automatically was implemented once and dropped: a project's
networks in one partition are usually separate on purpose.

The VRF ID of a designated anchor network as the RT local part was considered and dropped:
it is unique within one metal-api without hashing, but the RT depends on the anchor network
staying alive, and it isn't unique across several metal-api instances.

The RD doesn't depend on the RT (`<routerID>:<sid>`), so derived RTs need no RD scheme.
