# Status

`status` on the lab's gw-a1:

```
gateway   fd00:dc1:ff::a1  AS 4200000016  router-id 10.0.0.16  locator fd00:dc1:a::/48
frr       in sync
locator   announced
transport vrf vrf104100  veth up: yes  path MTU: 9166 (need 9166)  local rule last: yes  vrf strict_mode: 1
filter    dropped: tenant-to-transport 0, locator-from-outside 0, loopback-from-outside 0
api       1 network(s), last change 2026-10-14T09:12:35Z, 0 not applied

PEER     AS          STATE        UP        VPNv4 RCVD/SENT  VPNv6 RCVD/SENT
uplink0  4200000014  Established  00:00:29  4/6              4/6
uplink1  4200000018  Established  00:00:29  4/6              4/6

VRF                         RT                RD              SID                          L3VNI    LOCAL v4/v6  REMOTE v4/v6
vrf3981                     65535:1001        10.0.0.16:3981  fd00:dc1:a:f8d:: (End.DT46)  3981 Up  1/1          1/1
vrf3982 (tenant-2/main)     65535:26782482    10.0.0.16:3982  fd00:dc1:a:f8e:: (End.DT46)  3982 Up  1/1          1/1
```

- `locator` is `announced`, `WITHHELD` with the reason while the gateway isn't ready, or
  `DRAINED` (see [Health and failover](health-and-failover.md)).
- `filter` shows the ingress filter's rules with their drop counters (see [The SRv6 domain and its edge](srv6-domain-edge.md)).
- `L3VNI` shows each network's VNI and whether it is up.
- `api` (only with the [API](api.md) enabled): how many networks it adds, when they last
  changed, and networks that can't be applied, each with a warning line. Those networks
  show their stitched network after the VRF (here tenant 2, from
  [dci-controller](../metal-stack/controller.md): project `tenant-2`, label value `main`).
- `PEER` lists the sessions to the exits with the VPN routes received and sent.
- A gateway in default-VRF mode shows `transport default VRF` instead.
- `OPEN_DCI_OUTPUT=json open-dci status` prints the same as JSON.
- The exit code is non-zero unless all of these hold:
  - there is no drift
  - the locator is announced
  - the health checks pass (see [Health and failover](health-and-failover.md#an-unhealthy-gateway-withdraws-itself))
  - the kernel parts are in place
  - every peer is Established
  - every network has a SID
  - every network's L3VNI is `Up`
  - every route in the tenant VRFs is installed (a route FRR never selected is a silent
    black hole; `open-dci run` repairs it within seconds, see
    [Health and failover](health-and-failover.md))
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

- `LOCAL`: the network's prefixes in the own partition: the machines' host routes and the
  own aggregates. `VPN` says whether a prefix is sent to the other partitions (host routes
  inside an aggregate are not, the aggregate is). `KERNEL` says whether it is installed;
  `NO BEST PATH` marks a silent black hole.
- `REMOTE`: prefixes from the other partitions, with the SID they are sent to and the
  number of paths (both exits × both remote gateways).
- `EVPN TYPE-5`: the routes exchanged with the own fabric: `in` from the fabric, `out` the
  remote prefixes the gateway announces into it.
- With `OPEN_DCI_OUTPUT=json`, the routes are in `Networks[].Routes`.

The listing is read from FRR on every call; with many prefixes, select a single network.
