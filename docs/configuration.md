# Configuration

`open-dci` reads one YAML file, by default `/etc/open-dci/config.yaml`. Unknown fields are
rejected. `open-dci validate -c FILE` checks a file without touching the system.

## Example

The lab's fw-a (`lab/configs/fw-a/open-dci.yaml`), with the transport in a DCI network:

```yaml
gateway:
  locator: fd00:dc1:a::/48        # this gateway; its loopback is fd00:dc1:a::1
  locatorBlock: fd00:dc1::/32     # all gateways' locators
transport:
  vrf: vrf104100                  # existing VRF of a DCI network; omit it for the default VRF
peers:
  - {address: "fd00:dc1:b::1", asn: 4200000022}   # remote gateway loopbacks
networks:
  - vrf: vrf3981                  # existing tenant VRF
    routeTarget: "65535:1001"     # the stitched network's identity, same on all gateways
```

For the default VRF as transport, leave out the `transport` section (see the lab's fw-b).

A dedicated gateway (the lab's gw-a) has no tenant VRFs of its own. A `vni` makes
open-dci provision the VRF as an EVPN L3VNI itself:

```yaml
gateway:
  locator: fd00:dc1:a2::/48
  locatorBlock: fd00:dc1::/32
peers:
  - {address: "fd00:dc1:b2::1", asn: 4200000026}   # gw-b
networks:
  - vrf: vrf3982                  # created by open-dci
    vni: 3982                     # the tenant's VNI in this partition
    routeTarget: "65535:1002"
```

Both kinds of networks can be mixed on one gateway.

## Reference

### `gateway`

| Field | Default | Meaning |
|---|---|---|
| `locator` | required | This gateway's SRv6 locator, e.g. `fd00:dc1:a::/48`. The first address (`<locator>::1`) becomes the gateway loopback: the VPN session endpoint and the SRv6 encap source. Function 0 is never allocated as a SID. |
| `locatorBlock` | required | Contains the locators of all gateways, e.g. `fd00:dc1::/32`. Traffic to it is routed into the transport. |
| `nodeLength` | `16` | Node bits of the locator. Block length + node length must equal the locator's prefix length. 16 function bits follow. |
| `asn` | discovered | ASN of the existing default BGP instance. If set, it must match the running FRR. |
| `routerID` | discovered | Router-id of the existing BGP instance, used for route distinguishers. If set, it must match the running FRR. |
| `vtep` | router-id | VXLAN source address (IPv4) of provisioned L3VNIs. |

### `transport`

| Field | Default | Meaning |
|---|---|---|
| `vrf` | empty | Existing EVPN VRF (a "DCI network") that carries the SRv6 transport. Empty: the transport is routed in the default VRF. |
| `mtu` | `9166` | DCI network mode only: MTU for the DCI VRF's bridge, VXLAN port, SVI and the veth pair. It must be ≥ `tenantMTU` + 48. |
| `tenantMTU` | `9000` | Largest tenant packet. Used to validate `mtu`, and as MTU of provisioned L3VNIs' bridge and VXLAN device. |
| `veth`, `vethPeer` | `dci0`, `dci1` | DCI network mode only: names of the veth ends in the default VRF and in the DCI VRF. |

### `peers[]`

| Field | Meaning |
|---|---|
| `address` | Loopback of a remote gateway (`<its locator>::1`). It must be inside `locatorBlock` and not inside the own locator. |
| `asn` | The remote gateway's ASN. A different ASN gives eBGP multihop, an equal one iBGP. |

### `networks[]`

| Field | Default | Meaning |
|---|---|---|
| `vrf` | required | Tenant VRF to stitch. Without `vni` it must exist (it belongs to the base system); with `vni`, open-dci creates it. |
| `routeTarget` | required | `<asn>:<nn>` or `<ipv4>:<nn>`. It identifies the stitched network across all partitions and must be the same on all its gateways. |
| `rd` | `<routerID>:<nn>` | Route distinguisher for the VPN export. The default takes `<nn>` from `routeTarget`. |
| `vni` | empty | Provision the VRF as an EVPN L3VNI with this VNI (1 – 16777215): VRF, bridge `dcibr<vni>`, VXLAN device `dcivx<vni>`, the FRR VRF with `vni`, and a BGP instance that advertises the routes as type-5. It must be the tenant's VNI in this partition. For dedicated gateways; empty: augment an existing VRF. |
| `table` | the VNI | Kernel routing table of a provisioned VRF. Only with `vni`. |

## Validation

Besides syntax, `validate` (and every other command) rejects:
- a locator outside `locatorBlock`, with host bits set, or not matching `nodeLength`
- peers inside the own locator, outside the block, duplicated or without ASN
- VRFs used twice, or a tenant VRF that is also the transport VRF
- a transport MTU that can't carry `tenantMTU` + 48 B
- invalid route targets or distinguishers
- a `vni` out of range or used twice, a `table` without `vni`, a reserved or duplicate table

At runtime, `apply`/`run`/`diff`/`status` also check the system:
- the augmented VRFs exist in the kernel, with a BGP instance each
- ASN and router-id match the config, if they are set there
- with provisioned networks: `advertise-all-vni` in the default instance
- a provisioned network's devices, if they exist, were created by open-dci (see
  [Operation](operation.md#provisioned-networks)); it never takes over the base system's

## Requirements on the environment

**Both modes:**
- FRR ≥ 10 (tested 10.6) with bgpd, zebra and staticd, and the integrated config (`vtysh`).
  Linux ≥ 5.14 (End.DT46).
- The base system provides the tenant VRFs as EVPN L3VNIs, each with a
  `router bgp <asn> vrf <name>` instance, and a default BGP instance with a router-id.
- No route targets are needed on the EVPN side: auto RTs work across partitions and ASNs
  (see the [Phase 0 findings](phase0-findings.md)).

**Dedicated gateways (provisioned networks):**
- The base FRR config peers EVPN with the fabric (e.g. with the exit) and has
  `advertise-all-vni` in the default instance's `l2vpn evpn` address family.
- The gateway's VTEP address (router-id or `gateway.vtep`) is reachable in the underlay,
  and the fabric passes the tenant VNIs' type-5 routes to the gateway.
- The underlay carries VXLAN with the tenant MTU: ≥ tenant MTU + 50 B.

**DCI network mode:**
- The DCI VRF is an EVPN L3VNI (VRF + SVI on a bridge + VXLAN port) with a BGP instance.
- The leaves must send the DCI VNI to the gateway. With metal-core, the network must be
  attached to the firewall, which puts its VNI into the `match evpn vni` route-map.
- The exits route the DCI VRF to the other partitions.
- Every fabric link on the DCI path must carry ≥ tenant MTU + 98 B (SRv6 + VXLAN), e.g.
  9098 for 9000 B tenants.

**Default-VRF mode:**
- IPv6 unicast must be activated towards the underlay peers.
- Outbound filters must let the locator pass. metal-networker's `only-self-out` does,
  because the locator is originated locally.
- Every link on the path must carry ≥ tenant MTU + 48 B.
