# Configuration

`open-dci` reads one YAML file, by default `/etc/open-dci/config.yaml`. Unknown fields are
rejected. `open-dci validate -c FILE` checks a file without touching the system.

## Example

The lab's gw-a (`lab/configs/gw-a/open-dci.yaml`), with the transport in a DCI network:

```yaml
gateway:
  locator: fd00:dc1:a::/48        # this gateway; its loopback is fd00:dc1:a::1
  locatorBlock: fd00:dc1::/32     # all gateways' locators
transport:
  vrf: vrf104100                  # DCI network of the base config; omit it for the default VRF
peers:
  - {address: "fd00:dc1:b::1", asn: 4200000026}   # remote gateway loopbacks
networks:
  - vrf: vrf3981                  # created by open-dci
    vni: 3981                     # the tenant's VNI in this partition
    routeTarget: "65535:1001"     # the stitched network's identity, same on all gateways
    prefixes:                     # its address space in all partitions, same on all gateways
      - 10.0.16.0/24 le 32
      - 10.0.32.0/24 le 32
      - 2001:db8:16::/48 le 128
      - 2001:db8:32::/48 le 128
  - vrf: vrf3982
    vni: 3982
    routeTarget: "65535:1002"
    prefixes: [10.0.17.0/24 le 32, 10.0.33.0/24 le 32, 2001:db8:17::/48 le 128, 2001:db8:33::/48 le 128]
```

For the default VRF as transport, leave out the `transport` section (see the lab's gw-b).

## Reference

### `gateway`

| Field | Default | Meaning |
|---|---|---|
| `locator` | required | This gateway's SRv6 locator, e.g. `fd00:dc1:a::/48`. The first address (`<locator>::1`) becomes the gateway loopback: the VPN session endpoint and the SRv6 encap source. Function 0 is never allocated as a SID. |
| `locatorBlock` | required | Contains the locators of all gateways, e.g. `fd00:dc1::/32`. Traffic to it is routed into the transport. |
| `nodeLength` | `16` | Node bits of the locator. Block length + node length must equal the locator's prefix length. 16 function bits follow. |
| `asn` | discovered | ASN of the existing default BGP instance. If set, it must match the running FRR. |
| `routerID` | discovered | Router-id of the existing BGP instance, used for route distinguishers. If set, it must match the running FRR. |
| `vtep` | router-id | VXLAN source address (IPv4) of the provisioned L3VNIs. |

### `transport`

| Field | Default | Meaning |
|---|---|---|
| `vrf` | empty | EVPN VRF of the base config (a "DCI network") that carries the SRv6 transport. Empty: the transport is routed in the default VRF. |
| `mtu` | `9166` | DCI network mode only: MTU for the DCI VRF's bridge, VXLAN port, SVI and the veth pair. It must be ≥ `tenantMTU` + 48. |
| `tenantMTU` | `9000` | Largest tenant packet. Used to validate `mtu`, and as MTU of the provisioned L3VNIs' bridge and VXLAN device. |
| `veth`, `vethPeer` | `dci0`, `dci1` | DCI network mode only: names of the veth ends in the default VRF and in the DCI VRF. |

### `peers[]`

| Field | Meaning |
|---|---|
| `address` | Loopback of a remote gateway (`<its locator>::1`). It must be inside `locatorBlock` and not inside the own locator. |
| `asn` | The remote gateway's ASN. A different ASN gives eBGP multihop, an equal one iBGP. |
| `maxPrefixes` | Maximum VPN prefixes accepted from the peer per address family (default `10000`). FRR tears the session down when it is exceeded; it stays down until `clear bgp <address>`. |

Every peer only delivers routes that carry one of the configured `routeTarget`s (an inbound
route-map `DCI-PEER-IN` on the VPN sessions); routes with other RTs are dropped at the
session, before any VRF import.

### `networks[]`

| Field | Default | Meaning |
|---|---|---|
| `vrf` | required | Tenant VRF to stitch. open-dci creates it; a VRF of that name that open-dci didn't create is refused. |
| `routeTarget` | required | `<asn>:<nn>` or `<ipv4>:<nn>`. It identifies the stitched network across all partitions and must be the same on all its gateways. |
| `rd` | `<routerID>:<nn>` | Route distinguisher for the VPN export. The default takes `<nn>` from `routeTarget`. |
| `vni` | required | The tenant's L3VNI in this partition (1 – 16777215). open-dci provisions the VRF with it: VRF, bridge `dcibr<vni>`, VXLAN device `dcivx<vni>`, the FRR VRF with `vni`, and a BGP instance that advertises the routes as type-5. |
| `table` | the VNI | Kernel routing table of the VRF. |
| `prefixes` | required | The stitched network's address space in all partitions, in FRR prefix-list syntax: `PREFIX [ge N] [le N]`. A bare prefix matches exactly, `ge`/`le` extend it to more-specific ones (`10.0.16.0/24 le 32`: the /24 and every host in it). Only matching routes are exported from and imported into the VRF. Everything else stays in its partition, including a default route unless `0.0.0.0/0` / `::/0` is listed. Like `routeTarget`, the list is the same on all gateways of the network. A family without entries is not exchanged at all. |

## Validation

Besides syntax, `validate` (and every other command) rejects:
- a locator outside `locatorBlock`, with host bits set, or not matching `nodeLength`
- peers inside the own locator, outside the block, duplicated or without ASN
- VRFs used twice, or a tenant VRF that is also the transport VRF
- a transport MTU that can't carry `tenantMTU` + 48 B
- invalid route targets or distinguishers
- a missing `vni`, one out of range or used twice, a reserved or duplicate table
- missing `prefixes`, malformed entries (host bits set, `ge`/`le` out of range, `ge` > `le`)
  or duplicates, and a `maxPrefixes` below 1

At runtime, `apply`/`run`/`diff`/`status` also check the system:
- the transport VRF (if any) exists in the kernel, with a BGP instance
- ASN and router-id match the config, if they are set there
- `advertise-all-vni` in the default instance
- a network's devices, if they exist, were created by open-dci (see
  [Operation](operation.md#provisioned-networks)); it never takes over other devices

## Requirements on the environment

**Both modes:**
- FRR ≥ 10 (tested 10.6) with bgpd, zebra and staticd, and the integrated config (`vtysh`).
  Linux ≥ 5.14 (End.DT46).
- The base FRR config has a default BGP instance with a router-id that peers EVPN with the
  fabric (e.g. with the exit) and has `advertise-all-vni` in its `l2vpn evpn` address
  family.
- The gateway's VTEP address (router-id or `gateway.vtep`) is reachable in the underlay,
  and the fabric passes the tenant VNIs' type-5 routes to the gateway.
- The underlay carries VXLAN with the tenant MTU: ≥ tenant MTU + 50 B.
- No route targets are needed on the EVPN side: auto RTs work across partitions and ASNs
  (see the [Phase 0 findings](phase0-findings.md)).

**DCI network mode:**
- The base config has the DCI VRF as an EVPN L3VNI (VRF + SVI on a VLAN-aware bridge +
  VXLAN port) with a BGP instance.
- The fabric passes the DCI VNI's routes between gateway and exit.
- The exits route the DCI VRF to the other partitions.
- Every fabric link on the DCI path must carry ≥ tenant MTU + 98 B (SRv6 + VXLAN), e.g.
  9098 for 9000 B tenants.

**Default-VRF mode:**
- IPv6 unicast must be activated towards the underlay peers.
- Outbound filters must let the locator pass (it is originated locally).
- Every link on the path must carry ≥ tenant MTU + 48 B.
