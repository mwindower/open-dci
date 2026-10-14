# dci-controller

`dci-controller` connects open-dci to metal-stack: it reads the private networks from
[metal-apiserver](https://github.com/metal-stack/metal-apiserver) and sets every gateway the
networks it has to stitch, through the gateways' [API](../operation/api.md). Tenants decide
what is stitched by labelling their private networks; metal-stack needs no changes.

One controller serves all gateways. The gateways keep working without it: they keep their
networks when the controller or metal-apiserver is unavailable.

## How tenants stitch their networks

A tenant puts the same label value on its private networks in the partitions that should
form one network, e.g. `open-dci.io/stitch: prod` on its network in partition A and on its
network in partition B. The gateways of both partitions stitch them together.

- A project can have several stitched networks: one per label value.
- Networks of different projects are never stitched together, even with the same value.
- At most one network per partition can carry the same value. If a second one gets it, the
  whole stitched network is paused (reported in the controller's metrics) until one of the
  two loses the label.
- Labels can be added and removed at any time. Adding or removing a network only affects
  that network: the others keep running without interruption.
- Changing a network's label value moves it to another stitched network.

## Setup

1. On every gateway, enable the API: `open-dci run ... --api-listen <mgmt-ip>:9470
   --api-token-file /etc/open-dci/api-token` (optionally with TLS, see
   [API](../operation/api.md)). All gateways use the same token.
2. Run `dci-controller` once, e.g. on a management host (systemd unit and example config
   are in the release archive):

```yaml
metalStack:
  url: https://api.metal.example.com
  tokenFile: /etc/dci-controller/metal-stack-token   # admin viewer role
  label: open-dci.io/stitch           # the label tenants set on their networks
  routeTargetASN: 65535               # namespace of the derived route targets
interval: 30s
listen: "127.0.0.1:9471"              # /metrics and /healthz
gatewayTokenFile: /etc/dci-controller/gateway-token   # the gateways' API token
gateways:                             # every gateway: its partition and its API
  - {name: gw-a1, partition: fra-equ01, endpoint: "https://gw-a1.mgmt:9470", caFile: /etc/dci-controller/ca.pem}
  - {name: gw-a2, partition: fra-equ01, endpoint: "https://gw-a2.mgmt:9470", caFile: /etc/dci-controller/ca.pem}
```

| Field | Default | Meaning |
|---|---|---|
| `metalStack.url` | required | metal-apiserver's URL. |
| `metalStack.tokenFile` | – | An API token with the admin viewer role. |
| `metalStack.label` | required | The label key tenants set on their networks. |
| `metalStack.routeTargetASN` | required | A 2-byte number (1 – 65535) used as namespace of the route targets the controller derives. It is independent of your ASNs, which may be 4-byte. |
| `metalStack.defaultRouteLabel` | – | A label key whose value (`export` or `import`) sets [`defaultRoute`](../configuration/networks.md) for the network. |
| `interval` | `30s` | Time between rounds. |
| `owner` | `dci-controller` | The name the controller writes the gateways' networks under. Networks of other owners (e.g. set by hand) stay untouched. |
| `gateways` | required | Every gateway: `name`, `partition`, API `endpoint` (`http://` or `https://`), optional `caFile`. |
| `gatewayTokenFile` | required | The gateways' API token, re-read every round. |
| `listen` | – | Address for `/metrics` and `/healthz`. |

The networks in a gateway's config file keep working alongside; if a network from the
controller conflicts with one of them, the one from the file wins.

## Operation

- Every `interval`, the controller lists the networks once and sets each gateway the
  networks of its partition. Changes take effect within one interval.
- If metal-apiserver can't be reached, the controller changes nothing; the gateways keep
  their networks. `/healthz` answers 503 after three failed intervals.
- A gateway that can't be reached is retried in the next round; the others are updated.
- Metrics (`dci_controller_*`): `healthy`, `sync_timestamp_seconds`, `errors_total`,
  `groups`, `skipped{group,reason}` (stitched networks that are paused, e.g. two networks
  with the same value in a partition), `unserved{group,partition}` (labelled networks in a
  partition without gateways), and per gateway `gateway_up`, `gateway_networks`,
  `gateway_push_errors_total`, `gateway_skipped`.
- On a gateway, `open-dci status` shows which stitched network each VRF belongs to.

How the controller derives VRFs, VNIs and route targets from the networks:
[dci-controller in detail](https://github.com/mwindower/open-dci/blob/main/docs/metal-stack-controller.md).
