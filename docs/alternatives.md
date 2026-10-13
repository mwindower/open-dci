# Alternatives

Which other products and projects can stitch tenant VRFs across EVPN/VXLAN fabrics with
SRv6 L3VPN (EVPN type-5 ↔ VPNv4/v6 with an SRv6 SID), and whether Cilium can. Why open-dci
looks the way it does: [README](../README.md#design-decisions).

State as of October 2026, taken from vendor documentation. None of it has been tested in
the lab.

## Vendor gateways

The router platforms do this as a built-in gateway function ("handoff", "stitching",
"re-origination"). They are closed, licensed boxes, and they own the whole router config.

| Vendor | OS / platform | EVPN-VXLAN ↔ SRv6 L3VPN | Notes |
|---|---|---|---|
| Juniper | Junos, MX (24.2R2 and later) | yes | Stitches EVPN-VXLAN type-5 to an SRv6 micro-SID tunnel through a Layer 3 VRF, IPv4 and IPv6. "Seamless type-5 stitching" and Apstra's "Integrated DCI" are EVPN-VXLAN ↔ EVPN-VXLAN instead. |
| Cisco | NX-OS, Nexus 9000 | not confirmed | The documented handoff is VXLAN EVPN ↔ MPLS-SR L3VPN (N9300-FX2, N9500 with -R line cards). IOS XR not checked. |
| Nokia | SR Linux | no | The multi-instance IP-VRF stitches EVPN (VXLAN or MPLS) with IP-VPN over MPLS only, on 7250 IXR and 7730 SXR. SR OS not checked. |
| H3C / Huawei | border devices | yes | Convert routes between SRv6 and EVPN VXLAN networks and advertise them as type-5 into the VXLAN side. |

## Open-source building blocks

None of these is a turnkey gateway.

- **FRR and the Linux kernel, configured by hand.** This is what open-dci automates. FRR has
  SRv6 L3VPN since 8.2; the re-origination itself works out of the box
  ([Phase 0](phase0-findings.md)). What open-dci adds is everything around it: the VRF and
  VXLAN device chain, pinned SIDs, route-target hygiene, aggregates, the locator gate, the
  edge filter, health checks and the workarounds for FRR bugs (see
  [Operation](operation.md)).
- **SONiC.** Uses FRR, has SRv6 uSID (SAI and FRR) since release 202305 and EVPN-VXLAN. A
  candidate for the same gateway on a switch; whether an ASIC does VXLAN decapsulation and
  SRv6 encapsulation in one pass depends on the platform and is unverified. Why open-dci
  doesn't run on switches: [README](../README.md#design-decisions) ("Why not on the
  switches?").
- **VPP (fd.io).** A fast data plane with SRv6 and VXLAN; the control plane (e.g. linux-cp
  plus FRR) is up to you.
- **GoBGP.** Speaks EVPN and SRv6 VPN (Prefix-SID attribute) but has no data plane; the
  kernel or VPP would have to be programmed separately.

## Cilium

Cilium can't do this stitching.

- **SRv6 L3VPN** arrived in Cilium 1.13 as a beta and was announced with Isovalent
  Enterprise 1.13; open-source Cilium only shows the SRv6 datapath maps
  (`cilium-dbg bpf srv6 vrf/policy/sid`). The Kubernetes node itself becomes the SRv6 PE.
- **EVPN** came with Isovalent Private Networks / Isovalent Networking for Virtualization
  (GA 2026, together with Cisco Nexus One): nodes peer with their top-of-rack switches over
  BGP unnumbered, advertise EVPN type-5 host routes (/32, /128), and each private network maps
  to a VRF of the fabric; Cilium does the VXLAN encapsulation in eBPF.

Both make the host an endpoint of one fabric (EVPN VTEP or SRv6 PE), not a gateway between
independent fabrics. That is the host-as-gateway approach open-dci dropped
([Phase 0b](phase0b-findings.md)), and metal-stack's firewalls aren't Kubernetes nodes
anyway. Cilium is complementary: a cluster running Cilium's SRv6 L3VPN could import the
VPN routes the open-dci gateways export, if route targets and SID format match.
OVN-Kubernetes has a similar proposal for EVPN on the nodes (OKEP-5088).

## How open-dci differs

- Stock FRR and the Linux kernel on a server, no licence and no ASIC dependency.
- Adds to an operator-owned base config and touches only what it provisions.
- Redundancy by anycast gateway pairs; the core only forwards IPv6.
- Closes the SRv6 domain at its edge itself (ingress filter on the gateways).

## Sources

- Juniper: [Seamless Type-5 to Type-5 stitching](https://www.juniper.net/documentation/us/en/software/junos/evpn/topics/task/configure-seamless-type5-stitching.html),
  [Feature Explorer 8989](https://apps.juniper.net/feature-explorer/feature/8989),
  [Apstra Integrated DCI](https://www.juniper.net/documentation/us/en/software/apstra6.0/apstra-user-guide/topics/topic-map/integrated-dci.html)
- Cisco: [VXLAN EVPN to MPLS SR handoff](https://www.cisco.com/c/en/us/support/docs/multiprotocol-label-switching-mpls/mpls/215785-implement-vxlan-evpn-to-mpls-sr-handoff.pdf)
- Nokia: [SR Linux 26.7, inter-domain VPN services](https://documentation.nokia.com/srlinux/26-7/books/vpn-services/inter-domain-vpn-services.html)
- H3C: [Segment Routing Configuration Guide](https://www.h3c.com/en/d_202502/2358802_294551_0.htm)
- SONiC / FRR: [SRv6 uSID DC fabric with SONiC](https://www.segment-routing.net/blogs/srv6-usid-on-sonic/),
  [FRR on segment-routing.net](https://www.segment-routing.net/open-software/frr/)
- Cilium / Isovalent: [Isovalent Enterprise 1.13](https://isovalent.com/blog/post/isovalent-enterprise-1-13/),
  [Isovalent Private Networks and Cisco Nexus One](https://isovalent.com/blog/post/isovalent-private-networks-and-cisco-nexus-one-bgp-evpn-integration-for-the-enterprise-data-center/),
  [Cisco solution overview](https://www.cisco.com/c/en/us/products/collateral/networking/cloud-networking/nx-os/isovalent-net-v12n-int-n-one-fabric-so.html),
  [cilium-dbg bpf srv6](https://docs.cilium.io/en/latest/cmdref/cilium-dbg_bpf_srv6/)
- OVN-Kubernetes: [OKEP-5088 EVPN](https://ovn-kubernetes.io/okeps/okep-5088-evpn/)
