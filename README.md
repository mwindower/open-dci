# open-dci

[![ci](https://github.com/mwindower/open-dci/actions/workflows/ci.yaml/badge.svg)](https://github.com/mwindower/open-dci/actions/workflows/ci.yaml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**Stitch EVPN tenant VRFs across independent EVPN/VXLAN domains using SRv6 L3VPN.**

`open-dci` turns an FRR-based Linux box into a DCI gateway between EVPN domains, e.g.
[metal-stack](https://metal-stack.io) partitions:
- tenant VRFs are exported as VPNv4/v6 with an SRv6 End.DT46 SID
- remote routes come back as EVPN type-5
- each partition keeps its own VNIs, RTs and ASNs

It runs wherever the tenant VRFs can be reached. The core of the tool is the same in both
places:

| Placement | Tenant VRFs | Fits |
|---|---|---|
| On the tenant's VTEP, e.g. a metal-stack firewall | **augmented**: they belong to the base system, open-dci only adds to them | no extra hardware, per-tenant failure domain |
| Dedicated gateway at the exit | **provisioned**: open-dci creates VRF, bridge and VXLAN device per tenant (`networks[].vni`) | one trust domain run by the provider, few stable nodes |

Either way it never rewrites `frr.conf`. It adds its lines via `vtysh` and puts them back
whenever the base system reloads its config.

> **Status: experimental.** Tested end to end in a [containerlab lab](lab/README.md) that
> runs in CI. Redundancy and RT hygiene are next on the [roadmap](docs/development.md#roadmap).

<p align="center">
  <img src="docs/packet-flow.svg" width="960" alt="Animated packet flow in two scenes. First, open-dci on the tenant's metal-stack firewalls: a tenant-1 packet from m-a travels over VXLAN with VNI 3981 to fw-a, SRv6-encapsulated in the DCI network (VNI 104100) to the exit, as plain IPv6 through the core and partition B's underlay to fw-b, which decapsulates it (End.DT46) and forwards it with partition B's VNI 4011 to m-b. Second, dedicated gateways at the exits: a tenant-2 packet from m-a2 travels over VXLAN with VNI 3982 through the fabric to gw-a, whose tenant VRF open-dci provisioned, SRv6 through exits and core to gw-b, and with VNI 4012 via leaf-b to m-b2.">
</p>

Between partitions only IPv6 is needed: exits and core see one locator prefix per gateway,
and nothing of tenants or VNIs. The SRv6 transport runs in one of two modes, which can be
mixed freely:

| Mode | SRv6 transport | Fits |
|---|---|---|
| DCI network (`transport.vrf`) | in an EVPN VRF, over VXLAN through the fabric | metal-stack firewalls: the DCI network is just another metal-stack network |
| Default VRF | in the IPv6 underlay | gateways with their own routed uplink, e.g. dedicated gateways |

## Why this design

- **L3 only, SRv6 L3VPN between domains.** Stretching EVPN would couple the partitions'
  VNIs, RTs, ASNs and failure domains. Exchanging only type-5 prefixes as VPNv4/v6 keeps
  every EVPN domain independent. The inter-partition network needs nothing but IPv6 and
  one locator prefix per gateway: no tenant state, no VNIs, no MPLS.
- **Stock FRR and the Linux kernel.** End.DT46 in the kernel and FRR's EVPN ↔ VPN
  re-origination already do the job ([Phase 0](docs/phase0-findings.md)). No custom
  data plane means nothing to maintain beyond configuration.
- **Add, don't replace.** The gateway lives next to a base system (metal-networker,
  or an operator's FRR config) that keeps rewriting its own config. open-dci discovers
  ASN, router-id and devices from kernel and FRR, adds its lines, and reconciles
  continuously instead of owning `frr.conf`.
- **Augment on the firewall, provision on a dedicated gateway.** On a metal-stack
  firewall the tenant VRFs already exist, so the tool must not touch them. A dedicated
  gateway has no tenant VRFs of its own, so there the tool creates exactly those it
  stitches and marks them as its own. For production, dedicated provider gateways are the
  stronger trust model ([placement](docs/placement.md)).
- **Independent of metal-stack.** metal-stack is the first target, not a dependency: the
  code assumes no device names or metal-stack APIs, so any FRR-based EVPN fabric works.

## Quick start

```yaml
# /etc/open-dci/config.yaml
gateway:
  locator: fd00:dc1:a::/48        # this gateway; its loopback is fd00:dc1:a::1
  locatorBlock: fd00:dc1::/32     # all gateways' locators
transport:
  vrf: vrf104100                  # DCI network; omit for the default VRF
peers:
  - {address: "fd00:dc1:b::1", asn: 4200000022}
networks:
  - {vrf: vrf3981, routeTarget: "65535:1001"}
```

A dedicated gateway provisions its tenant VRFs instead. Its base FRR config needs
`advertise-all-vni` (see [Configuration](docs/configuration.md)):

```yaml
gateway:
  locator: fd00:dc1:a2::/48
  locatorBlock: fd00:dc1::/32
peers:
  - {address: "fd00:dc1:b2::1", asn: 4200000026}
networks:
  - {vrf: vrf3982, vni: 3982, routeTarget: "65535:1002"}   # vni: open-dci creates the VRF
```

```sh
open-dci validate -c /etc/open-dci/config.yaml
open-dci render   -c /etc/open-dci/config.yaml   # the FRR lines it will add
open-dci run      -c /etc/open-dci/config.yaml   # reconcile continuously
open-dci status   -c /etc/open-dci/config.yaml
```

## Documentation

| | |
|---|---|
| [Installation](docs/installation.md) | binary + systemd, container, metal-stack notes |
| [Configuration](docs/configuration.md) | all fields, validation, requirements per mode |
| [Operation](docs/operation.md) | commands, `status`, what exactly is changed in kernel and FRR |
| [Adding partitions and networks](docs/day2.md) | what changes where, route targets, keeping locations in sync |
| [Gateway placement](docs/placement.md) | metal-stack firewall vs. dedicated gateways at the exit, and why |
| [Lab](lab/README.md) | the containerlab lab and its e2e tests |
| [Routing tables](docs/lab-routing.md) | which node knows which routes, in both modes |
| [Development](docs/development.md) | layout, tests, CI, releases, roadmap |
| Design findings | [Phase 0](docs/phase0-findings.md): EVPN ↔ SRv6 feasibility, RT behaviour · [Phase 0b](docs/phase0b-findings.md): the firewall and DCI network design |

## License

[MIT](LICENSE)
