# Routing tables in the lab

What each node of `lab/` knows once the lab has converged. Taken from a clean deploy
(`make lab-up`, all e2e tests passing); the gateways' tenant VRFs and DCI parts were added by
open-dci.

Each partition has two exits and a redundant gateway pair sharing locator, SIDs and ASN
(anycast). Every gateway is attached to both exits of its partition (`uplink0` → exit-x1,
`uplink1` → exit-x2), so almost every path below exists twice (ECMP):
- **Pair A (gw-a1, gw-a2)** runs the SRv6 transport in the DCI network of its base config
  (`transport.vrf: vrf104100`). gw-a1 is shown in detail below; gw-a2 is the same with its
  own VTEP (10.0.0.17) and loopback (`fd00:dc1:ff::a2`).
- **Pair B (gw-b1, gw-b2)** runs it in the default VRF: exit-b1/b2 carry the locator in
  the IPv6 underlay. See [Partition B](#partition-b-transport-in-the-default-vrf).

Both pairs provision two tenants: tenant 1 (m-a 10.0.16.10 ↔ m-b 10.0.32.10, VNIs
3981/4011, RT `65535:1001`) and tenant 2 (m-a2 10.0.17.10 ↔ m-b2 10.0.33.10, VNIs
3982/4012, RT `65535:1002`). Tenant 1 is shown; tenant 2 is the same with its own VRF, VNI
and SID. The SIDs are pinned to the VNI: `fd00:dc1:a:f8d::` (3981), `fd00:dc1:a:f8e::`
(3982), `fd00:dc1:b:fab::` (4011), `fd00:dc1:b:fac::` (4012).

Link-local addresses are shown as `fe80::…`. Link-local routes, multicast, the management
network and the kernel's local routes are omitted.

## Overview

| Node / table | Tenant prefixes | DCI prefixes (locators, loopbacks) | Underlay (VTEP loopbacks) |
|---|---|---|---|
| m-a | ✓ (own + remote, via leaf) | – | – |
| leaf-a `vrf3981` | ✓ | – | – |
| leaf-a main | – | – | ✓ |
| spine-a | – | – | ✓ |
| exit-a1/a2 main | – | exit loopbacks (VPN relay) | ✓ |
| exit-a1/a2 `vrf104100` (DCI) | – | ✓ | – |
| gw-a1 `vrf3981` (tenant, provisioned) | ✓ (remote ones via SRv6) | throw to main (encap only) | – |
| gw-a1 main | – | own SIDs + everything else via veth | ✓ |
| gw-a1 `vrf104100` (DCI) | – | ✓ | – |
| core | – | ✓ (locators and loopbacks only) | – |

In short:
- **Tenant prefixes exist only in tenant VRFs** (machine, leaf, gateway).
- **The DCI VRF only carries locators and gateway loopbacks**. The e2e test
  `no-tenant-state/*` asserts this for exits and core.
- **Spines, exits and core** never know a tenant. The exits only pass the EVPN routes
  between leaves and gateways.
- **Every VRF ends in `unreachable default`**: nothing falls through to the main table.

## Machine `m-a`

The machine learns everything from its leaf via BGP. The remote machine is a plain /32 like
any local one. The source address is set by `ip protocol bgp route-map RM_SET_SRC`, as in
metal-stack.

```
10.0.32.10      via inet6 fe80::… dev lan0 proto bgp src 10.0.16.10     # m-b (other partition)
2001:db8:32::10 via fe80::… dev lan0 proto bgp
```

## Leaf `leaf-a`

**Tenant VRF `vrf3981`.** The local machine is behind `swp1`. Everything remote points to
both gateways of the pair (ECMP), carried over VXLAN VNI 3981 through spine and exit:

```
unreachable default metric 4278198272                                    # no fall-through to main
10.0.16.10      via inet6 fe80::… dev swp1 proto bgp                     # m-a (BGP, unnumbered)
10.0.32.10      proto bgp                                                # m-b ← re-originated by the pair
        nexthop via 10.0.0.16 dev vlan3981 weight 1 onlink               #   gw-a1
        nexthop via 10.0.0.17 dev vlan3981 weight 1 onlink               #   gw-a2
(IPv6 alike, next hops ::ffff:10.0.0.16/17)
```

**Main (underlay):** only VTEP loopbacks, all via `swp31` (spine-a): 10.0.0.13 (spine),
10.0.0.14/18 (the exits) and 10.0.0.16/17 (the gateways).

**EVPN type-5 routes (BGP).** The leaf also sees the DCI network's routes, but imports them
nowhere: it has no VRF for VNI 104100.

| Type-5 prefix | Originator (RD) | RTs |
|---|---|---|
| 10.0.16.10/32 | leaf-a (10.0.0.11) | `59915:3981` |
| 10.0.32.10/32 | gw-a1 (10.0.0.16) and gw-a2 (10.0.0.17) | `59920:3981` **+ `65535:1001`** (DCI RT leak, Phase 2) |
| 10.0.33.10/32 | gw-a1, gw-a2 | `59920:3982` + `65535:1002` |
| fd00:dc1:a::/48, own loopbacks | gw-a1, gw-a2 | `59920:104100` |
| fd00:dc1:b::/48, pair B's loopbacks, 2001:db8:c::1/128 | exit-a1 (10.0.0.14) and exit-a2 (10.0.0.18) | `59918:104100`, `59922:104100` |

Auto RTs with 4-byte ASNs use the low 16 bits of the ASN (4200000016 mod 65536 = 59920, the
same for both gateways of the pair). They differ per router and still match, because FRR
falls back to the VNI when importing.

## Spine `spine-a` and exits `exit-a1`/`exit-a2`

**spine-a:** only VTEP loopbacks: 10.0.0.11 via `swp1`, the exits via `swp2` (exit-a1) and
`swp3` (exit-a2), the gateways via both (ECMP). It passes all EVPN routes through
unchanged, including the next hop, and imports none of them.

**exit-a1 main (underlay):** VTEP loopbacks only: 10.0.0.11/13 and exit-a2's 10.0.0.18 via
`swp1` (spine-a), gw-a1's 10.0.0.16 via `swp3`, gw-a2's 10.0.0.17 via `swp4`. The gateways
are dual-attached but never transit between the exits: their base config announces only
their own prefixes (`only-self-out`, e2e test `TestGatewaysAreNotTransit`).

**exit-a1 DCI VRF `vrf104100`:** EVPN towards the partition, plain IPv6 towards the core. The
shared locator has both gateways as next hops (exit-a2 is the same):

```
fd00:dc1:a::/48     proto bgp                                     # pair A's anycast locator
        nexthop via ::ffff:10.0.0.16 dev vlan104100 onlink        #   gw-a1
        nexthop via ::ffff:10.0.0.17 dev vlan104100 onlink        #   gw-a2
fd00:dc1:ff::a1     via ::ffff:10.0.0.16 dev vlan104100 onlink    # gw-a1's loopback (encap source)
fd00:dc1:ff::a2     via ::ffff:10.0.0.17 dev vlan104100 onlink    # gw-a2's loopback
fd00:dc1:b::/48     via fe80::… dev swp2 proto bgp                # pair B's locator (from core)
fd00:dc1:ff::b1     via fe80::… dev swp2 proto bgp                # pair B's loopbacks
fd00:dc1:ff::b2     via fe80::… dev swp2 proto bgp
2001:db8:c::1/128   via fe80::… dev swp2 proto bgp                # core loopback
unreachable default metric 4278198272
(plus the exit loopbacks 2001:db8:e::*, which the core passes into the DCI VRF; harmless)
```

The exits' edge filter (`edgeFilter` in their `node.yaml`) keeps the block
`fd00:dc1::/32` unreachable from the fabric side and from the default-VRF core link
(`swp5`), and drops packets from the core into the block with a source outside it (see
[Operation](operation.md#the-srv6-domain-and-its-edge)).

## Gateway `gw-a1`

The gateway has three tables that work together.

**Tenant VRF `vrf3981`** (provisioned: kernel table 3981, bridge `dcibr3981`, VXLAN device
`dcivx3981`): local via VXLAN, remote via SRv6 to pair B's anycast SID:

```
unreachable default metric 4278198272                                       # IPv4 and IPv6
10.0.16.10      via 10.0.0.11 dev dcibr3981 onlink                          # m-a (EVPN from leaf-a)
10.0.32.10      encap seg6 mode encap segs 1 [ fd00:dc1:b:fab:: ] via inet6 fe80::… dev uplink0  # m-b
throw fd00:dc1::/32 metric 1024                                             # IPv6 only, see below
(IPv6 routes alike)
```

The kernel routes the outer packet of the SRv6 encapsulation in this table too. The
`throw` route sends lookups for the locator block, and only those, on to the main table.
The encap route's next hop (the exit that relayed the VPN route) doesn't decide the path:
the outer packet is routed by its destination SID, here into the DCI VRF via the veth.
Tenants' own packets to the block are dropped by the ingress filter before they get here.

**Main (default VRF)**: own SIDs, own loopback, and the path into the DCI VRF:

```
fd00:dc1:ff::a1     dev lo                                                     # own loopback: encap source
fd00:dc1:a:f8d::    encap seg6local action End.DT46 vrftable 3981 dev vrf3981  # SID → tenant 1 (the same on gw-a2)
fd00:dc1:a:f8e::    encap seg6local action End.DT46 vrftable 3982 dev vrf3982  # SID → tenant 2
blackhole fd00:dc1:a::/48                                                      # rest of the locator
fd00:dc1::/32       via fe80::… dev dci0                                       # everything else in the block → DCI VRF (veth)
```

**DCI VRF `vrf104100`** (base config): locators and loopbacks only, via both exits:

```
fd00:dc1:a::/48     via fe80::… dev dci1                                  # own locator → back to main (veth)
fd00:dc1:ff::a1     via fe80::… dev dci1                                  # own loopback → back to main
fd00:dc1:b::/48     proto bgp                                             # pair B (VXLAN VNI 104100)
        nexthop via ::ffff:10.0.0.14 dev vlan104100 onlink                #   via exit-a1
        nexthop via ::ffff:10.0.0.18 dev vlan104100 onlink                #   via exit-a2
fd00:dc1:ff::b1, fd00:dc1:ff::b2, 2001:db8:c::1/128                       # alike, via both exits
unreachable default metric 4278198272
```

**BGP view of the prefix 10.0.32.10/32 on gw-a1**: two VPN paths, one per gateway of pair B
(RD), both with the same anycast SID. Each arrives twice, once over each of gw-a1's VPN
sessions, to exit-a1 (`uplink0`) and exit-a2 (`uplink1`). E.g. gw-b1's path, as relayed by
exit-b1, then by exit-a1 or exit-a2:

| RD | Originated by | Via | AS path | Notes |
|---|---|---|---|---|
| `10.0.1.16:1001` | gw-b1 | exit-a1 | 4200000014 4200000024 4200000026 4200000024 4200000023 4200000021 4200000025 | `RT:65535:1001`, SID `fd00:dc1:b::` + label → `fd00:dc1:b:fab::` |
| `10.0.1.16:1001` | gw-b1 | exit-a2 | 4200000018 4200000024 4200000026 … | same SID |
| `10.0.1.17:1001` | gw-b2 | both | alike | same SID |

The path starts with the relaying exits and then contains exit-b1 once more: gw-b1
exported a route it learned via EVPN through exit-b1. That is why the exits accept their
gateways' VPN routes with `allowas-in 1`.

Whichever path is best, the kernel route is the same: encap to `fd00:dc1:b:fab::`. Which
gateway of pair B receives the packet is decided by the transport (ECMP in the core and in
exit-b1/b2), per flow: open-dci sets `net.ipv6.seg6_flowlabel=1`, so the outer flow label
differs per tenant flow.

The opposite direction: `10.0.16.10/32` comes from leaf-a as type-5 (next hop 10.0.0.11),
lands in `vrf3981`, and is exported as VPNv4 with RD `10.0.0.16:1001` and SID
`fd00:dc1:a:f8d::`.

## VPN relay between the exits

Gateways don't peer with each other. Each one runs VPNv4/v6 only on its existing sessions to
its two exits (`peers[].interface: uplink0`, `uplink1`). The exits relay the VPN routes
between the partitions without importing them:

- Between the exits: a ladder of eBGP multihop sessions between their loopbacks
  `2001:db8:e::a1`, `::a2`, `::b1`, `::b2`, VPN address families only. Each exit peers with
  both exits of the neighbouring partition (with two partitions: the other one), but not
  with its own partner: exit-a1 ↔ exit-b1, exit-b2 and exit-a2 ↔ exit-b1, exit-b2
  (e2e `TestExitLadder`). With more partitions, they would form a ring of such pairs. The VPN families
  only exist in FRR's default instance, and exit-a1/a2's core links (`swp2`) are in the DCI
  VRF, so each of them has a second, default-VRF link to the core (`swp5`) that carries the
  exit loopbacks.
- Exit ↔ its gateways (`swp3`, `swp4`): VPN on the fabric session, `allowas-in 1` (see
  above).
- `show bgp ipv4 vpn` on an exit lists every gateway's routes, but `show ip route vrf all`
  holds none of them: the exits have no tenant VRFs.

## Core

```
fd00:dc1:a::/48     proto bgp                                     # pair A, via exit-a1 and exit-a2
        nexthop via fe80::… dev swp1                              #   exit-a1 (its DCI VRF)
        nexthop via fe80::… dev swp4                              #   exit-a2
fd00:dc1:b::/48     proto bgp                                     # pair B, via exit-b1 and exit-b2
        nexthop via fe80::… dev swp2
        nexthop via fe80::… dev swp6
fd00:dc1:ff::a1, ::a2, ::b1, ::b2                                 # gateway loopbacks, alike via both exits
2001:db8:e::a1      via fe80::… dev swp3 proto bgp                # exit loopbacks (VPN relay sessions)
2001:db8:e::a2      via fe80::… dev swp5 proto bgp
2001:db8:e::b1      via fe80::… dev swp2 proto bgp
2001:db8:e::b2      via fe80::… dev swp6 proto bgp
```

The core carries one locator per partition, one loopback per gateway and per exit, and
nothing else.

## Partition B: transport in the default VRF

Pair B's open-dci configs have no `transport.vrf`. open-dci announces the locator and the
gateway's loopback from the default BGP instance (`network fd00:dc1:b::/48` backed by a
blackhole route, `network fd00:dc1:ff::b1/128`) to both exits, and exit-b1/b2 carry them
in the IPv6 underlay to the core. There is no veth, no DCI network and no VXLAN on the transport path.
The tenant side is the same as on gw-a1.

**gw-b1 main:**

```
fd00:dc1:a::/48     proto bgp                                                  # pair A, via both exits
        nexthop via fe80::… dev uplink0                                        #   exit-b1
        nexthop via fe80::… dev uplink1                                        #   exit-b2
fd00:dc1:ff::a1, fd00:dc1:ff::a2                                               # pair A's loopbacks, alike
fd00:dc1:ff::b1     dev lo                                                     # own loopback
fd00:dc1:b:fab::    encap seg6local action End.DT46 vrftable 4011 dev vrf4011  # SID → tenant 1
fd00:dc1:b:fac::    encap seg6local action End.DT46 vrftable 4012 dev vrf4012  # SID → tenant 2
blackhole fd00:dc1:b::/48                                                      # announced via "network"
```

**exit-b1 main** (exit-b2 alike): the shared locator via both gateways (ECMP), the rest via the core:

```
fd00:dc1:b::/48     proto bgp
        nexthop via fe80::… dev swp3 weight 1                   # gw-b1
        nexthop via fe80::… dev swp4 weight 1                   # gw-b2
fd00:dc1:ff::b1     via fe80::… dev swp3 proto bgp
fd00:dc1:ff::b2     via fe80::… dev swp4 proto bgp
fd00:dc1:a::/48     via fe80::… dev swp2 proto bgp              # pair A, via the core
```

The leaves and spines of partition B don't carry the locators: the exits announce IPv6 only
to their gateways and the core, never into the fabric (e2e `TestFabricHasNoTransportRoutes`).
If a fabric device sent something into the block anyway, the exit's edge filter would drop
it.

exit-a1/a2's DCI VRF and the core simply see `fd00:dc1:b::/48` coming from exit-b1/b2. Both modes
interoperate without either side knowing the other's mode.

## Reproduce

```sh
make lab-up
docker exec clab-open-dci-m-a    ip route
docker exec clab-open-dci-leaf-a ip route show vrf vrf3981
docker exec clab-open-dci-gw-a1  ip route show vrf vrf3981
docker exec clab-open-dci-gw-a1  ip -6 route                        # main
docker exec clab-open-dci-gw-a1  ip -6 route show vrf vrf104100     # DCI
docker exec clab-open-dci-exit-a1 ip -6 route show vrf vrf104100
docker exec clab-open-dci-core   ip -6 route
docker exec clab-open-dci-gw-a1  vtysh -c 'show bgp ipv4 vpn'
docker exec clab-open-dci-leaf-a vtysh -c 'show bgp l2vpn evpn route type prefix'
```
