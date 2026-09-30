# Where to run the gateway

srv6-dci runs on any FRR-based EVPN VTEP. Two placements make sense in a metal-stack
setting:
- **the tenant's metal-stack firewall.** This is what the lab and the
  [Phase 0b findings](phase0b-findings.md) cover.
- **dedicated gateway servers attached at the exits**, one pair per partition, owned by the
  provider.

The render, reconcile and status code is the same for both. They differ in trust,
lifecycle and who provides the tenant VRFs.

## Summary

The firewall is the right place for a proof of concept and small setups. For production with
many tenants, dedicated gateways are the better home. The deciding factor is trust, not
performance: firewall placement puts machines owned by tenants into one shared VPN/SRv6
trust domain.

| | metal-stack firewall | Dedicated gateway at the exit |
|---|---|---|
| Tenant VRFs / L3VNIs | already there (metal-networker) | must be provisioned (not implemented) |
| Trust domain of VPN and SRv6 | includes tenant machines | provider boxes only |
| Number of gateways | projects × partitions | ~2 per partition |
| Lifecycle | recreated on firewall rolling updates | static |
| Transport | DCI network or default VRF | default VRF (plain IPv6 uplink) |
| Changes to firewall and fabric | veth, ip rules, MTU, DCI network per firewall | none on firewalls; tenant VNIs towards the gateway port |
| Failure domain | one project | all tenants of a partition (needs a pair) |
| Tenant firewall policy on cross-partition traffic | possible (nftables) | no, as within a partition |

## Firewall placement

**For it:**
- Tenant VRFs, L3VNIs and the leaves' VNI filters already exist, so srv6-dci only augments.
- No extra hardware. Failures and load are spread per project.
- Cross-partition traffic passes the tenant's firewall.

**Against it:**
- **Isolation between tenants.** A metal-stack firewall is a machine in the tenant's
  project.
  - Control plane: VPNv4/v6 import is decided by route target only. Any firewall in the mesh
    can announce or import any RT, including another tenant's. This needs trusted route
    reflectors that filter each peer, in and out, to its own RTs.
  - Data plane: in DCI network mode, all firewalls share the DCI network. Any of them can
    send SRv6 packets to another firewall's End.DT46 SID, which decapsulates them into that
    tenant's VRF. This needs SID ingress filtering (by source locator, which can be spoofed
    inside the DCI VRF) or one DCI network per project. The latter puts tenant state back
    into exits and core.
- **Lifecycle.** Firewall rolling updates create new firewalls with a new router-id, ASN and
  loopback. Locators and peer lists churn with them (see [day2.md](day2.md)).
- **Invasiveness.** The veth pair, `ip rule` reordering, the DCI MTU and a DCI network per
  firewall reach into metal-networker and firewall-controller. The srv6-dci config must
  travel with every firewall.
- **Count.** One gateway per project and partition: many locators, sessions and RT/peer
  combinations.

## Dedicated gateways

**For them:**
- **One trust domain.** Only provider boxes speak VPN and SRv6, and tenants never reach a
  SID or the transport.
- **Few, stable nodes.** Static locators, a small full mesh or two route reflectors, and
  config in Git. Most of the [day-2 work](day2.md) disappears.
- **The transport already exists.** It is the default-VRF mode (fw-b in the lab): a plain
  IPv6 uplink to the core, no DCI network, no veth.
- **Firewalls stay untouched.** Cross-partition traffic takes machine → leaf → gateway,
  like traffic within a partition, which doesn't pass the firewall either.

**Against them:**
- **Someone must provide the tenant VRFs.** The gateway has to be an EVPN VTEP for every
  stitched tenant: VRF, VXLAN device, SVI, `router bgp <asn> vrf`, `vni`. Either
  - srv6-dci creates them in a "gateway mode". This changes the "augment, never own" scope.
  - Or metal-stack provisions them. It has no entity for a gateway that serves many projects
    yet, and the border leaf must pass the tenant VNIs to the gateway port.
- **A shared failure domain.** Redundant pairs are needed from the start (Phase 2).
- **Aggregate capacity.** Kernel SRv6 forwarding and FRR with many `router bgp vrf`
  instances on one box must be measured (packets per second, number of VRFs).
- **No tenant firewall policy** on cross-partition traffic.

## Direction

- The core tool stays placement-agnostic.
- Production target: dedicated gateways with default-VRF transport, plus a gateway mode that
  provisions tenant L3VNIs from config or metal-api.
- Firewall placement remains the option without extra hardware. It needs RT filtering on
  trusted route reflectors and SID ingress filtering, and it has the weaker trust model.
- Next steps: a lab variant with a gateway pair at exit-a/exit-b whose tenant VRFs srv6-dci
  owns, and a scale test.
