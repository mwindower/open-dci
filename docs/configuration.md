# Configuration

`open-dci` reads one YAML file, by default `/etc/open-dci/config.yaml`. Unknown fields are
rejected. `open-dci validate -c FILE` checks a file without touching the system.

## Example

The lab's gw-a1 (`lab/configs/gw-a1/open-dci.yaml`), one of partition A's redundant pair,
with the transport in a DCI network:

```yaml
gateway:
  locator: fd00:dc1:a::/48        # shared with gw-a2 (anycast); SIDs fd00:dc1:a:<sid>::
  loopback: fd00:dc1:ff::a1       # own address: SRv6 encap source
  locatorBlock: fd00:dc1::/32     # all gateways' locators and loopbacks
transport:
  vrf: vrf104100                  # DCI network of the base config; omit it for the default VRF
peers:                            # the session to the exit; the exits relay the
  - interface: uplink0            # VPN routes between the partitions
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

For the default VRF as transport, leave out the `transport` section (see the lab's gw-b1).

## Reference

### `gateway`

| Field | Default | Meaning |
|---|---|---|
| `locator` | required | The gateway's SRv6 locator, e.g. `fd00:dc1:a::/48`. The redundant gateways of a partition share it (anycast): they announce the same locator and, with the same `sid`s, the same SIDs. |
| `loopback` | `<locator>::1` | The gateway's own address: SRv6 encap source, and session endpoint for `address` peers. The default lies inside the locator (function 0 is never a SID), which only works for a single gateway. A redundant pair needs a unique loopback per gateway, inside `locatorBlock` but outside the locator; open-dci announces it next to the locator. |
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

The BGP sessions that carry the VPN routes, in one of two forms:

| Field | Meaning |
|---|---|
| `interface` | **To the exit (recommended).** The base config's existing session over this interface (e.g. the unnumbered `uplink0` to the exit). open-dci only activates VPNv4/v6 on it, with its filters. The exits relay the VPN routes between the partitions, so a gateway needs no session to remote gateways, and a new partition touches no existing gateway. |
| `address`, `asn` | **Direct to a remote gateway (full mesh).** Its loopback and ASN; a different ASN gives eBGP multihop (`update-source` = own loopback), an equal one iBGP. The address must be inside `locatorBlock` and not inside the own locator. |
| `maxPrefixes` | Maximum VPN prefixes accepted from the peer per address family (default `10000`). FRR tears the session down when it is exceeded; it stays down until `clear bgp <neighbor>`. A session to the exit carries the routes of **all** partitions: size it accordingly. |

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
| `sid` | the VNI | Function part of the network's End.DT46 SID, `<locator>:<sid in hex>::` (1 – 65535). Pinned, so the SID survives restarts and is identical on both gateways of a pair. Must be set if the VNI doesn't fit 16 bits. |
| `prefixes` | required | The stitched network's address space in all partitions, in FRR prefix-list syntax: `PREFIX [ge N] [le N]`. A bare prefix matches exactly, `ge`/`le` extend it to more-specific ones (`10.0.16.0/24 le 32`: the /24 and every host in it). Only matching routes are exported from and imported into the VRF. Everything else stays in its partition, including a default route unless `0.0.0.0/0` / `::/0` is listed. Like `routeTarget`, the list is the same on all gateways of the network. A family without entries is not exchanged at all. |

## Validation

Besides syntax, `validate` (and every other command) rejects:
- a locator outside `locatorBlock`, with host bits set, or not matching `nodeLength`
- peers with both `interface` and `address`, duplicated interfaces, invalid names
- address peers inside the own locator, outside the block, duplicated or without ASN
- VRFs used twice, or a tenant VRF that is also the transport VRF
- a transport MTU that can't carry `tenantMTU` + 48 B
- invalid route targets or distinguishers
- a missing `vni`, one out of range or used twice, a reserved or duplicate table
- a `sid` out of range or used twice, a `loopback` inside the locator or outside the block
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
- Linux with nf_tables (the gateway's ingress filter).
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

**Redundant gateways (a pair per partition):**
- Both gateways have the same `locator`, `networks` (so the same SIDs), and the same BGP
  ASN in their base config. The shared ASN makes each drop the routes the other one
  re-announced into the fabric, so neither re-exports or detours through its partner.
- Each has its own `loopback` and router-id. With `address` peers, remote gateways list both
  as `peers`; with `interface` peers, nothing changes elsewhere.
- The fabric (exit, core) spreads traffic to the shared locator over both gateways (ECMP)
  and falls back to the survivor.
- Known limit: a gateway whose transport is up but whose fabric side (EVPN) is broken still
  attracts traffic for the locator.

**Peering with the exit (`peers[].interface`):**
- open-dci adds `bgp disable-ebgp-connected-route-check` to the gateway's default instance:
  FRR tracks the remote SID as next hop of imported SRv6 VPN routes, and requires it to be
  directly connected for single-hop eBGP sessions, which a SID never is.
- The exits (their base config, not open-dci):
  - activate `ipv4 vpn` and `ipv6 vpn` towards their gateways, with `allowas-in 1`: the
    gateways' VPN routes carry the exit's own ASN, since they were learned via EVPN through
    the exit.
  - relay the VPN routes to each other (eBGP between the exits; a mesh, a ring or route
    reflectors). They import none of them.
  - need a default-VRF path to each other for these sessions: in FRR, the VPN address
    families only exist in the default BGP instance. In DCI-network partitions, where the
    exit's core link sits in the DCI VRF, that means an extra link or path (the lab's
    exit-a ↔ core `swp5`).
- Routes pass the exits with RD, RT and SID unchanged; the BGP next hop becomes the exit,
  which doesn't matter, since SRv6 forwards by the SID.

**The edge of the SRv6 domain** (see [Operation](operation.md#the-srv6-domain-and-its-edge)):
the exits must keep the locator block unreachable from anything but the gateways and the
core, and drop packets from the core into the block with a source outside it.

**Default-VRF mode:**
- IPv6 unicast must be activated towards the underlay peers.
- Outbound filters must let the locator pass (it is originated locally).
- Every link on the path must carry ≥ tenant MTU + 48 B.
