# What open-dci changes

## Kernel (idempotent)

- sysctls: forwarding, `seg6_enabled`, `net.vrf.strict_mode=1` (required for End.DT46), and
  `net.ipv6.seg6_flowlabel=1` (the outer flow label is derived from the inner flow, so ECMP
  along the transport spreads the flows instead of putting all traffic between two gateways
  on one path)
- the loopback (`gateway.loopback`, default `<locator>::1`) on `lo`
- the ingress filter, an nftables table `ip6 open-dci` (see
  [the SRv6 domain and its edge](srv6-domain-edge.md))

In DCI network mode additionally:
- a veth pair `dci0` (default VRF, `fe80::1`) ↔ `dci1` (DCI VRF, `fe80::2`)
- the DCI network's device chain raised to `transport.mtu`. The chain (VRF → SVI → bridge →
  VXLAN port) is discovered from the kernel, not from device names. This matters because
  a base config with 9000 there **silently black-holes** full-size SRv6 packets.
- the `lookup local` ip rule moved behind the l3mdev rule

Why a veth and not route leaking: see the [Phase 0b findings](../development/phase0b-findings.md).

## Provisioned networks

For every network, open-dci creates and maintains:
- the VRF (table `table`, default the VNI)
- a bridge `dcibr<vni>` in the VRF. It is not VLAN-aware and serves as the L3VNI's SVI, so
  no VLAN IDs are needed.
- a VXLAN device `dcivx<vni>` (source: `gateway.vtep` or the router-id, port 4789, no
  learning) as the bridge's port, MTU `tenantMTU`
- in the VRF's table: `unreachable default` (IPv4 and IPv6, metric 4278198272), so that a
  lookup finding nothing never falls through to the main table, where the other tenants'
  SIDs live; and `throw <locatorBlock>`. The kernel routes the outer packet of the SRv6
  encapsulation in the tenant VRF's table, so the block, and only the block, has to
  continue in main. Tenants themselves never get there: the ingress filter drops their
  packets to the block.

Each device gets the interface alias `open-dci`. That is how open-dci recognizes its own
devices: it only changes or deletes devices with this alias, and it refuses to provision a
network whose VRF or devices exist without it.

When a network is dropped from the config, open-dci removes it in the order FRR
accepts:
1. `no vni` in the FRR VRF
2. `no router bgp <asn> vrf <name>`, once zebra has released the L3VNI (retried briefly)
3. the kernel devices (VXLAN device, bridge, VRF)
4. `no vrf <name>`, which FRR only accepts once the kernel VRF is gone

## FRR (via `vtysh`, never touching `frr.conf`)

1. Discovers the ASN, router-id, `advertise-all-vni` and the transport VRF's BGP instance
   from the running config.
2. Renders its lines (`open-dci render`) in FRR's canonical form.
3. Applies them only when some are missing from the running config.
4. Removes lines it applied earlier that are no longer desired (state in `--state`). Block
   headers of the base config are never removed. The tenant VRFs, and open-dci's own
   route-maps (all named `DCI-...`), are removed as a whole (see [Provisioned networks](#provisioned-networks)).

The lines for the lab's gw-a1 in DCI network mode (full version:
[`internal/frr/testdata/gw-a1.golden`](https://github.com/mwindower/open-dci/blob/main/internal/frr/testdata/gw-a1.golden)):

```
vrf vrf3981                               (one per network)
 vni 3981
exit-vrf
!
segment-routing
 srv6
  encapsulation
   source-address fd00:dc1:ff::a1          ! gateway.loopback
  exit
  locators
   locator DCI
    prefix fd00:dc1:a::/48 block-len 32 node-len 16
   ...
router bgp 4200000016
 bgp disable-ebgp-connected-route-check   ! the SID as next hop of single-hop eBGP routes
 segment-routing srv6
  locator DCI
 address-family ipv4 vpn                  (and ipv6 vpn)
  neighbor uplink0 activate               ! peers[].interface: the base config's session to the exit
  neighbor uplink0 route-map DCI-PEER-IN in       ! only configured RTs
  neighbor uplink0 maximum-prefix 10000
                                          (with peers[].address instead: neighbor ... remote-as,
                                           ebgp-multihop, update-source <loopback>,
                                           capability extended-nexthop, and no ipv4 unicast)
!
router bgp 4200000016 vrf vrf3981          (one per network)
 bgp router-id 10.0.0.16
 sid vpn per-vrf export 3981             ! networks[].sid (default: the VNI) → fd00:dc1:a:f8d::
 address-family ipv4 unicast              (and ipv6 unicast)
  network 10.0.16.0/24 route-map DCI-AGG   ! networks[].aggregates with hosts from the own
                                          !   fabric (10.0.32.0/24 is partition B's: not here)
  rd vpn export 10.0.0.16:3981
  rt vpn both 65535:1001
  route-map vpn import DCI-vrf3981-v4     (-v6 in ipv6 unicast)
  route-map vpn export DCI-vrf3981-v4
  export vpn
  import vpn
 address-family l2vpn evpn
  advertise ipv4 unicast route-map DCI-ADV   ! type-5 into the own fabric, see DCI-ADV
  advertise ipv6 unicast route-map DCI-ADV
!
router bgp 4200000016 vrf vrf104100
 address-family ipv6 unicast
  redistribute static
!
ipv6 route fd00:dc1::/32 fe80::2 dci0          ! remote locators → DCI VRF
ipv6 route fd00:dc1:a::/48 blackhole
vrf vrf3981
 ip route 10.0.16.0/24 blackhole               ! active aggregate: unused addresses end here
vrf vrf104100
 ipv6 route fd00:dc1:a::/48 fe80::1 dci1       ! own locator → default VRF (SIDs)
 ipv6 route fd00:dc1:ff::a1/128 fe80::1 dci1   ! own loopback, if outside the locator
!
ip prefix-list DCI-vrf3981-v4 seq 663923921 permit 10.0.16.0/24   ! networks[].aggregates (exact) and
ip prefix-list DCI-vrf3981-v4 seq 438889611 permit 10.0.32.0/24   !   networks[].prefixes; seq from the
                                                                  !   entry's content, not its position
route-map DCI-vrf3981-v4 deny 5                 ! (in every filter route-map) a route with the
 match extcommunity DCI-SOO                     !   own site of origin came back from the own
                                                !   fabric: never into the VPN
route-map DCI-vrf3981-v4 permit 10
 match ip address prefix-list DCI-vrf3981-v4
 set large-comm-list DCI-AGG delete             ! the aggregate mark stays on the gateway
!                                               (IPv6 alike; a family without entries: "deny 10")
route-map DCI-vrf3981-v4-default permit 10      ! only with defaultRoute: the same allowlist,
 match ip address prefix-list DCI-vrf3981-v4    !   used for the VPN import (import) or
 set large-comm-list DCI-AGG delete             !   export (export) instead of DCI-vrf3981-v4
route-map DCI-vrf3981-v4-default permit 20      !   ... plus the default route
 match ip address prefix-list DCI-DEFAULT-v4
 set local-preference 50                        !   import only: behind a local default route
ip prefix-list DCI-DEFAULT-v4 seq 5 permit 0.0.0.0/0
bgp large-community-list standard DCI-AGG seq 5 permit 4200000016:0:1   ! only with aggregates:
route-map DCI-AGG permit 10                     !   marks the own aggregates ...
 set large-community 4200000016:0:1
route-map DCI-ADV deny 10                       !   ... and keeps them out of the own
 match large-community DCI-AGG                  !   partition's type-5 (it has the hosts)
route-map DCI-ADV permit 20                     ! type-5: drop every extended community the
 on-match next                                  !   route brought from the VPN (DCI RT, the
 set extended-comm-list DCI-ANY delete          !   remote partition's RTs and router MAC) ...
route-map DCI-ADV permit 30                     !   ... then mark it with the site of origin;
 set extcommunity soo 4200000016:10             !   FRR adds the L3VNI's RT and router MAC
bgp extcommunity-list expanded DCI-ANY seq 5 permit .*
bgp extcommunity-list standard DCI-SOO seq 5 permit soo 4200000016:10   ! gateway.siteOfOrigin
bgp extcommunity-list standard DCI-RT seq 1872273698 permit rt 65535:1001   ! one per route target,
bgp extcommunity-list standard DCI-RT seq 3319327117 permit rt 65535:26782482   !   seq from its content
route-map DCI-PEER-IN permit 10
 match extcommunity DCI-RT
```

In default-VRF mode ([`gw-b1.golden`](https://github.com/mwindower/open-dci/blob/main/internal/frr/testdata/gw-b1.golden)), the DCI VRF
part and the veth routes are replaced by the locator announcement:

```
router bgp 4200000026
 address-family ipv6 unicast
  network fd00:dc1:b::/48
  network fd00:dc1:ff::b1/128              ! own loopback, if outside the locator
!
ipv6 route fd00:dc1:b::/48 blackhole
```
