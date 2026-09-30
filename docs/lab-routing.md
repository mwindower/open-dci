# Routing tables in the lab

What each node of `lab/` knows once the lab has converged. Taken from a clean deploy
(`make lab-up`, all e2e tests passing); the gateways' tenant VRFs and DCI parts were added by
open-dci.

The two gateways use the two transport modes:
- **gw-a (partition A)** runs the SRv6 transport in the DCI network of its base config
  (`transport.vrf: vrf104100`). It is shown in detail below.
- **gw-b (partition B)** runs it in the default VRF: exit-b carries its locator in the IPv6
  underlay. See [Partition B](#partition-b-transport-in-the-default-vrf).

Both gateways provision two tenants: tenant 1 (m-a 10.0.16.10 ↔ m-b 10.0.32.10, VNIs
3981/4011, RT `65535:1001`) and tenant 2 (m-a2 10.0.17.10 ↔ m-b2 10.0.33.10, VNIs
3982/4012, RT `65535:1002`). Tenant 1 is shown; tenant 2 is the same with its own VRF, VNI
and SID (`fd00:dc1:a:2::` / `fd00:dc1:b:2::`).

Link-local, multicast, the management network and the kernel's local routes are omitted.

## Overview

| Node / table | Tenant prefixes | DCI prefixes (locators) | Underlay (VTEP loopbacks) |
|---|---|---|---|
| m-a | ✓ (own + remote, via leaf) | – | – |
| leaf-a `vrf3981` | ✓ | – | – |
| leaf-a main | – | – | ✓ |
| spine-a | – | – | ✓ |
| exit-a main | – | – | ✓ |
| exit-a `vrf104100` (DCI) | – | ✓ | – |
| gw-a `vrf3981` (tenant, provisioned) | ✓ (remote ones via SRv6) | – | – |
| gw-a main | – | own SIDs + everything else via veth | ✓ |
| gw-a `vrf104100` (DCI) | – | ✓ | – |
| core | – | ✓ (locators only) | – |

In short:
- **Tenant prefixes exist only in tenant VRFs** (machine, leaf, gateway).
- **The DCI VRF only carries locators**: one /48 per gateway, which also contains the
  gateway's loopback. The e2e test `no-tenant-state/*` asserts this for exits and core.
- **Spines, exits and core** never know a tenant. The exits only pass the EVPN routes
  between leaves and gateway.

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
the gateway's VTEP (10.0.0.16), carried over VXLAN VNI 3981 through spine and exit:

```
10.0.16.10      via inet6 fe80::… dev swp1 proto bgp                     # m-a (BGP, unnumbered)
10.0.32.10      via 10.0.0.16 dev vlan3981 proto bgp onlink               # m-b ← re-originated by gw-a
(IPv6 alike, next hop ::ffff:10.0.0.16)
```

**Main (underlay):** only VTEP loopbacks, all via `swp31` (spine-a): 10.0.0.13/14 (spine,
exit) and 10.0.0.16 (gw-a).

**EVPN type-5 routes (BGP).** The leaf also sees the DCI network's routes, but imports them
nowhere: it has no VRF for VNI 104100.

| Type-5 prefix | Originator (RD) | RTs |
|---|---|---|
| 10.0.16.10/32 | leaf-a (10.0.0.11) | `59915:3981` |
| 10.0.32.10/32 | gw-a (10.0.0.16) | `59920:3981` **+ `65535:1001`** (DCI RT leak, Phase 2) |
| 10.0.33.10/32 | gw-a (10.0.0.16) | `59920:3982` + `65535:1002` |
| fd00:dc1:a::/48 | gw-a (10.0.0.16) | `59920:104100` |
| fd00:dc1:b::/48, 2001:db8:c::1/128 | exit-a (10.0.0.14) | `59918:104100` |

Auto RTs with 4-byte ASNs use the low 16 bits of the ASN (4200000011 mod 65536 = 59915).
They differ per router and still match, because FRR falls back to the VNI when importing.

## Spine `spine-a` and exit `exit-a`

**spine-a:** only VTEP loopbacks: 10.0.0.11 via `swp1`, 10.0.0.14 and 10.0.0.16 via `swp2`.
It passes all EVPN routes through unchanged, including the next hop, and imports none of
them.

**exit-a main (underlay):** VTEP loopbacks only (10.0.0.11/13 via `swp1`, gw-a's 10.0.0.16
via `swp3`).

**exit-a DCI VRF `vrf104100`:** EVPN towards the partition, plain IPv6 towards the core:

```
fd00:dc1:a::/48     via ::ffff:10.0.0.16 dev vlan104100 onlink    # gw-a's locator (type-5 from gw-a)
fd00:dc1:b::/48     via fe80::… dev swp2 proto bgp                # gw-b's locator (from core)
2001:db8:c::1/128   via fe80::… dev swp2 proto bgp                # core loopback
```

## Gateway `gw-a`

The gateway has three tables that work together.

**Tenant VRF `vrf3981`** (provisioned: kernel table 3981, bridge `dcibr3981`, VXLAN device
`dcivx3981`): local via VXLAN, remote via SRv6:

```
10.0.16.10      via 10.0.0.11 dev dcibr3981 onlink                                         # m-a (EVPN from leaf-a)
10.0.32.10      encap seg6 mode encap segs 1 [ fd00:dc1:b:1:: ] via inet6 fe80::2 dev dci0  # m-b (VPN from gw-b)
(IPv6 alike)
```

**Main (default VRF)**: underlay, own SIDs, and the path into the DCI VRF:

```
fd00:dc1:a::1       dev lo                                                     # own loopback = VPN session endpoint, encap source
fd00:dc1:a:1::      encap seg6local action End.DT46 vrftable 3981 dev vrf3981  # SID → tenant 1
fd00:dc1:a:2::      encap seg6local action End.DT46 vrftable 3982 dev vrf3982  # SID → tenant 2
blackhole fd00:dc1:a::/48                                                      # rest of own locator
fd00:dc1::/32       via fe80::2 dev dci0                                       # all other locators → DCI VRF (veth)
```

**DCI VRF `vrf104100`** (base config): locators only:

```
fd00:dc1:a::/48     via fe80::1 dev dci1                                       # own locator → back to main (veth)
fd00:dc1:b::/48     via ::ffff:10.0.0.14 dev vlan104100 onlink                 # remote locator via exit-a (VXLAN VNI 104100)
2001:db8:c::1/128   via ::ffff:10.0.0.14 dev vlan104100 onlink                 # core loopback (harmless)
```

**BGP view of the prefix 10.0.32.10/32 on gw-a**, as it passes through three tables:

| Table | Next hop | AS path | Notes |
|---|---|---|---|
| VPNv4, RD `10.0.1.16:1001` | `fd00:dc1:b::1` (gw-b) | 4200000026 4200000024 4200000023 4200000021 4200000025 | `RT:65535:1001`, SID `fd00:dc1:b::` + label 16 → `fd00:dc1:b:1::` |
| VRF `vrf3981` | `fd00:dc1:b::1` (resolved in main) | same | imported via RT `65535:1001` |
| EVPN type-5 towards the fabric | 10.0.0.16 (own VTEP), RMAC of `dcibr3981` | 4200000016 … | RT `59920:3981` (+ leaked `65535:1001`) |

The opposite direction: `10.0.16.10/32` comes from leaf-a as type-5 (next hop 10.0.0.11),
lands in `vrf3981`, and is exported as VPNv4 with RD `10.0.0.16:1001` and SID
`fd00:dc1:a:1::`.

## Core

```
fd00:dc1:a::/48     via fe80::… dev swp1 proto bgp                # gw-a, towards exit-a
fd00:dc1:b::/48     via fe80::… dev swp2 proto bgp                # gw-b, towards exit-b
```

The core carries one prefix per gateway and nothing else.

## Partition B: transport in the default VRF

gw-b's open-dci config has no `transport.vrf`. open-dci announces the locator from the
default BGP instance (`network fd00:dc1:b::/48`, backed by a blackhole route), and exit-b
carries it in the IPv6 underlay to the core. There is no veth, no DCI network and no VXLAN
on the transport path. The tenant side is the same as on gw-a.

**gw-b main:**

```
fd00:dc1:a::/48   via fe80::… dev uplink0 proto bgp                          # remote locator via exit-b
fd00:dc1:b::1     dev lo                                                     # own loopback (inside the locator)
fd00:dc1:b:1::    encap seg6local action End.DT46 vrftable 4011 dev vrf4011  # SID → tenant 1
fd00:dc1:b:2::    encap seg6local action End.DT46 vrftable 4012 dev vrf4012  # SID → tenant 2
blackhole fd00:dc1:b::/48                                                    # announced via "network"
```

**exit-b main:** `fd00:dc1:b::/48` via `swp3` (gw-b), `fd00:dc1:a::/48` and
`2001:db8:c::1` via `swp2` (core).

On the link between gw-b and exit-b, the same packet as in partition A is plain SRv6:

```
IP6 fd00:dc1:a::1 > fd00:dc1:b:1::: RT6 (type=4, segleft=0) IP 10.0.16.10 > 10.0.32.10: ICMP echo request
```

exit-a's DCI VRF and the core simply see `fd00:dc1:b::/48` coming from exit-b. Both modes
interoperate without either side knowing the other's mode.

## Reproduce

```sh
make lab-up
docker exec clab-open-dci-m-a    ip route
docker exec clab-open-dci-leaf-a ip route show vrf vrf3981
docker exec clab-open-dci-gw-a   ip route show vrf vrf3981
docker exec clab-open-dci-gw-a   ip -6 route                        # main
docker exec clab-open-dci-gw-a   ip -6 route show vrf vrf104100     # DCI
docker exec clab-open-dci-exit-a ip -6 route show vrf vrf104100
docker exec clab-open-dci-core   ip -6 route
docker exec clab-open-dci-gw-a   vtysh -c 'show bgp ipv4 vpn'
docker exec clab-open-dci-leaf-a vtysh -c 'show bgp l2vpn evpn route type prefix'
```
