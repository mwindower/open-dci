# open-dci

<p align="center">
  <img src="website/static/img/mascot.svg" width="236" alt="open-dci's mascot: a yellow weaver bird weaving a blue and a green thread, one per tenant, through the knots of partitions A, B and C.">
</p>

[![ci](https://github.com/mwindower/open-dci/actions/workflows/ci.yaml/badge.svg)](https://github.com/mwindower/open-dci/actions/workflows/ci.yaml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**Stitch EVPN tenant VRFs across independent EVPN/VXLAN domains using SRv6 L3VPN.**

`open-dci` turns an FRR-based Linux box at the exit of an EVPN fabric, e.g. a
[metal-stack](https://metal-stack.io) partition, into a DCI gateway:
- it provisions the tenant VRFs as EVPN L3VNIs (VRF, bridge, VXLAN device) with the
  tenant's VNI in that partition, so the fabric sees an ordinary VTEP
- the VRFs are exported as VPNv4/v6 with an SRv6 End.DT46 SID
- remote routes come back as EVPN type-5
- each partition keeps its own VNIs, RTs and ASNs

It never rewrites `frr.conf`. It adds its lines via `vtysh` to the gateway's base config
and puts them back whenever the base system reloads its config.

## Why open-dci

Like the weaver bird, which weaves many threads into one nest, open-dci weaves each
tenant's networks from independent fabrics into one.

- **One network per tenant, across all your sites.** Every partition keeps its own EVPN
  fabric, VNIs, route targets and ASNs. open-dci joins a tenant's networks into one routed
  network, IPv4 and IPv6, and keeps tenants apart from each other.
- **No fabric surgery.** The gateways join the fabric like any VTEP. Leaves and spines stay
  as they are, and the network between the sites only needs plain IPv6.
- **Stock Linux and FRR.** No custom data plane, no special hardware: SRv6 in the Linux
  kernel, BGP in FRR, on any server.
- **Fails gracefully, maintains losslessly.** Anycast gateway pairs, dual-attached to two
  exits, with BFD: losing a gateway or an exit costs well under a second. `open-dci drain`
  takes a gateway out without losing a single packet, and a gateway that can't forward
  withdraws itself ([measured](docs/performance.md)).
- **Safe by default.** Prefix allowlists per network, route-target filters and prefix
  limits per peer, no DCI route target in any fabric, and an SRv6 domain closed at its edge.
- **Hands-off.** One YAML file per gateway. open-dci reconciles continuously, survives
  config reloads of the base system and reports its health in `status`, as Prometheus
  metrics and on a health endpoint.

**Use cases**
- A tenant's private networks in several [metal-stack](https://metal-stack.io) partitions
  or datacenters, routed as one.
- Availability zones with independent fabrics and failure domains, connected per tenant.
- Fabrics with different VNI, route-target or ASN plans joined without renumbering, e.g. during a migration.
- Many isolated tenants over one shared IPv6 core.

> [!WARNING]
> **Alpha.** open-dci is at an alpha stage: configuration and behaviour may still change
> incompatibly, and it is not ready for production. It is tested end to end in a
> [containerlab lab](docs/lab.md) that runs in CI.
>
> **L3 stitching only.** Tenant IP prefixes are routed between partitions (EVPN type-5 ↔
> VPNv4/v6). **L2 is not supported**: no stretched subnets, no MAC/IP (type-2) routes, no
> L2VNIs across partitions.

<p align="center">
  <img src="website/static/img/logical-view.svg" width="960" alt="Logical view: three partitions, each with its own private network and VNI per tenant. Tenant 1 has 10.0.16.0/24 with VNI 3981 in A, 10.0.32.0/24 with VNI 4011 in B and 10.0.48.0/24 with VNI 5011 in C; tenant 2 has 10.0.17.0/24 (VNI 3982), 10.0.33.0/24 (VNI 4012) and 10.0.49.0/24 (VNI 5012). The gateway pair of each partition exports every tenant VRF with its own SRv6 SID; SRv6 L3VPN joins each tenant's networks into one routed network (route targets 65535:1001 and 65535:1002). Tenants stay separate.">
</p>

How a packet travels (per partition, the two exits, the gateway pair and the fabric are one box each):

<p align="center">
  <img src="website/static/img/packet-flow.svg" width="960" alt="Animated packet flow across three partitions joined by the DCI network, an IPv6-only core. Each partition shows its two exits, its redundant gateway pair (attached to both exits) and its fabric (spines, leaves, machines) as one box each. A tenant packet comes from partition A's fabric over VXLAN with VNI 3981 via either exit to either gateway of pair A, which provisioned the tenant VRF. The gateway encapsulates it in SRv6 to partition B's anycast SID and sends it in partition A's EVPN VRF for the transport (VNI 104100) to either exit, into the DCI network. Partition B's exits deliver it to either gateway of pair B, which decapsulates it (End.DT46) and forwards it with partition B's VNI 4011 via the exits into partition B's fabric. Partition C works alike.">
</p>

Between partitions only IPv6 is needed: exits and core see one locator prefix per gateway,
and nothing of tenants or VNIs. The SRv6 transport runs in one of two modes, which can be
mixed freely:

| Mode | SRv6 transport | Isolation from the fabric | Fits |
|---|---|---|---|
| DCI network (`transport.vrf`), **recommended** | in an EVPN VRF of the base config, over VXLAN through the fabric | by construction: the VRF only exists on gateways and exits | any setup, especially fabrics with untrusted devices in the underlay (e.g. tenant firewalls) |
| Default VRF | in the IPv6 underlay | by configuration: the exits must announce locators only to gateways and core, and filter the edge | gateways with their own routed uplink; no VXLAN overhead (−50 B) and no veth |

## Documentation

**User documentation:** [mwindower.github.io/open-dci](https://mwindower.github.io/open-dci/)
(sources in [`website/docs`](website/docs)).

| | |
|---|---|
| [Introduction](https://mwindower.github.io/open-dci/docs/introduction/what-is-open-dci) | what open-dci is, how it works, design decisions, capabilities and limits |
| [Getting started](https://mwindower.github.io/open-dci/docs/getting-started/requirements) | requirements, installation, quick start, trying it in the lab |
| [Configuration](https://mwindower.github.io/open-dci/docs/configuration/overview) | every field, route targets, networks from metal-apiserver |
| [Operation](https://mwindower.github.io/open-dci/docs/operation/commands) | commands, status, metrics, health and failover, maintenance, the SRv6 domain's edge, adding partitions |
| [metal-stack](https://mwindower.github.io/open-dci/docs/metal-stack/overview) | open-dci in metal-stack |

**Developer documentation:** [`docs/`](docs/README.md): how it works inside, measurements,
the lab and its routing tables, development and roadmap, design history.

Quick look at a gateway (details: [Quick start](https://mwindower.github.io/open-dci/docs/getting-started/quick-start)):

```sh
open-dci validate -c /etc/open-dci/config.yaml   # check the config
open-dci render   -c /etc/open-dci/config.yaml   # the FRR lines it will add
open-dci run      -c /etc/open-dci/config.yaml   # reconcile continuously
open-dci status   -c /etc/open-dci/config.yaml
```

## License

[MIT](LICENSE)
