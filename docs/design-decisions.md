# Design decisions in detail

The long form of [Design decisions](https://mwindower.github.io/open-dci/docs/introduction/design-decisions) on the website.

- **L3 only, SRv6 L3VPN between domains.** Stretching EVPN would couple the partitions'
  VNIs, RTs, ASNs and failure domains. Exchanging only type-5 prefixes as VPNv4/v6 keeps
  every EVPN domain independent. The inter-partition network needs nothing but IPv6 and
  one locator prefix per gateway: no tenant state, no VNIs, no MPLS.
- **Why SRv6 rather than MPLS labels.** MPLS L3VPN would work the same way at the BGP
  level. But a label needs a transport: either the core runs MPLS (LDP or SR-MPLS), coupling
  it to the partitions, or the label is tunnelled in GRE or UDP. That means tunnel devices
  per remote gateway pair, set up outside FRR on Linux. With SRv6, the address is the
  tunnel and the label at once: the SID `fd00:dc1:b:fab::` routes to pair B and selects the
  tenant VRF.
  - The core is plain IPv6 and learns one prefix per partition.
  - Redundancy is two gateways announcing the same locator (anycast), and ECMP hashes the
    IPv6 flow label.
  - The kernel's End.DT46 serves IPv4 and IPv6 with one SID per VRF.
  - Securing the domain means ACLs on one prefix block.

  The costs: 48 B per packet (VXLAN: 50 B), SIDs that decapsulate anything that reaches
  them (hence the [edge filtering](https://mwindower.github.io/open-dci/docs/operation/srv6-domain-edge)), and few
  switch chips that terminate SRv6. Only the Linux gateways terminate it; the core just
  forwards IPv6.
- **Stock FRR and the Linux kernel.** End.DT46 in the kernel and FRR's EVPN ↔ VPN
  re-origination already do the job ([Phase 0](phase0-findings.md)). No custom
  data plane means nothing to maintain beyond configuration.
- **Dedicated, provider-owned gateways.** Only these boxes speak VPN and SRv6, so tenants
  never reach a SID or the transport, and a few stable nodes per partition keep locators
  and peers static ([day-2 operations](https://mwindower.github.io/open-dci/docs/operation/adding-partitions-and-networks)). The gateway joins the fabric like
  any VTEP; the fabric needs no changes beyond passing the tenant VNIs' routes.
- **Redundancy by anycast.** Both gateways of a partition own the same locator and SIDs.
  Remote gateways don't need to know which one is alive: the transport delivers to
  whichever is reachable, so a failure needs no BGP reconvergence of the VPN routes.
- **Why not on the switches?**
  The stitching could run on the exits or leaves themselves (SONiC uses FRR, too). open-dci
  deliberately puts it on dedicated Linux gateways:

  - **Data-plane support.** End.DT46 decap plus SRv6 encap with VPN SIDs, in the same box as
    EVPN/VXLAN, needs ASIC support. Many datacenter switch ASICs don't support SRv6 VPN at all
    or only in recent generations, and SONiC's SRv6 support covers only some platforms. The
    Linux kernel supports it on any server.
  - **Hardware tables.** On a switch, every tenant VRF, L3VNI, VXLAN tunnel and SRv6 encap
    entry competes for fixed tables (VRF IDs, next hops, tunnel and TCAM entries, shared LPM
    space). Depending on the ASIC, VRFs are typically limited to hundreds or a few thousand.
    Stitching N tenants across partitions adds N VRFs plus their remote routes to every
    switch that does it. A server's limits are memory and CPU, and are easy to grow.
  - **Blast radius and ownership.** The exits carry the whole partition's fabric and
    internet traffic and are managed by the fabric's own tooling (e.g. metal-core). Putting
    per-tenant DCI state on them couples every tenant change to the fabric. Dedicated gateways
    add to an untouched fabric, can be updated, restarted or replaced pair by pair, and fail
    over without the fabric noticing.
  - **Scale-out.** When one pair isn't enough, add gateways to the anycast group, another
    group (tenants spread across groups) or bigger servers, instead of upgrading switches
    (see [scaling bandwidth](scaling.md#scaling-bandwidth)).

  The price is an extra hop through a server and software forwarding, which is why its
  throughput needs measuring before production use.
- **Add to the base config, own only what it provisions.** The operator's base config
  (underlay, EVPN, optionally the DCI network) stays theirs. open-dci discovers ASN,
  router-id and devices from kernel and FRR, creates only the tenant VRFs it stitches
  (tagged as its own), and reconciles continuously instead of owning `frr.conf`.
- **Independent of metal-stack.** metal-stack is the first target, not a dependency: the
  code assumes no device names or metal-stack APIs, so any FRR-based EVPN fabric works.
  The metal-stack integration is a separate program,
  [dci-controller](https://mwindower.github.io/open-dci/docs/metal-stack/controller), which sets the gateways their networks
  from metal-apiserver through the gateway API (labelled networks, no new metal-stack
  entities).
