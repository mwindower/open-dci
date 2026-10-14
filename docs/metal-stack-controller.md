# dci-controller in detail

How dci-controller derives the gateways' networks from metal-apiserver, how it hands them
over, and why. The user's view: [dci-controller](https://mwindower.github.io/open-dci/docs/metal-stack/controller)
on the website. Code: `internal/metalstack` (mapping, client, fake API),
`internal/controller` and `cmd/dci-controller`; the gateway side is the API
(`api/opendci/v1`, `internal/api`, `internal/gateway/apistore.go`).

## The round

Every `interval` the controller lists all private networks once (admin API), maps them per
partition and calls `SetNetworks` (owner `dci-controller`) on every gateway of that
partition. `SetNetworks` is declarative and idempotent: an unchanged set changes nothing on
the gateway, a changed one makes it reconcile at once.

- metal-apiserver fails: nothing is pushed, the gateways keep their networks.
- A gateway fails: it is retried next round; the others are pushed anyway.
- The gateways persist their API networks (`/var/lib/open-dci/api-networks.json`), so a
  restart of a gateway or the controller changes nothing. Only a successful answer
  without a network (or without its label) removes it.
- A gateway with the API enabled that has never received a write doesn't reconcile at all,
  so it never removes networks it merely doesn't know about yet (e.g. after losing the
  file). The controller's first round fills it.
- Global checks, once per round: route-target collisions and crowded groups (skipped
  everywhere), and stitched groups with networks in partitions without gateways
  (`unserved`).

## Mapping

How the networks are derived. The controller computes the same result for both gateways
of a pair, and every gateway gets the same route targets:
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
- `created`: the metal-stack network's `created_at`, passed on to the gateway.
- `sid`: assigned by the gateway (`config.AssignSIDs`), not the controller, because only the
  gateway knows its static networks: the VNI if it fits 16 bits and is free. metal-stack's
  VRF IDs go up to 131072 by default, so larger ones get a SID hashed from the VNI.
  Networks are served oldest first (`created`), so a new network never moves an existing
  SID; only deleting a network can move a younger one that collided with it (rare: it
  needs a hash collision). Both gateways of a pair compute the same SIDs from the same
  input. Wider SID functions would avoid hashing, but FRR 10.4.1 encodes functions beyond
  16 bits wrongly into the VPN routes.
- On the gateway, static `networks` win: an API network that conflicts with one (VRF, VNI,
  SID, table, route target) or is invalid is skipped and reported (`SetNetworks` response,
  `open_dci_api_skipped`, `dci_controller_gateway_skipped`); the others are applied. Between
  API networks of different owners, the older one wins.


## Label changes in detail

Labels come and go while traffic flows. What each change touches (e2e
`TestLabelChanges`, with continuous pings between the untouched members):

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
  global view can detect them. The controller sees all groups, so it skips the colliding
  ones everywhere, and reports them.

The controller takes the name from a label on the private networks (`metalStack.label`,
e.g. `open-dci.io/stitch: prod`). Label values are only unique within a project, so the
name is `<projectID>/<value>`: nothing is stitched across projects, a project can have
several stitched networks, and stitching is an explicit opt-in by the tenant. Stitching all
private networks of a project automatically was implemented once and dropped: a project's
networks in one partition are usually separate on purpose.

The VRF ID of a designated anchor network as the RT local part was considered and dropped:
it is unique within one metal-api without hashing, but the RT depends on the anchor network
staying alive, and it isn't unique across several metal-api instances.

The RD doesn't depend on the RT (`<routerID>:<sid>`), so derived RTs need no RD scheme.
