# What is open-dci

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
  withdraws itself ([measured](https://github.com/mwindower/open-dci/blob/main/docs/performance.md)).
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

:::warning
**Alpha.** open-dci is at an alpha stage: configuration and behaviour may still change
incompatibly, and it is not ready for production. It is tested end to end in a
[containerlab lab](../getting-started/try-the-lab.md) that runs in CI.

**L3 stitching only.** Tenant IP prefixes are routed between partitions (EVPN type-5 ↔
VPNv4/v6). **L2 is not supported**: no stretched subnets, no MAC/IP (type-2) routes, no
L2VNIs across partitions.
:::
