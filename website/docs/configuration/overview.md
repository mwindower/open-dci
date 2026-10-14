# Configuration file

`open-dci` reads one YAML file, by default `/etc/open-dci/config.yaml`. Unknown fields are
rejected. `open-dci validate -c FILE` checks a file without touching the system.

| Section | What it sets |
|---|---|
| [`gateway`](gateway.md) | the gateway's identity: locator, loopback, locator block, ASN, router-id, site of origin |
| [`transport`](transport.md) | where the SRv6 transport runs: a DCI network (EVPN VRF) or the default VRF |
| [`peers`](peers.md) | the sessions that carry the VPN routes |
| [`networks`](networks.md) | the tenant VRFs to stitch, with their allowlists, aggregates and default route |

How to pick route targets for `networks`: [Route targets](route-targets.md).

## Example

Close to the lab's gw-a1 (`lab/configs/gw-a1/open-dci.yaml`), one of partition A's redundant pair,
with the transport in a DCI network:

```yaml
gateway:
  locator: fd00:dc1:a::/48        # shared with gw-a2 (anycast); SIDs fd00:dc1:a:<sid>::
  loopback: fd00:dc1:ff::a1       # own address: SRv6 encap source
  locatorBlock: fd00:dc1::/32     # all gateways' locators and loopbacks
transport:
  vrf: vrf104100                  # DCI network of the base config; omit it for the default VRF
peers:                            # the sessions to both exits; the exits relay
  - interface: uplink0            # the VPN routes between the partitions
  - interface: uplink1
networks:
  - vrf: vrf3981                  # created by open-dci
    vni: 3981                     # the tenant's VNI in this partition
    routeTarget: "65535:1001"     # the stitched network's identity, same on all gateways
    aggregates:                   # its ranges, one per partition, same on all gateways
      - 10.0.16.0/24              #   partition A
      - 10.0.32.0/24              #   partition B
      - 2001:db8:16::/48
      - 2001:db8:32::/48
  - vrf: vrf3982
    vni: 3982
    routeTarget: "65535:1002"
    aggregates: [10.0.17.0/24, 10.0.33.0/24, 2001:db8:17::/48, 2001:db8:33::/48]
```

For the default VRF as transport, leave out the `transport` section (see the lab's gw-b1).
Networks can also come through the gateway's [API](../operation/api.md), e.g. from
[dci-controller](../metal-stack/controller.md) (in the lab: tenant 2); then `networks` may be
empty.

## Validation

Besides syntax, `validate` (and every other command) rejects:
- a locator outside `locatorBlock`, with host bits set, or not matching `nodeLength`
- peers with both `interface` and `address`, duplicated interfaces, invalid names
- address peers inside the own locator, outside the block, duplicated or without ASN
- VRFs used twice, or a tenant VRF that is also the transport VRF
- a transport MTU that can't carry `tenantMTU` + 48 B
- invalid route targets, distinguishers or sites of origin, including local parts too wide
  for their admin part (more than 16 bits after a 4-byte ASN or an IPv4 address), and a
  default `siteOfOrigin` that doesn't fit (checked when `asn` is set, otherwise at render)
- a missing `vni`, one out of range or used twice, a reserved or duplicate table
- a `sid` out of range or used twice, a `loopback` inside the locator or outside the block
- a network without `aggregates` and `prefixes`; malformed aggregates (host bits set,
  `ge`/`le`, a default or host route) or overlapping ones; malformed prefixes (host bits
  set, `ge`/`le` out of range, `ge` > `le`) or duplicates, also of an aggregate; a
  `defaultRoute` other than `export`/`import`, or together with a prefix matching the
  default route; and a `maxPrefixes` below 1

At runtime, `apply`/`run`/`diff`/`status` also check the system:
- the transport VRF (if any) exists in the kernel, with a BGP instance
- ASN and router-id match the config, if they are set there
- `advertise-all-vni` in the default instance
- a network's devices, if they exist, were created by open-dci (see
  [Operation](https://github.com/mwindower/open-dci/blob/main/docs/internals.md#provisioned-networks)); it never takes over other devices
