# Operation

## Commands

```sh
open-dci validate -c config.yaml        # check the config
open-dci render   -c config.yaml        # FRR lines open-dci adds (--asn/--router-id to render offline)
open-dci diff     -c config.yaml        # what is missing in the running FRR, and what is stale
open-dci apply    -c config.yaml        # reconcile kernel + FRR once
open-dci run      -c config.yaml -i 10s # reconcile continuously (service / sidecar)
open-dci status   -c config.yaml        # exits non-zero if not healthy
open-dci version
```

Common flags:

| Flag | Default |
|---|---|
| `--vtysh` | `vtysh` |
| `--state` | `/var/lib/open-dci/applied.conf` |
| `-v` | off (debug logging) |

`run` is meant to run permanently, as a service or as a sidecar next to FRR. It retries until
FRR is up, and it re-applies its configuration whenever the base system (e.g.
metal-networker running `frr-reload.py`) removes it. In the lab, that takes one interval.

## Status

`status` on the lab's fw-a:

```
gateway   fd00:dc1:a::1  AS 4200000012  router-id 10.0.0.12  locator fd00:dc1:a::/48
frr       in sync
transport vrf vrf104100  veth up: yes  path MTU: 9166 (need 9166)  local rule last: yes  vrf strict_mode: 1

PEER           AS          STATE        UP        VPNv4 RCVD/SENT  VPNv6 RCVD/SENT
fd00:dc1:b::1  4200000022  Established  00:01:27  2/4              2/4

VRF      RT          RD              SID                        L3VNI  LOCAL v4/v6  REMOTE v4/v6
vrf3981  65535:1001  10.0.0.12:1001  fd00:dc1:a:1:: (End.DT46)  base   2/2          2/2
```

On a dedicated gateway (the lab's gw-a), the network is provisioned, and `L3VNI` shows its
VNI with zebra's state:

```
VRF      RT          RD              SID                         L3VNI    LOCAL v4/v6  REMOTE v4/v6
vrf3982  65535:1002  10.0.0.16:1002  fd00:dc1:a2:1:: (End.DT46)  3982 Up  1/1          1/1
```

- A gateway in default-VRF mode shows `transport default VRF` instead.
- `OPEN_DCI_OUTPUT=json open-dci status` prints the same as JSON.
- The exit code is non-zero unless all of these hold:
  - there is no drift
  - the kernel parts are in place
  - every peer is Established
  - every network has a SID
  - every provisioned network's L3VNI is `Up`

## What `apply` / `run` change

### Kernel (idempotent)

- sysctls: forwarding, `seg6_enabled`, and `net.vrf.strict_mode=1` (required for End.DT46)
- the loopback `<locator>::1` on `lo`

In DCI network mode additionally:
- a veth pair `dci0` (default VRF, `fe80::1`) ↔ `dci1` (DCI VRF, `fe80::2`)
- the DCI network's device chain raised to `transport.mtu`. The chain (VRF → SVI → bridge →
  VXLAN port) is discovered from the kernel, not from device names. This matters because
  metal-stack's default of 9000 **silently black-holes** full-size SRv6 packets.
- the `lookup local` ip rule moved behind the l3mdev rule

Why a veth and not route leaking: see the [Phase 0b findings](phase0b-findings.md).

### Provisioned networks

For every network with a `vni`, open-dci creates and maintains:
- the VRF (table `table`, default the VNI)
- a bridge `dcibr<vni>` in the VRF. It is not VLAN-aware and serves as the L3VNI's SVI, so
  no VLAN IDs are needed.
- a VXLAN device `dcivx<vni>` (source: `gateway.vtep` or the router-id, port 4789, no
  learning) as the bridge's port, MTU `tenantMTU`

Each device gets the interface alias `open-dci`. That is how open-dci recognizes its own
devices: it only changes or deletes devices with this alias, and it refuses to provision a
network whose VRF or devices exist without it (e.g. a firewall's tenant VRF).

When a provisioned network is dropped from the config, open-dci removes it in the order FRR
accepts:
1. `no vni` in the FRR VRF
2. `no router bgp <asn> vrf <name>`, once zebra has released the L3VNI (retried briefly)
3. the kernel devices (VXLAN device, bridge, VRF)
4. `no vrf <name>`, which FRR only accepts once the kernel VRF is gone

### FRR (via `vtysh`, never touching `frr.conf`)

1. Discovers the ASN, router-id and per-VRF BGP instances from the running config.
2. Renders its lines (`open-dci render`) in FRR's canonical form.
3. Applies them only when some are missing from the running config.
4. Removes lines it applied earlier that are no longer desired (state in `--state`). Block
   headers of the base config are never removed. Provisioned VRFs are removed as a whole
   (see above).

The lines for the lab's fw-a in DCI network mode (full version:
[`internal/frr/testdata/fw-a.golden`](../internal/frr/testdata/fw-a.golden)):

```
segment-routing
 srv6
  encapsulation
   source-address fd00:dc1:a::1
  exit
  locators
   locator DCI
    prefix fd00:dc1:a::/48 block-len 32 node-len 16
   ...
router bgp 4200000012
 neighbor fd00:dc1:b::1 remote-as 4200000022
 neighbor fd00:dc1:b::1 ebgp-multihop 16
 neighbor fd00:dc1:b::1 update-source fd00:dc1:a::1
 neighbor fd00:dc1:b::1 capability extended-nexthop
 segment-routing srv6
  locator DCI
 address-family ipv4 unicast
  no neighbor fd00:dc1:b::1 activate
 address-family ipv4 vpn                  (and ipv6 vpn)
  neighbor fd00:dc1:b::1 activate
!
router bgp 4200000012 vrf vrf3981
 sid vpn per-vrf export auto
 address-family ipv4 unicast              (and ipv6 unicast)
  rd vpn export 10.0.0.12:1001
  rt vpn both 65535:1001
  export vpn
  import vpn
!
router bgp 4200000012 vrf vrf104100
 address-family ipv6 unicast
  redistribute static
!
ipv6 route fd00:dc1::/32 fe80::2 dci0          ! remote locators → DCI VRF
ipv6 route fd00:dc1:a::/48 blackhole
vrf vrf104100
 ipv6 route fd00:dc1:a::/48 fe80::1 dci1       ! own locator → default VRF (SIDs, loopback)
```

In default-VRF mode ([`fw-b.golden`](../internal/frr/testdata/fw-b.golden)), the DCI VRF
part and the veth routes are replaced by the locator announcement:

```
router bgp 4200000022
 address-family ipv6 unicast
  network fd00:dc1:b::/48
!
ipv6 route fd00:dc1:b::/48 blackhole
```

## What the network sees

A tenant packet on the fabric wire in partition A (DCI network mode):

```
IP 10.0.0.12 > 10.0.0.14.4789: VXLAN vni 104100                   ← fw-a → exit-a, DCI network
  IP6 fd00:dc1:a::1 > fd00:dc1:b:1::: RT6 (type=4, segleft=0)      ← SRv6 to fw-b's End.DT46 SID
    IP 10.0.16.10 > 10.0.32.10: ICMP echo request                  ← tenant packet
```

Which node knows which routes, for both modes: [lab-routing.md](lab-routing.md).

## Adding partitions and networks

What to change on which gateway when a partition joins or a network is stitched, and how to
choose route targets: [day2.md](day2.md).
