# Routing tables in the lab

What each node of `lab/` knows once the lab has converged. Taken from a clean deploy
(`make lab-up`, all e2e tests passing); the firewalls' DCI parts were added by open-dci.

The two partitions use the two transport modes:
- **Partition A** runs the SRv6 transport in a DCI network (`transport.vrf: vrf104100`).
  It is shown in detail below.
- **Partition B** runs it in the default VRF: the fabric underlay carries IPv6 locators and
  there is no DCI network. See [Partition B](#partition-b-transport-in-the-default-vrf).
  Its tenant side mirrors A: `m-b` 10.0.32.10, tenant `vrf4011`/VNI 4011, locator
  `fd00:dc1:b::/48`, SID `fd00:dc1:b:1::`.

A second tenant (m-a2 10.0.17.10, m-b2 10.0.33.10) is stitched by the dedicated gateways
gw-a and gw-b at the exits, whose tenant VRFs open-dci provisions. See
[Dedicated gateways](#dedicated-gateways-tenant-2).

Link-local, multicast, the management network and the kernel's local routes are omitted.

## Overview

| Node / table | Tenant prefixes | DCI prefixes (locators) | Underlay (VTEP loopbacks) |
|---|---|---|---|
| m-a | ✓ (own + remote, via leaf) | – | – |
| leaf-a `vrf3981` | ✓ | – | – |
| leaf-a main | – | – | ✓ |
| fw-a `vrf3981` (tenant) | ✓ (remote ones via SRv6) | – | – |
| fw-a main | – | own SID + everything else via veth | ✓ |
| fw-a `vrf104100` (DCI) | – | ✓ | – |
| spine-a | – | – | ✓ |
| exit-a main | – | – | ✓ |
| exit-a `vrf104100` (DCI) | – | ✓ | – |
| core | – | ✓ (locators only) | – |

In short:
- **Tenant prefixes exist only in tenant VRFs** (machine, leaf, firewall).
- **The DCI VRF only carries locators**: one /48 per gateway, which also contains the
  gateway's loopback. The e2e test `no-tenant-state/*` asserts this for exits and core.
- **Spine and core** never know a tenant.

## Machine `m-a`

The machine learns everything from its leaf via BGP. The remote machine is a plain /32 like
any local one. The source address is set by `ip protocol bgp route-map RM_SET_SRC`, as in
metal-stack.

```
10.0.16.2       via inet6 fe80::…  dev lan0 proto bgp src 10.0.16.10     # fw-a (tenant SVI)
10.0.32.2       via inet6 fe80::…  dev lan0 proto bgp src 10.0.16.10     # fw-b (other partition)
10.0.32.10      via inet6 fe80::…  dev lan0 proto bgp src 10.0.16.10     # m-b  (other partition)
2001:db8:16::10 dev lo                                                   # own IP
2001:db8:16::2  via fe80::… dev lan0 proto bgp
2001:db8:32::2  via fe80::… dev lan0 proto bgp
2001:db8:32::10 via fe80::… dev lan0 proto bgp
```

## Leaf `leaf-a`

**Tenant VRF `vrf3981`.** The local machine is behind `swp1`. Everything remote, including
partition B, points to the firewall's VTEP (10.0.0.12), carried over VXLAN VNI 3981.

```
10.0.16.10      via inet6 fe80::… dev swp1 proto bgp                     # m-a (BGP, unnumbered)
10.0.16.2       via 10.0.0.12 dev vlan3981 proto bgp onlink               # fw-a
10.0.32.2       via 10.0.0.12 dev vlan3981 proto bgp onlink               # fw-b   ← re-originated by fw-a
10.0.32.10      via 10.0.0.12 dev vlan3981 proto bgp onlink               # m-b    ← re-originated by fw-a
(IPv6 alike, next hop ::ffff:10.0.0.12)
```

**Main (underlay):** only VTEP loopbacks: 10.0.0.12 (fw-a) via `swp3`, 10.0.0.13/14
(spine, exit) via `swp31`.

**EVPN table (BGP).** The leaf sees and forwards the DCI network's type-5 routes, but
imports them nowhere: it has no VRF for VNI 104100.

| Type-5 prefix | Originator (RD) | RTs |
|---|---|---|
| 10.0.16.10/32 | leaf-a (10.0.0.11) | `59915:3981` |
| 10.0.16.2/32 | fw-a (10.0.0.12) | `59916:3981` |
| 10.0.32.10/32, 10.0.32.2/32 | fw-a (10.0.0.12) | `59916:3981` **+ `65535:1001`** (DCI RT leak, Phase 2) |
| fd00:dc1:a::/48 | fw-a (10.0.0.12) | `59916:104100` |
| fd00:dc1:b::/48, 2001:db8:c::1/128 | exit-a (10.0.0.14) | `59918:104100` |

- Auto RTs with 4-byte ASNs use the low 16 bits of the ASN
  (4200000011 mod 65536 = 59915). They differ per router and still match, because FRR falls
  back to the VNI when importing.
- Each firewall/exit route is present twice on the leaf. The second copy came back from the
  spine: `allowas-in 2` from the metal-core template accepts the leaf's own ASN. This is
  harmless.

## Firewall / gateway `fw-a`

The firewall has three tables that work together.

**Tenant VRF `vrf3981`**: local via VXLAN, remote via SRv6:

```
10.0.16.10      via 10.0.0.11 dev vlan3981 proto bgp onlink                                    # m-a (EVPN from leaf-a)
10.0.32.10      encap seg6 mode encap segs 1 [ fd00:dc1:b:1:: ] via inet6 fe80::2 dev dci0  # m-b (VPN from fw-b)
10.0.32.2       encap seg6 mode encap segs 1 [ fd00:dc1:b:1:: ] via inet6 fe80::2 dev dci0  # fw-b
(own SVI 10.0.16.2 / 2001:db8:16::2 is local; IPv6 routes alike)
```

**Main (default VRF)**: underlay, own SID, and the path into the DCI VRF:

```
10.0.0.11/13/14     via inet6 fe80::… dev lan0 proto bgp                              # VTEPs (underlay)
fd00:dc1:a::1       dev lo                                                            # own loopback = VPN session endpoint, encap source
fd00:dc1:a:1::      encap seg6local action End.DT46 vrftable 1000 dev vrf3981         # own SID → tenant VRF
blackhole fd00:dc1:a::/48                                                             # rest of own locator
fd00:dc1::/32       via fe80::2 dev dci0                                              # all other locators → DCI VRF (veth)
```

**DCI VRF `vrf104100`**: locators only:

```
fd00:dc1:a::/48     via fe80::1 dev dci1                                       # own locator → back to main (veth)
fd00:dc1:b::/48     via ::ffff:10.0.0.14 dev vlan104100 onlink                 # remote locator via exit-a (VXLAN VNI 104100)
2001:db8:c::1/128   via ::ffff:10.0.0.14 dev vlan104100 onlink                 # core loopback (harmless)
```

**BGP view of the prefix 10.0.32.10/32 on fw-a**, as it passes through three tables:

| Table | Next hop | AS path | Notes |
|---|---|---|---|
| VPNv4, RD `10.0.1.12:1001` | `fd00:dc1:b::1` (fw-b) | 4200000022 4200000021 4200000025 | `RT:65535:1001`, SID `fd00:dc1:b::` + label 16 → `fd00:dc1:b:1::` |
| VRF `vrf3981` | `fd00:dc1:b::1` (`@0`, resolved in main) | same | imported via RT `65535:1001` |
| EVPN type-5 towards leaf-a | 10.0.0.12 (own VTEP), RMAC of fw-a | 4200000012 … | RT `59916:3981` (+ leaked `65535:1001`) |

The opposite direction: `10.0.16.10/32` comes from leaf-a as type-5 (next hop 10.0.0.11),
lands in `vrf3981`, and is exported as VPNv4 with RD `10.0.0.12:1001` and SID
`fd00:dc1:a:1::`.

## Spine `spine-a`

Only VTEP loopbacks: 10.0.0.11, 10.0.0.12 via `swp1`, and 10.0.0.14 via `swp2`. It passes
all EVPN routes through unchanged, including the next hop, and imports none of them.

## Exit `exit-a`

**Main (underlay):** VTEP loopbacks only (10.0.0.11/12/13 via `swp1`, gw-a's 10.0.0.16 via
`swp3`).

**DCI VRF `vrf104100`:** EVPN towards the partition, plain IPv6 towards the core:

```
fd00:dc1:a::/48     via ::ffff:10.0.0.12 dev vlan104100 onlink    # fw-a's locator (type-5 from fw-a)
fd00:dc1:b::/48     via fe80::… dev swp2 proto bgp                # fw-b's locator (from core)
fd00:dc1:a2::/48    via fe80::… dev swp4 proto bgp                # gw-a's locator (routed port)
fd00:dc1:b2::/48    via fe80::… dev swp2 proto bgp                # gw-b's locator (from core)
2001:db8:c::1/128   via fe80::… dev swp2 proto bgp                # core loopback
```

## Core

```
fd00:dc1:a::/48     via fe80::… dev swp1 proto bgp                # fw-a, towards exit-a
fd00:dc1:a2::/48    via fe80::… dev swp1 proto bgp                # gw-a
fd00:dc1:b::/48     via fe80::… dev swp2 proto bgp                # fw-b, towards exit-b
fd00:dc1:b2::/48    via fe80::… dev swp2 proto bgp                # gw-b
```

The core carries one prefix per gateway and nothing else (plus fw-b's loopback /128 from
metal-networker's `redistribute connected`, see partition B).

## Reproduce

```sh
make lab-up
docker exec clab-open-dci-m-a    ip route
docker exec clab-open-dci-leaf-a ip route show vrf vrf3981
docker exec clab-open-dci-fw-a   ip route show vrf vrf3981
docker exec clab-open-dci-fw-a   ip -6 route                        # main
docker exec clab-open-dci-fw-a   ip -6 route show vrf vrf104100     # DCI
docker exec clab-open-dci-exit-a ip -6 route show vrf vrf104100
docker exec clab-open-dci-core   ip -6 route
docker exec clab-open-dci-fw-a   vtysh -c 'show bgp ipv4 vpn'
docker exec clab-open-dci-leaf-a vtysh -c 'show bgp l2vpn evpn route type prefix'
```

## Partition B: transport in the default VRF

fw-b's open-dci config has no `transport.vrf`. open-dci announces the locator from the
default BGP instance (`network fd00:dc1:b::/48`, backed by a blackhole route), and the
fabric's IPv6 underlay carries it to the exit and on to the core. There is no veth, no DCI
network, no VXLAN on the transport path, and fw-b's tenant side is unchanged.

**fw-b main:**

```
fd00:dc1:a::/48   via fe80::… dev lan0 proto bgp                         # remote locator via the underlay
fd00:dc1:b::1     dev lo                                                 # own loopback (inside the locator)
fd00:dc1:b:1::    encap seg6local action End.DT46 vrftable 1000 dev vrf4011
blackhole fd00:dc1:b::/48                                                # announced via "network"
```

**leaf-b, spine-b, exit-b main** (the same on each hop, pointing outwards or inwards):

```
fd00:dc1:b::/48   via fe80::… (towards fw-b)
fd00:dc1:b::1     via fe80::… (towards fw-b)       # metal-networker's "redistribute connected" of lo; inside the /48
fd00:dc1:a::/48   via fe80::… (towards exit-b → core)
2001:db8:c::1     via fe80::… (core loopback)
```

On the fabric of partition B, the same packet as above is plain SRv6:

```
IP6 fd00:dc1:a::1 > fd00:dc1:b:1::: RT6 (type=4, segleft=0) IP 10.0.16.10 > 10.0.32.10: ICMP echo request
```

exit-a's DCI VRF and the core simply see `fd00:dc1:b::/48` (and the /128) coming from
exit-b. Both modes interoperate without either side knowing the other's mode.

## Dedicated gateways (tenant 2)

gw-a and gw-b are not the tenant's VTEP: open-dci provisions `vrf3982` (VNI 3982) on gw-a and
`vrf4012` (VNI 4012) on gw-b, each with a bridge `dcibr<vni>` and a VXLAN device
`dcivx<vni>` (VTEP = router-id). The SRv6 transport runs in the default VRF: gw-a reaches
the core through a routed port in exit-a's DCI VRF (`uplink1`), gw-b through partition B's
underlay.

**gw-a `vrf3982`** (provisioned, kernel table 3982):

```
10.0.17.10        via 10.0.0.11 dev dcibr3982 onlink                          # m-a2, type-5 from leaf-a
10.0.33.10        encap seg6 mode encap segs 1 [ fd00:dc1:b2:1:: ] dev uplink1  # m-b2, via gw-b's SID
2001:db8:17::10   via ::ffff:10.0.0.11 dev dcibr3982 onlink
2001:db8:33::10   encap seg6 mode encap segs 1 [ fd00:dc1:b2:1:: ] dev uplink1
```

**gw-a main:**

```
fd00:dc1:a2::1    dev lo                                                  # own loopback
fd00:dc1:a2:1::   encap seg6local action End.DT46 vrftable 3982 dev vrf3982
blackhole fd00:dc1:a2::/48                                                # announced via "network"
fd00:dc1:b2::/48  via fe80::… dev uplink1 proto bgp                       # gw-b (and all other locators)
```

**leaf-a `vrf3982`:** the remote machine via gw-a's VTEP, like any type-5 route:

```
10.0.17.10        via fe80::… dev swp2 proto bgp                          # m-a2
10.0.33.10        via 10.0.0.16 dev vlan3982 onlink                       # m-b2, type-5 from gw-a
```

The exits only forward the EVPN routes between leaf and gateway. Tenant 1 and tenant 2
share leaves, exits and core, but no routes (e2e test `TestTenantIsolation`).
