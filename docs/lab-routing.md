# Routing tables in the lab

What each node of `lab/` knows once the lab has converged. Taken from a clean deploy
(`make lab-up`, all e2e tests passing); the gateways' tenant VRFs and DCI parts were added by
open-dci.

Each partition has a redundant gateway pair sharing locator, SIDs and ASN (anycast):
- **Pair A (gw-a1, gw-a2)** runs the SRv6 transport in the DCI network of its base config
  (`transport.vrf: vrf104100`). gw-a1 is shown in detail below; gw-a2 is the same with its
  own VTEP (10.0.0.17) and loopback (`fd00:dc1:ff::a2`).
- **Pair B (gw-b1, gw-b2)** runs it in the default VRF: exit-b carries the locator in the
  IPv6 underlay. See [Partition B](#partition-b-transport-in-the-default-vrf).

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
| exit-a main | – | – | ✓ |
| exit-a `vrf104100` (DCI) | – | ✓ | – |
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

**Main (underlay):** only VTEP loopbacks, all via `swp31` (spine-a): 10.0.0.13/14 (spine,
exit) and 10.0.0.16/17 (the gateways).

**EVPN type-5 routes (BGP).** The leaf also sees the DCI network's routes, but imports them
nowhere: it has no VRF for VNI 104100.

| Type-5 prefix | Originator (RD) | RTs |
|---|---|---|
| 10.0.16.10/32 | leaf-a (10.0.0.11) | `59915:3981` |
| 10.0.32.10/32 | gw-a1 (10.0.0.16) and gw-a2 (10.0.0.17) | `59920:3981` **+ `65535:1001`** (DCI RT leak, Phase 2) |
| 10.0.33.10/32 | gw-a1, gw-a2 | `59920:3982` + `65535:1002` |
| fd00:dc1:a::/48, own loopbacks | gw-a1, gw-a2 | `59920:104100` |
| fd00:dc1:b::/48, pair B's loopbacks, 2001:db8:c::1/128 | exit-a (10.0.0.14) | `59918:104100` |

Auto RTs with 4-byte ASNs use the low 16 bits of the ASN (4200000016 mod 65536 = 59920, the
same for both gateways of the pair). They differ per router and still match, because FRR
falls back to the VNI when importing.

## Spine `spine-a` and exit `exit-a`

**spine-a:** only VTEP loopbacks: 10.0.0.11 via `swp1`, 10.0.0.14/16/17 via `swp2`. It
passes all EVPN routes through unchanged, including the next hop, and imports none of them.

**exit-a main (underlay):** VTEP loopbacks only: 10.0.0.11/13 via `swp1`, gw-a1's 10.0.0.16
via `swp3`, gw-a2's 10.0.0.17 via `swp4`.

**exit-a DCI VRF `vrf104100`:** EVPN towards the partition, plain IPv6 towards the core. The
shared locator has both gateways as next hops:

```
fd00:dc1:a::/48     proto bgp                                     # pair A's anycast locator
        nexthop via ::ffff:10.0.0.16 dev vlan104100 onlink        #   gw-a1
        nexthop via ::ffff:10.0.0.17 dev vlan104100 onlink        #   gw-a2
fd00:dc1:ff::a1     via ::ffff:10.0.0.16 dev vlan104100 onlink    # gw-a1's loopback (VPN sessions)
fd00:dc1:ff::a2     via ::ffff:10.0.0.17 dev vlan104100 onlink    # gw-a2's loopback
fd00:dc1:b::/48     via fe80::… dev swp2 proto bgp                # pair B's locator (from core)
fd00:dc1:ff::b1     via fe80::… dev swp2 proto bgp                # pair B's loopbacks
fd00:dc1:ff::b2     via fe80::… dev swp2 proto bgp
2001:db8:c::1/128   via fe80::… dev swp2 proto bgp                # core loopback
unreachable default metric 4278198272
```

The exit's edge filter (`edgeFilter` in its `node.yaml`) keeps the block
`fd00:dc1::/32` unreachable from the fabric side and drops packets from the core into the
block with a source outside it (see
[Operation](operation.md#the-srv6-domain-and-its-edge)).

## Gateway `gw-a1`

The gateway has three tables that work together.

**Tenant VRF `vrf3981`** (provisioned: kernel table 3981, bridge `dcibr3981`, VXLAN device
`dcivx3981`): local via VXLAN, remote via SRv6 to pair B's anycast SID:

```
unreachable default metric 4278198272                                       # IPv4 and IPv6
10.0.16.10      via 10.0.0.11 dev dcibr3981 onlink                          # m-a (EVPN from leaf-a)
10.0.32.10      encap seg6 mode encap segs 1 [ fd00:dc1:b:fab:: ] via inet6 fe80::… dev dci0  # m-b
throw fd00:dc1::/32 metric 1024                                             # IPv6 only, see below
(IPv6 routes alike)
```

The kernel routes the outer packet of the SRv6 encapsulation in this table too. The
`throw` route sends lookups for the locator block, and only those, on to the main table.
Tenants' own packets to the block are dropped by the ingress filter before they get here.

**Main (default VRF)**: own SIDs, own loopback, and the path into the DCI VRF:

```
fd00:dc1:ff::a1     dev lo                                                     # own loopback: VPN sessions, encap source
fd00:dc1:a:f8d::    encap seg6local action End.DT46 vrftable 3981 dev vrf3981  # SID → tenant 1 (the same on gw-a2)
fd00:dc1:a:f8e::    encap seg6local action End.DT46 vrftable 3982 dev vrf3982  # SID → tenant 2
blackhole fd00:dc1:a::/48                                                      # rest of the locator
fd00:dc1::/32       via fe80::… dev dci0                                       # everything else in the block → DCI VRF (veth)
```

**DCI VRF `vrf104100`** (base config): locators and loopbacks only:

```
fd00:dc1:a::/48     via fe80::… dev dci1                                  # own locator → back to main (veth)
fd00:dc1:ff::a1     via fe80::… dev dci1                                  # own loopback → back to main
fd00:dc1:b::/48     via ::ffff:10.0.0.14 dev vlan104100 onlink            # pair B via exit-a (VXLAN VNI 104100)
fd00:dc1:ff::b1     via ::ffff:10.0.0.14 dev vlan104100 onlink
fd00:dc1:ff::b2     via ::ffff:10.0.0.14 dev vlan104100 onlink
2001:db8:c::1/128   via ::ffff:10.0.0.14 dev vlan104100 onlink            # core loopback (harmless)
unreachable default metric 4278198272
```

**BGP view of the prefix 10.0.32.10/32 on gw-a1**: two VPN paths, one per gateway of pair B,
both with the same anycast SID:

| RD | From | AS path | Notes |
|---|---|---|---|
| `10.0.1.16:1001` | gw-b1 (`fd00:dc1:ff::b1`) | 4200000026 4200000024 4200000023 4200000021 4200000025 | `RT:65535:1001`, SID `fd00:dc1:b::` + label → `fd00:dc1:b:fab::` |
| `10.0.1.17:1001` | gw-b2 (`fd00:dc1:ff::b2`) | same | same SID |

Whichever path is best, the kernel route is the same: encap to `fd00:dc1:b:fab::`. Which
gateway of pair B receives the packet is decided by the transport (exit-b's ECMP).

The opposite direction: `10.0.16.10/32` comes from leaf-a as type-5 (next hop 10.0.0.11),
lands in `vrf3981`, and is exported as VPNv4 with RD `10.0.0.16:1001` and SID
`fd00:dc1:a:f8d::`.

## Core

```
fd00:dc1:a::/48     via fe80::… dev swp1 proto bgp                # pair A, towards exit-a
fd00:dc1:ff::a1     via fe80::… dev swp1 proto bgp                # gw-a1, gw-a2 loopbacks
fd00:dc1:ff::a2     via fe80::… dev swp1 proto bgp
fd00:dc1:b::/48     via fe80::… dev swp2 proto bgp                # pair B, towards exit-b
fd00:dc1:ff::b1     via fe80::… dev swp2 proto bgp
fd00:dc1:ff::b2     via fe80::… dev swp2 proto bgp
```

The core carries one locator per partition, one loopback per gateway, and nothing else.

## Partition B: transport in the default VRF

Pair B's open-dci configs have no `transport.vrf`. open-dci announces the locator and the
gateway's loopback from the default BGP instance (`network fd00:dc1:b::/48` backed by a
blackhole route, `network fd00:dc1:ff::b1/128`), and exit-b carries them in the IPv6
underlay to the core. There is no veth, no DCI network and no VXLAN on the transport path.
The tenant side is the same as on gw-a1.

**gw-b1 main:**

```
fd00:dc1:a::/48     via fe80::… dev uplink0 proto bgp                          # pair A via exit-b
fd00:dc1:ff::a1     via fe80::… dev uplink0 proto bgp                          # pair A's loopbacks
fd00:dc1:ff::a2     via fe80::… dev uplink0 proto bgp
fd00:dc1:ff::b1     dev lo                                                     # own loopback
fd00:dc1:b:fab::    encap seg6local action End.DT46 vrftable 4011 dev vrf4011  # SID → tenant 1
fd00:dc1:b:fac::    encap seg6local action End.DT46 vrftable 4012 dev vrf4012  # SID → tenant 2
blackhole fd00:dc1:b::/48                                                      # announced via "network"
```

**exit-b main:** the shared locator via both gateways (ECMP), the rest via the core:

```
fd00:dc1:b::/48     proto bgp
        nexthop via fe80::… dev swp3 weight 1                   # gw-b1
        nexthop via fe80::… dev swp4 weight 1                   # gw-b2
fd00:dc1:ff::b1     via fe80::… dev swp3 proto bgp
fd00:dc1:ff::b2     via fe80::… dev swp4 proto bgp
fd00:dc1:a::/48     via fe80::… dev swp2 proto bgp              # pair A, via the core
```

The leaves and spines of partition B don't carry the locators (no IPv6 underlay towards
them). If they did, the exit's edge filter would still keep them out of the block.

exit-a's DCI VRF and the core simply see `fd00:dc1:b::/48` coming from exit-b. Both modes
interoperate without either side knowing the other's mode.

## Reproduce

```sh
make lab-up
docker exec clab-open-dci-m-a    ip route
docker exec clab-open-dci-leaf-a ip route show vrf vrf3981
docker exec clab-open-dci-gw-a1  ip route show vrf vrf3981
docker exec clab-open-dci-gw-a1  ip -6 route                        # main
docker exec clab-open-dci-gw-a1  ip -6 route show vrf vrf104100     # DCI
docker exec clab-open-dci-exit-a ip -6 route show vrf vrf104100
docker exec clab-open-dci-core   ip -6 route
docker exec clab-open-dci-gw-a1  vtysh -c 'show bgp ipv4 vpn'
docker exec clab-open-dci-leaf-a vtysh -c 'show bgp l2vpn evpn route type prefix'
```
