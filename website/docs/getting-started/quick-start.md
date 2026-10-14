# Quick start

The gateway's base FRR config peers EVPN with the fabric and has `advertise-all-vni` (see
[Configuration](../configuration/overview.md)). Each network is a tenant VRF that open-dci
provisions with the tenant's VNI in that partition. Each partition runs a redundant pair of
gateways that share the locator (anycast) and pinned SIDs (the VNI by default), each with
its own loopback, and each attached to both exits of the partition (`uplink0`, `uplink1`). Two of the lab's six gateways
(`/etc/open-dci/config.yaml`, from `lab/configs/gw-*/open-dci.yaml`; gw-a2 and gw-b2 differ
only in `loopback`, partition C's pair is configured like partition B's):

<table>
<tr>
<th>gw-a1: partition A (pair with gw-a2), transport in a DCI network</th>
<th>gw-b1: partition B (pair with gw-b2), transport in the default VRF</th>
</tr>
<tr>
<td>

```yaml
gateway:
  locator: fd00:dc1:a::/48      # shared by the pair
  loopback: fd00:dc1:ff::a1     # own: encap source
  locatorBlock: fd00:dc1::/32   # all gateways
transport:
  vrf: vrf104100                # DCI network
peers:                          # only the two exits; they
  - interface: uplink0          # relay the VPN routes
  - interface: uplink1
networks:
  - vrf: vrf3981
    vni: 3981                   # tenant 1 in A, SID f8d
    routeTarget: "65535:1001"
    aggregates:                 # tenant 1, one per partition
      - 10.0.16.0/24
      - 10.0.32.0/24
      - 10.0.48.0/24
      - 2001:db8:16::/48
      - 2001:db8:32::/48
      - 2001:db8:48::/48
  - vrf: vrf3982
    vni: 3982                   # tenant 2 in A, SID f8e
    routeTarget: "65535:1002"
    aggregates:                 # tenant 2, one per partition
      - 10.0.17.0/24
      - 10.0.33.0/24
      - 10.0.49.0/24
      - 2001:db8:17::/48
      - 2001:db8:33::/48
      - 2001:db8:49::/48
```

</td>
<td>

```yaml
gateway:
  locator: fd00:dc1:b::/48      # shared by the pair
  loopback: fd00:dc1:ff::b1     # own: encap source
  locatorBlock: fd00:dc1::/32   # same everywhere
# no transport: default VRF (underlay)

peers:                          # only the two exits; they
  - interface: uplink0          # relay the VPN routes
  - interface: uplink1
networks:
  - vrf: vrf4011
    vni: 4011                   # tenant 1 in B, SID fab
    routeTarget: "65535:1001"   # = gw-a1's
    aggregates:                 # tenant 1, one per partition
      - 10.0.16.0/24
      - 10.0.32.0/24
      - 10.0.48.0/24
      - 2001:db8:16::/48
      - 2001:db8:32::/48
      - 2001:db8:48::/48
  - vrf: vrf4012
    vni: 4012                   # tenant 2 in B, SID fac
    routeTarget: "65535:1002"   # = gw-a1's
    aggregates:                 # tenant 2, one per partition
      - 10.0.17.0/24
      - 10.0.33.0/24
      - 10.0.49.0/24
      - 2001:db8:17::/48
      - 2001:db8:33::/48
      - 2001:db8:49::/48
```

</td>
</tr>
</table>

What must match across gateways: the `locatorBlock`, and per stitched network the
`routeTarget`, the `aggregates` and the `prefixes`. Within a pair, also the `locator`,
the `sid`s and the BGP ASN (which prevents loops). The VNIs are local to each partition.

**Safety net.**
- A partition announces each of a network's `aggregates` (e.g. a /24) instead of the
  machines' host routes in it. Only aggregates and `prefixes` leave or enter a VRF;
  anything else stays in its partition, including a default route unless a network shares
  one from its breakout partition (`defaultRoute`).
- Each peer only delivers routes with a configured route target, up to `maxPrefixes`
  (default 10000) per address family. See [Configuration](../configuration/networks.md).
- Forged SRv6 packets never reach a SID: the gateway drops packets from tenants to the
  locator block and packets to its locator from outside the block, the tenant VRFs have no
  fall-through to the main table, and the exits filter the edge of the SRv6 domain (see
  [Operation](../operation/srv6-domain-edge.md)).

```sh
open-dci validate -c /etc/open-dci/config.yaml
open-dci render   -c /etc/open-dci/config.yaml   # the FRR lines it will add
open-dci run      -c /etc/open-dci/config.yaml   # reconcile continuously
open-dci status   -c /etc/open-dci/config.yaml
```
