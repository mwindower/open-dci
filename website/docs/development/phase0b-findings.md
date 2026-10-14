# Phase 0b findings: gateway on the metal-stack firewall, transit via a DCI network

> Historical. The gateway on the tenant's metal-stack firewall was implemented in Phase 1
> and later **removed**: open-dci now runs only on dedicated gateways at the exits, which
> provision the tenant VRFs themselves (trust domain, lifecycle; see the README's "Why this
> design"). What still applies: the DCI network transport (veth, `ip rule`, MTU) described
> here, which dedicated gateways use with `transport.vrf`. One change against this
> document: the veth ends use fixed link-local addresses (`fe80::1` in main, `fe80::2` in
> the DCI VRF) instead of `fd00:dc2::/64`, so a `redistribute connected` in the DCI VRF
> doesn't announce the same transfer network from every gateway.

Lab: `lab/firewall/` (11 nodes, FRR 10.6.0). `make lab-redeploy LAB=firewall` passes all
34 e2e checks from a clean deploy: control plane, kernel dataplane, v4/v6 in both
directions, and full-size 9000 B machine packets.

## Verdict: go, with three kernel/FRR details the tool must get right

A metal-stack firewall can act as the DCI gateway. SRv6 is carried through the partition's
own EVPN fabric in a dedicated DCI network, and exits and core stay free of any tenant state.

## Topology

```
 m-a ─ leaf-a ─ spine-a ─ exit-a ─┐              ┌─ exit-b ─ spine-b ─ leaf-b ─ m-b
         │                         └──── core ────┘                       │
        fw-a                                                             fw-b
  tenant vrf3981 (VNI 3981)                                   tenant vrf4011 (VNI 4011)
  DCI    vrf104100 (VNI 104100)                                DCI    vrf204100 (VNI 204100)
```

- Machines announce their IPs (on `lo`) via BGP to the leaf inside the tenant VRF, as in
  metal-stack.
- Leaf and firewall configs are modelled on metal-core's `sonic_frr.tpl` and
  metal-networker's `frr.firewall.tpl`: peer-groups, auto RTs, `vrf<VNI>` naming, and one
  VLAN-aware bridge with `vni<VNI>`/`vlan<VNI>` devices.
- The tenant VNIs (3981/4011) and DCI VNIs (104100/204100) differ per partition. Only the
  DCI RT (`65535:1001`) is shared.

On the fabric wire (spine-a), a machine packet looks like this:

```
IP 10.0.0.12 > 10.0.0.14.4789: VXLAN vni 104100                    ← fw-a VTEP → exit-a VTEP (DCI network)
  IP6 fd00:dc1:a::1 > fd00:dc1:b:1::: RT6 (type=4, segleft=0)       ← SRv6 to fw-b's End.DT46 SID
    IP 10.0.16.10 > 10.0.32.10: ICMP echo request                   ← tenant packet, m-a → m-b
```

## How the firewall attaches the SRv6 transport to the DCI VRF

FRR runs SRv6 L3VPN only in the default BGP instance: VPN sessions, SID allocation and
next-hop tracking all happen there. The kernel always does the outer SRv6 lookup in the
main table. The transport, however, lives in the DCI VRF. The working design:

```
                     main (default VRF)                              vrf104100 (DCI)
  lo  fd00:dc1:a::1  (gateway loopback, inside own locator)
  fd00:dc1:a:1::  seg6local End.DT46 → vrf3981
  fd00:dc1::/32   via fd00:dc2::2 ──── dci0 ═══ veth ═══ dci1 ────  fd00:dc1:a::/48 via fd00:dc2::1
  fd00:dc1:a::/48 blackhole                                          fd00:dc1:b::/48 via exit-a (EVPN t5)
                                                                     └─ redistribute static → EVPN t5 → exit → core
```

1. **The gateway loopback lives inside its own locator** (`fd00:dc1:a::1`). Function 0 is
   never allocated as a SID. So one prefix per gateway (`fd00:dc1:a::/48`) covers both the
   VPN session endpoint and the SIDs. Exits and core only carry these locators.
2. **A veth pair connects main and the DCI VRF.** FRR static routes on both sides point the
   locator block across it.
3. **The local rule moves behind the l3mdev rule** (`ip rule` priority 0 → 32765).
4. **A blackhole for the own locator** in main stops unused locator addresses from bouncing
   between main and the DCI VRF.

### Alternatives that failed (and why)

| Attempt | Result |
|---|---|
| FRR `import vrf vrf104100` into the default instance | The routes leak but stay **invalid**. Their next hop is the exit's VTEP (EVPN), and FRR resolves it inside the DCI VRF (`@4`), where underlay addresses don't exist. |
| Kernel VRF-device leak (`ipv6 route fd00:dc1::/32 vrf104100 nexthop-vrf vrf104100`) + `ip rule` for inbound + `tcp_l3mdev_accept=1` | Ping between gateway loopbacks works, **but the VPN BGP session never comes up**. The TCP handshake completes, then the receiving bgpd sends FIN at once: FRR assigns an accepted connection to the BGP instance of the VRF it arrived in (the DCI VRF), where the VPN neighbor cannot exist. |
| veth, but local rule at priority 0 | SYNs arrive in the DCI VRF, and the kernel answers **inside the VRF** (the loopback matches the local table before any VRF routing). They never cross the veth, so the session stays in `Connect`. |

## MTU: metal-stack's defaults break full-size packets, silently

metal-networker pins `bridge`, all `vni*` and all `vlan*` devices to **9000**; only the
uplinks are 9216. Packet sizes on the DCI path:

| Hop | Size |
|---|---|
| machine | 9000 |
| after SRv6 encap on the firewall | 9048 |
| in the DCI VNI on the fabric | 9098 |

Tested in the lab with the firewall's DCI devices (bridge, vni, vlan, veth) at 9000:
- A full-size IPv6 ping fails (100 % loss).
- The sender **receives no ICMPv6 Packet Too Big**: the packet is black-holed.
- At 9166 (9216 − 50) everything passes.

Requirements for the tool / metal-stack:
- On the firewall, the DCI network's `vni`/`vlan` devices, the veth pair, and the shared
  `bridge` must be ≥ 9048. The lab uses 9166. The tenant VNIs can stay at 9000.
- The exit's DCI devices must be ≥ 9048. SONiC's VLAN interface default of 9100 fits.
- Every fabric link on the firewall → exit path must be ≥ 9098.
- The core links between partitions must be ≥ 9048.
- The tool should check this, because a misconfiguration is a black hole with no error.

## metal-stack integration points found

1. **Leaf VNI filter.** metal-core renders `route-map fw-<port>-vni out` with one
   `match evpn vni` per network the firewall is attached to. The DCI network's VNI must be
   in that list. That is natural if the DCI network is modelled as a metal-stack network
   (like the internet network) attached to the firewall.
2. **Firewall FRR template.** The additions are all marked `! DCI:` in
   `internal/frr/testdata/fw-a.golden` (rendered by open-dci; in Phase 0b hand-written in the firewall frr.conf):
   - the SRv6 locator
   - the VPN neighbor
   - `sid vpn per-vrf export` plus `rd/rt vpn` and `import/export vpn` in the tenant VRF
   - `redistribute static` in the DCI VRF
   - two static routes and a blackhole
3. **Firewall kernel setup** (metal-networker / systemd-networkd):
   - the veth pair
   - the local-rule reordering
   - MTU ≥ 9048 on the DCI devices and the bridge
4. **Auto RTs work unchanged.** Neither leaf, firewall nor exit configures a single RT on
   the EVPN side.

## Open items

- nftables on the firewall (firewall-controller) must let SRv6 through the DCI VRF / veth.
  The lab has no nftables yet.
- The DCI RT leak into the partition (see the Phase 0 findings) still exists and must be
  stripped (Phase 2).
- Redundancy: metal-stack firewalls attach to two leaves (`lan0`/`lan1`), and there may be
  firewall pairs. Each needs its own locator; ECMP over VPN paths.
- Loop prevention as soon as more than two partitions or redundant gateways are involved:
  SoO.
- Whether the sender-side black hole on MTU errors is a kernel seg6 limitation (no PTB
  generated for the inner source) or specific to this path is not investigated. Either way
  the tool must prevent the misconfiguration.
