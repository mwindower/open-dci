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

## Reference

### `gateway`

| Field | Default | Meaning |
|---|---|---|
| `locator` | required | This gateway's SRv6 locator, e.g. `fd00:dc1:a::/48`. The first address (`<locator>::1`) becomes the gateway loopback: the VPN session endpoint and the SRv6 encap source. Function 0 is never allocated as a SID. |
| `locatorBlock` | required | Contains the locators of all gateways, e.g. `fd00:dc1::/32`. Traffic to it is routed into the transport. |
| `nodeLength` | `16` | Node bits of the locator. Block length + node length must equal the locator's prefix length. 16 function bits follow. |
| `asn` | discovered | ASN of the existing default BGP instance. If set, it must match the running FRR. |
| `routerID` | discovered | Router-id of the existing BGP instance, used for route distinguishers. If set, it must match the running FRR. |

### `transport`

| Field | Default | Meaning |
|---|---|---|
| `vrf` | empty | Existing EVPN VRF (a "DCI network") that carries the SRv6 transport. Empty: the transport is routed in the default VRF. |
| `mtu` | `9166` | DCI network mode only: MTU for the DCI VRF's bridge, VXLAN port, SVI and the veth pair. It must be ≥ `tenantMTU` + 48. |
| `tenantMTU` | `9000` | Largest tenant packet. Only used to validate `mtu`. |
| `veth`, `vethPeer` | `dci0`, `dci1` | DCI network mode only: names of the veth ends in the default VRF and in the DCI VRF. |

### `peers[]`

| Field | Meaning |
|---|---|
| `address` | Loopback of a remote gateway (`<its locator>::1`). It must be inside `locatorBlock` and not inside the own locator. |
| `asn` | The remote gateway's ASN. A different ASN gives eBGP multihop, an equal one iBGP. |

### `networks[]`

| Field | Default | Meaning |
|---|---|---|
| `vrf` | required | Existing tenant VRF to stitch. |
| `routeTarget` | required | `<asn>:<nn>` or `<ipv4>:<nn>`. It identifies the stitched network across all partitions and must be the same on all its gateways. |
| `rd` | `<routerID>:<nn>` | Route distinguisher for the VPN export. The default takes `<nn>` from `routeTarget`. |

## Validation

Besides syntax, `validate` (and every other command) rejects:
- a locator outside `locatorBlock`, with host bits set, or not matching `nodeLength`
- peers inside the own locator, outside the block, duplicated or without ASN
- VRFs used twice, or a tenant VRF that is also the transport VRF
- a transport MTU that can't carry `tenantMTU` + 48 B
- invalid route targets or distinguishers

At runtime, `apply`/`run`/`diff`/`status` also check the system:
- the VRFs exist in the kernel
- a BGP instance exists for each of them
- ASN and router-id match the config, if they are set there

## Requirements on the environment

**Both modes:**
- FRR ≥ 10 (tested 10.6) with bgpd, zebra and staticd, and the integrated config (`vtysh`).
  Linux ≥ 5.14 (End.DT46).
- The base system provides the tenant VRFs as EVPN L3VNIs, each with a
  `router bgp <asn> vrf <name>` instance, and a default BGP instance with a router-id.
- No route targets are needed on the EVPN side: auto RTs work across partitions and ASNs
  (see the [Phase 0 findings](phase0-findings.md)).

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
