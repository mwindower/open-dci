# Phase 0 findings: EVPN type-5 ↔ SRv6 L3VPN stitching

> Historical. The standalone-gateway lab (`lab/spike`) used here has been removed. It was
> superseded by the firewall lab in `lab/` ([Phase 0b](phase0b-findings.md)), which covers
> everything shown here. The findings, especially the RT behaviour, still apply.

Lab: `lab/spike/` (containerlab, FRR 10.6.0, host kernel 7.2). Result of `make lab-check LAB=spike` (originally a bash check script, now `lab/spike/e2e_test.go`):
**23/23 checks pass** from a clean deploy. This covers the control plane, the kernel
dataplane, IPv4/IPv6 reachability in both directions, and full-size 9000 B frames.

## Verdict: go

The single-VRF gateway design works with stock FRR and the Linux kernel. We need no custom
data plane and no split into an "EVPN half" and an "SRv6 half".

## What was proven

| # | Claim | Evidence |
|---|-------|----------|
| 1 | FRR exports routes **learned via EVPN type-5** into VPNv4/v6 with an SRv6 SID | `show bgp ipv4 vpn` on gw-a: `10.1.0.0/24 … sid=fd00:dc1:a:: … label=16` |
| 2 | FRR advertises routes **imported from VPN** as EVPN type-5 into the partition | leaf-a has `[5]:[0]:[24]:[10.2.0.0] RD 10.0.0.10:2 RT:65000:104001` |
| 3 | VNI translation needs no extra work | VNI 104001 (A) ↔ VNI 204001 (B). The VNI never leaves its partition |
| 4 | The WAN core needs no VRF, VPN or EVPN state | core runs IPv6 unicast only (loopbacks + locators) |
| 5 | Wire format is plain SRv6 | `IP6 2001:db8:a::1 > fd00:dc1:b:1::: RT6 (type=4, segleft=0) IP 10.1.0.10 > 10.2.0.10` |
| 6 | End.DT46 per VRF (one SID for v4 and v6) | `seg6local action End.DT46 vrftable 1001` |

## Gotchas the tool must handle

1. **`net.vrf.strict_mode=1` is mandatory for End.DT4/DT46, and its ordering matters.**
   The sysctl only exists after the `vrf` module is loaded, which happens when the first VRF
   is created. If it is not set:
   - FRR still allocates the SID, and `show segment-routing srv6 sid` looks fine.
   - Zebra rejects the kernel install (`B>r … seg6local End.DT46`).
   - All inbound SRv6 traffic is dropped silently.

   → The reconciler must create VRFs first, then set strict mode, and verify it (`pkg/kernel`
   precondition + health check).
2. **`seg6_enabled`** must be 1 on the interfaces that receive SRv6 traffic (`all` + `default`
   is the simplest).
3. **`ip link add NAME mtu N type …`**: `mtu` has to come before `type`. Otherwise it is
   parsed as a type-specific option and the command fails. This does not matter when using
   netlink directly.
4. **Missing `/etc/frr/vtysh.conf`** makes every `vtysh` call print a misleading
   `frr.conf processing failure: 11`. The image must ship `service integrated-vtysh-config`.
5. **Advertising the locator**: the lab uses `network <locator>` together with
   `no bgp network import-check`, because the locator prefix is not in the RIB. The tool should
   do this explicitly per locator and not rely on redistribution.

## MTU budget

| Segment | Overhead | Lab MTU |
|---|---|---|
| Host / tenant | – | 9000 |
| Partition fabric (VXLAN) | +50 B | 9500 underlay |
| WAN (SRv6, kernel `mode encap`) | **+48 B** (40 B IPv6 header + 8 B SRH; the kernel adds an SRH even for one segment) | 9500 |

Requirement: WAN MTU ≥ tenant MTU + 48. With H.Encaps.Red (kernel `mode encap.red`), the
single-segment SRH could be omitted, bringing it to +40 B. See the open items.

## Route-target behaviour (FRR 10.6, L3VNI, tested live in the lab)

| Config on the importer | Route arrives with | Imported? |
|---|---|---|
| auto (`65011:104001`) | `RT:65010:104001` (other ASN) | **yes**: auto import RTs fall back to matching the local part (VNI) only |
| explicit `65000:104001` | `RT:65010:104001` | **no**: explicit RTs need an exact match |
| explicit `*:104001` (shown as `0:104001`) | any `*:104001` | yes: explicit wildcard, ASN ignored |
| `autort rfc8365-compatible` | – | accepted, but **no effect** on L3VNI RTs (they stay `ASN:VNI`) |

Consequences:
- With auto RTs on both sides, EVPN RTs **need no configuration** even when every router has
  its own eBGP ASN, as in metal-stack. The gateway's partition-side config reduces to "local
  VNI", and RTs become an optional override.
- The gateway's **export** RT must still be importable by the leaves. If the leaves use
  explicit exact-match RTs, the gateway must export exactly those. Check this against
  metal-stack's SONiC leaves.
- Stripping the DCI RT on EVPN export becomes a **must**, not a cosmetic fix. Under
  VNI-only matching, a leaked `RT:65535:1001` would be imported by any partition VRF whose
  auto RT has local part 1001 (i.e. VNI 1001). This follows from the matching rule; it has
  not been tested yet. Phase 2 should add an e2e assertion for it.

## Open items for Phase 1/2

- **The DCI route target leaks into the partition.** Type-5 routes that were re-originated
  toward the leaves carry both `RT:65000:104001` (partition) and `RT:65535:1001` (DCI).
  Strip foreign RTs with an outbound route-map on the EVPN side, and also in the other
  direction.
- **Loop prevention with more than one gateway per partition.** In the lab, eBGP AS-path
  checks are enough (gw-a drops its own routes coming back). With redundant gateways that
  share an ASN, or with `allowas-in`, we need SoO extended communities per site.
- **SID stability.** `sid vpn per-vrf export auto` hands out function IDs in VRF creation
  order (`fd00:dc1:a:1::`). The tool should pin explicit values (`sid vpn per-vrf export <n>`)
  so SIDs survive restarts and reordering.
- **Reduced encapsulation.** Check whether FRR 10.6 can program `encap.red`, which saves 8 B.
- **Placement on the metal-stack firewall (Phase 0b).** Firewalls attach to leaves and are
  already EVPN VTEPs with the tenant VRFs, so peering with the leaves is a given. Still open:
  - The transit path is decided: a **dedicated DCI network** (EVPN VRF + L3VNI). SRv6 rides
    inside VXLAN from the firewall to the exits. Still open: SRv6 with its transport in a
    non-default VRF. FRR SIDs, kernel `seg6local` routes and the encap lookup all use the main
    table by default.
  - MTU with DCI: host 9000 → SRv6 9048 → VXLAN on the DCI VNI 9098. This fits the 9216 on
    the firewall; every fabric link on the DCI path must be ≥ 9098.
  - Coexistence with the FRR config that metal-stack generates.
  - The RT scheme in use (auto vs explicit `ASN:VNI`).
- **Redundancy and failover tests** (Phase 2): two gateways per partition, BFD on WAN sessions.

## Reference: the essential gateway config

This is the part of `lab/spike/configs/gw-a/frr.conf` that does the stitching:

```
vrf vrf-tenant-x
 vni 104001                              ! partition-local L3VNI
!
segment-routing
 srv6
  encapsulation
   source-address 2001:db8:a::1
  locators
   locator DCI
    prefix fd00:dc1:a::/48 block-len 32 node-len 16 func-bits 16
!
router bgp 65010
 segment-routing srv6
  locator DCI
!
router bgp 65010 vrf vrf-tenant-x
 sid vpn per-vrf export auto             ! End.DT46
 address-family ipv4 unicast             ! (same for ipv6 unicast)
  rd vpn export 10.0.0.10:1001
  rt vpn both 65535:1001                 ! global DCI RT for this tenant network
  export vpn                             ! EVPN-learned → VPN
  import vpn                             ! VPN → VRF → (advertise) → EVPN
 address-family l2vpn evpn
  advertise ipv4 unicast
  advertise ipv6 unicast
  route-target import 65000:104001       ! partition RTs
  route-target export 65000:104001
```
