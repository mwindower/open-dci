# Status

`status` on the lab's gw-a1:

```
gateway   fd00:dc1:ff::a1  AS 4200000016  router-id 10.0.0.16  locator fd00:dc1:a::/48
frr       in sync
locator   announced
transport vrf vrf104100  veth up: yes  path MTU: 9166 (need 9166)  local rule last: yes  vrf strict_mode: 1
filter    dropped: tenant-to-transport 0, locator-from-outside 0, loopback-from-outside 0
source    1 network(s), answered 2026-10-13T21:06:35Z, 0 error(s), 0 skipped

PEER     AS          STATE        UP        VPNv4 RCVD/SENT  VPNv6 RCVD/SENT
uplink0  4200000014  Established  00:00:29  4/6              4/6
uplink1  4200000018  Established  00:00:29  4/6              4/6

VRF                         RT                RD              SID                          L3VNI    LOCAL v4/v6  REMOTE v4/v6
vrf3981                     65535:1001        10.0.0.16:3981  fd00:dc1:a:f8d:: (End.DT46)  3981 Up  1/1          1/1
vrf3982 (tenant-2/main)     65535:26782482    10.0.0.16:3982  fd00:dc1:a:f8e:: (End.DT46)  3982 Up  1/1          1/1
```

- `locator` is `announced`, or `WITHHELD` with the reason (see
  [Announcing the locator](health-and-failover.md#announcing-the-locator)).
- `filter` shows the ingress filter's rules with their drop counters (see [The SRv6 domain and its edge](srv6-domain-edge.md)).
- `L3VNI` shows each network's VNI with zebra's state of it.
- `source` (only with a [network source](../configuration/metal-apiserver.md)):
  how many networks it adds, when it last answered, failed polls and skipped groups, followed
  by a line per warning (stale, skipped). Networks from the source show their group after the
  VRF (here tenant 2: project `tenant-2`, label value `main`).
- The only peers are the sessions to the two exits (`peers[].interface`); `AS` comes from
  FRR.
  `RCVD` counts the remote gateways' routes (2 tenants × 2 gateways of pair B). `SENT`
  includes the relayed routes a gateway passes back to its exit. The exit keeps them as
  longer, never-best paths, and the other exits and the partner gateway would drop them
  anyway (own ASN in the path): noise, not loops.

- A gateway in default-VRF mode shows `transport default VRF` instead.
- `OPEN_DCI_OUTPUT=json open-dci status` prints the same as JSON.
- The exit code is non-zero unless all of these hold:
  - there is no drift
  - the locator is announced
  - the health checks pass (see [Health and failover](health-and-failover.md#withdrawing-an-unhealthy-gateway))
  - the kernel parts are in place
  - every peer is Established
  - every network has a SID
  - every network's L3VNI is `Up`
  - no prefix in a tenant VRF has valid paths but no best path (FRR installs nothing for
    it; seen rarely after `frr-reload.py`, fixed by a BGP session reset)
  - the ingress filter is installed

## Routes of a network

`status --routes <VRF|VNI|all>` lists, after the summary, the routes of the selected
networks. On gw-a1:

```
vrf3981  VNI 3981  RT 65535:1001

  LOCAL (4)            NEXT HOP   AS PATH                                      VPN       KERNEL
  10.0.16.0/24         aggregate  -                                            exported  yes
  10.0.16.10/32        10.0.0.11  4200000014 4200000013 4200000011 4200000015  -         yes
  2001:db8:16::/48     aggregate  -                                            exported  yes
  2001:db8:16::10/128  10.0.0.11  4200000014 4200000013 4200000011 4200000015  -         yes

  REMOTE (4)        SID                AS PATH                           PATHS  KERNEL
  10.0.32.0/24      fd00:dc1:b:fab::   4200000014 4200000024 4200000026  4      yes
  10.0.48.0/24      fd00:dc1:c:1393::  4200000014 4200000034 4200000036  4      yes
  2001:db8:32::/48  fd00:dc1:b:fab::   4200000014 4200000024 4200000026  4      yes
  2001:db8:48::/48  fd00:dc1:c:1393::  4200000014 4200000034 4200000036  4      yes

  EVPN TYPE-5 (6)          RD           NEXT HOP   AS PATH                                      RT                     PATHS
  in  10.0.16.10/32        10.0.0.11:4  10.0.0.11  4200000014 4200000013 4200000011 4200000015  59915:3981             2
  in  2001:db8:16::10/128  10.0.0.11:4  10.0.0.11  4200000014 4200000013 4200000011 4200000015  59915:3981             2
  out 10.0.32.0/24         10.0.0.16:5  10.0.0.16  4200000014 4200000024 4200000026             59920:3981             1
  out 10.0.48.0/24         10.0.0.16:5  10.0.0.16  4200000014 4200000034 4200000036             59920:3981             1
  out 2001:db8:32::/48     10.0.0.16:5  10.0.0.16  4200000014 4200000024 4200000026             59920:3981             1
  out 2001:db8:48::/48     10.0.0.16:5  10.0.0.16  4200000014 4200000034 4200000036             59920:3981             1
```

- `LOCAL`: the tenant VRF's prefixes from the own partition, by their best path: host
  routes from EVPN with the VTEP as next hop, and the own aggregates. `VPN` says whether
  the prefix is exported under the network's RD (host routes inside an aggregate are not).
  `KERNEL` says whether zebra installed a route for it (for an aggregate, the blackhole).
  A prefix with valid paths but no best path shows up here as `NO BEST PATH`.
- `REMOTE`: prefixes imported from other partitions via the VPN, with the SID the kernel
  route encapsulates to (from zebra), the AS path (its last AS is the remote gateway
  pair's), and the number of VPN paths (both exits × both remote gateways).
- `EVPN TYPE-5`: the L3VNI's type-5 routes in the global EVPN table. `in` are received from
  the fabric with one of the VRF's import RTs, or an auto-derived RT with the VNI (FRR
  imports those whatever ASN they carry); `out` are advertised by this gateway under the
  L3VNI's RD: the remote prefixes, carrying only the L3VNI's auto RT (open-dci strips the
  network's RT and everything else the route brought from the VPN, and adds its SoO).
- With `OPEN_DCI_OUTPUT=json`, the routes are in `Networks[].Routes`.

The listing is read from FRR on every call; with many prefixes, select a single network.
