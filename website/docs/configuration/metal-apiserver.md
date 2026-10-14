# Networks from metal-apiserver

Optional. Instead of (or in addition to) writing `networks` by hand, open-dci reads them from
[metal-apiserver](https://github.com/metal-stack/metal-apiserver) (API v2). It uses only
what metal-stack already has, the private networks (type `child`) with their project,
partition, VRF ID, prefixes and labels; no new metal-stack entity.

Only networks that carry the stitch label are stitched, and a project's networks with the
same label value form one stitched network. The tenant opts in by labelling its networks,
e.g. `open-dci.io/stitch: prod` on its network in each partition. The project comes from the
network itself, so a stitched network never spans projects: `prod` in two projects makes two
stitched networks.

```yaml
source:
  metalStack:
    url: https://api.metal.example.com
    tokenFile: /etc/open-dci/metal-stack-token
    partition: fra-equ01              # the gateway's own partition
    routeTargetASN: 65535             # namespace of the derived route targets
    label: open-dci.io/stitch         # networks with this label, grouped by project and value
```

| Field | Default | Meaning |
|---|---|---|
| `url` | required | metal-apiserver's URL. |
| `tokenFile` | – | File with an API token with the admin viewer role, re-read on every poll. open-dci lists the private networks of all projects (labels can't be filtered by key on the server). |
| `partition` | required | The gateway's own partition: its networks there become the gateway's tenant VRFs. |
| `interval` | `30s` | Time between polls. |
| `routeTargetASN` | required | 2-byte admin part (1 – 65535) of the derived route targets. It is only a namespace, independent of the gateways' ASNs, which may well be 4-byte (the lab's gateways have 4200000016 and the like). A 4-byte value isn't accepted: the local part would only have 16 bits, too few for hashed names. |
| `label` | required | The network label key. Its value names the stitched network within the project; a project can have several. |
| `defaultRouteLabel` | – | A network label key whose value (`export` or `import`) sets `defaultRoute` for the gateway's own network of the group. |

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
  [Route targets](route-targets.md#deriving-route-targets-automatically). It is outside the VNI range, so it
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
- `status` and the metrics show the source's state: [Operation](../operation/metrics.md).

## Label changes

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

A newly provisioned network doesn't count for the [health checks](../operation/health-and-failover.md#withdrawing-an-unhealthy-gateway)
until it has passed them once: both gateways of a pair provision it at the same time, and
withdrawing for its first seconds would take the partition down for everyone.
