# Networks from metal-apiserver

Optional. Instead of writing `networks` by hand, the gateways can read them from
[metal-apiserver](https://github.com/metal-stack/metal-apiserver). Tenants decide what is
stitched by labelling their private networks; metal-stack needs no changes.

## How tenants stitch their networks

A tenant puts the same label value on its private networks in the partitions that should
form one network, e.g. `open-dci.io/stitch: prod` on its network in partition A and on its
network in partition B. The gateways of both partitions stitch them together.

- A project can have several stitched networks: one per label value.
- Networks of different projects are never stitched together, even with the same value.
- At most one network per partition can carry the same value. If a second one gets it, the
  whole stitched network is paused (reported in `status` and the metrics) until one of the
  two loses the label.
- Labels can be added and removed at any time. Adding or removing a network only affects
  that network: the others keep running without interruption.
- Changing a network's label value moves it to another stitched network.

## Configuration

```yaml
source:
  metalStack:
    url: https://api.metal.example.com
    tokenFile: /etc/open-dci/metal-stack-token
    partition: fra-equ01              # the gateway's own partition
    routeTargetASN: 65535             # namespace of the derived route targets
    label: open-dci.io/stitch         # the label tenants set on their networks
```

| Field | Default | Meaning |
|---|---|---|
| `url` | required | metal-apiserver's URL. |
| `tokenFile` | – | File with an API token with the admin viewer role, re-read on every poll. |
| `partition` | required | The gateway's own partition. |
| `interval` | `30s` | Time between polls. |
| `routeTargetASN` | required | A 2-byte number (1 – 65535) used as namespace of the route targets open-dci derives. It is independent of your ASNs, which may be 4-byte. |
| `label` | required | The label key tenants set on their networks. |
| `defaultRouteLabel` | – | A label key whose value (`export` or `import`) sets [`defaultRoute`](networks.md) for the network. |

The networks in the config file (`networks`) keep working alongside. If a network from
metal-apiserver conflicts with one of them, the one from the file wins.

## Operation

- Changes take effect at the next poll (`interval`).
- If metal-apiserver can't be reached, nothing changes: open-dci keeps the last answer,
  also across restarts (`/var/lib/open-dci/` must be persistent). `status` and the
  [metrics](../operation/metrics.md) report the source as stale.
- `status` lists which stitched network each VRF belongs to, and any that can't be
  stitched, with the reason.

How open-dci derives VRFs, VNIs, SIDs and route targets from the networks:
[The metal-apiserver source in detail](https://github.com/mwindower/open-dci/blob/main/docs/metal-apiserver-source.md).
