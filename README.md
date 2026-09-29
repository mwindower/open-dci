# srv6-dci

**Stitch EVPN tenant VRFs across independent EVPN/VXLAN domains using SRv6 L3VPN.**

`srv6-dci` turns an existing FRR-based EVPN VTEP (the intended one is a
[metal-stack](https://metal-stack.io) firewall) into a DCI gateway. The tenant's VRF is
exported as BGP VPNv4/VPNv6 with an SRv6 End.DT46 SID to gateways in other EVPN domains, and
their routes come back into the local domain as EVPN type-5. Partitions keep their own VNI,
RT and ASN plans; **VNIs may differ per partition**.

`srv6-dci` **augments** the VTEP and never owns it:
- It creates no VRFs or VXLAN devices.
- It never rewrites `frr.conf`.
- It adds its own lines to the running FRR, and adds the few kernel pieces it needs.
- It puts both back whenever the base system (e.g. metal-networker) rewrites its
  configuration.

> **Status: Phase 1 (MVP).** The tool is proven end to end in a containerlab lab (FRR 10.6,
> kernel 7.2): 7 e2e tests, including self-healing after an FRR reload, MTU resets and removed
> config. Not yet done: redundancy, RT hygiene, nftables, metrics. See [Roadmap](#roadmap).

---

## How it works

```
 Partition A                                                              Partition B
 m-a ─ leaf-a ─ spine-a ─ exit-a ─┐                        ┌─ exit-b ─ spine-b ─ leaf-b ─ m-b
         │                         └──────── core ─────────┘                         │
        fw-a                          (IPv6, locators only)                          fw-b
 tenant vrf3981 (VNI 3981)                                                tenant vrf4011 (VNI 4011)
 DCI    vrf104100 (VNI 104100)                                            DCI    vrf204100 (VNI 204100)
```

- **Tenant VRF → SRv6 L3VPN.** One End.DT46 SID per tenant VRF. Tenant routes learned via
  EVPN are exported as VPNv4/v6. Routes imported from remote gateways are re-advertised into
  the partition as type-5.
- **Transport in a DCI network.** SRv6 runs in a dedicated EVPN VRF with its own VNI (a
  metal-stack network attached to the firewall), through the fabric to the exits, which
  route it between partitions. Exits and core only ever see one locator prefix per gateway.
- **Veth between default VRF and DCI VRF.** FRR's VPN sessions and SIDs live in the default
  VRF, the transport in the DCI VRF. Route leaking does not work for this; see
  [the Phase 0b findings](docs/phase0b-findings.md).

A tenant packet on the fabric wire:

```
IP 10.0.0.12 > 10.0.0.14.4789: VXLAN vni 104100                   ← fw-a → exit-a, DCI network
  IP6 fd00:dc1:a::1 > fd00:dc1:b:1::: RT6 (type=4, segleft=0)      ← SRv6 to fw-b's End.DT46 SID
    IP 10.0.16.10 > 10.0.32.10: ICMP echo request                  ← tenant packet
```

Which node knows which routes: [docs/lab-routing.md](docs/lab-routing.md).

## Configuration

```yaml
# /etc/srv6-dci/config.yaml (lab/configs/fw-a/srv6-dci.yaml)
gateway:
  locator: fd00:dc1:a::/48        # this gateway; its loopback is fd00:dc1:a::1
  locatorBlock: fd00:dc1::/32     # all gateways' locators
  # asn / routerID: discovered from the running FRR (set them to pin/verify)
transport:                        # where the SRv6 transport is routed
  vrf: vrf104100                  # existing VRF of a DCI network; omit: default VRF
  # mtu: 9166                     # DCI devices; must be >= tenantMTU (9000) + 48
peers:
  - {address: "fd00:dc1:b::1", asn: 4200000022}   # remote gateway loopbacks
networks:
  - vrf: vrf3981                  # existing tenant VRF
    routeTarget: "65535:1001"     # the stitched network's identity, same on all gateways
    # rd: default <router-id>:<RT local part>
```

Validation catches, among other things:
- a locator outside the block
- peers inside the own locator or outside the block
- VRFs used twice
- a DCI MTU that can't carry tenant MTU + 48 B

## Usage

```sh
srv6-dci validate -c config.yaml        # check the config
srv6-dci render   -c config.yaml        # FRR lines srv6-dci adds (--asn/--router-id to render offline)
srv6-dci diff     -c config.yaml        # what is missing in the running FRR, and what is stale
srv6-dci apply    -c config.yaml        # reconcile kernel + FRR once
srv6-dci run      -c config.yaml -i 10s # reconcile continuously (service / sidecar)
srv6-dci status   -c config.yaml        # exits non-zero if not healthy
```

`status` on a lab firewall:

```
gateway   fd00:dc1:a::1  AS 4200000012  router-id 10.0.0.12  locator fd00:dc1:a::/48
frr       in sync
kernel    veth up: yes  DCI path MTU: 9166 (need 9166)  local rule last: yes  vrf strict_mode: 1

PEER           AS          STATE        UP        VPNv4 RCVD/SENT  VPNv6 RCVD/SENT
fd00:dc1:b::1  4200000022  Established  00:01:27  2/4              2/4

VRF      RT          RD              SID                        LOCAL v4/v6  REMOTE v4/v6
vrf3981  65535:1001  10.0.0.12:1001  fd00:dc1:a:1:: (End.DT46)  2/2          2/2
```

`SRV6_DCI_OUTPUT=json srv6-dci status` prints the same as JSON.

### What `apply` / `run` do

**Kernel** (idempotent):
- sysctls: forwarding, `seg6_enabled`, and `net.vrf.strict_mode=1` (required for End.DT46)
- the loopback `<locator>::1` on `lo`
- a veth pair `dci0` (main, `fe80::1`) ↔ `dci1` (DCI VRF, `fe80::2`)
- the DCI network's device chain raised to the DCI MTU. The chain (VRF → SVI → bridge →
  VXLAN port) is discovered from the kernel, not from device names. This matters because
  metal-stack's default of 9000 **silently black-holes** full-size SRv6 packets.
- the `lookup local` ip rule moved behind the l3mdev rule

**FRR** (via `vtysh`, never touching `frr.conf`):
1. Discovers the ASN, router-id and per-VRF BGP instances from the running config.
2. Renders its lines (`srv6-dci render`) in FRR's canonical form.
3. Applies them only when some are missing from the running config.
4. Removes lines it applied earlier that are no longer desired (state in
   `/var/lib/srv6-dci/applied.conf`); block headers of the base config are never removed.

The FRR lines for the lab's fw-a (golden file `internal/frr/testdata/fw-a.golden`):

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
 ipv6 route fd00:dc1:a::/48 fe80::1 dci1       ! own locator → main (SIDs, loopback)
```

### Requirements on the environment

- FRR ≥ 10 (tested 10.6) with bgpd, zebra and staticd, and the integrated config
  (`vtysh`). Linux ≥ 5.14 (End.DT46).
- The base system provides the tenant VRFs and the DCI VRF as EVPN L3VNIs (VRF + SVI on a
  bridge + VXLAN port), each with a `router bgp <asn> vrf <name>` instance.
- The leaves must send the DCI VNI to the gateway. With metal-core, the network must be
  attached to the firewall, which puts its VNI into the `match evpn vni` route-map.
- The exits route the DCI VRF to the other partitions. Every fabric link on the DCI path
  must carry ≥ tenant MTU + 98 B (SRv6 + VXLAN), e.g. 9098 for 9000 B tenants.
- No route targets are needed on the EVPN side: auto RTs work across partitions and ASNs
  (see [Phase 0 findings](docs/phase0-findings.md)).

## Lab

```sh
make lab-up        # build srv6-dci + labnode, deploy the 11-node lab
make lab-check     # 7 Go e2e tests
make lab-capture   # SRv6-in-VXLAN on spine-a's fabric link
make lab-down
make test          # unit tests: config, rendering (golden + canonical form), lab specs
```

Requirements: Linux with `vrf`/`vxlan`/SRv6, Docker, [containerlab](https://containerlab.dev)
(SUID-root or root), Go ≥ 1.26, and the image `quay.io/frrouting/frr:10.6.0`.

- **Firewalls:** they start as **plain metal-stack firewalls**. Their `node.yaml` and
  `frr.conf` reproduce what metal-networker sets up, including MTU 9000. `srv6-dci run`
  runs as a sidecar and turns them into gateways.
- **Leaves:** mirror metal-core's SONiC template (auto RTs, `FIREWALL` peer-group, VNI
  route-map).
- **`labnode`:** every node runs the stock FRR image with `labnode`, a small Go entrypoint,
  which applies the node's `node.yaml` via netlink before starting FRR.

| e2e test | Checks |
|---|---|
| `TestControlPlane` | all sessions, End.DT46 SIDs, EVPN→VPN with SID, SRv6 encap routes, machines learn remote machines, exits/core carry no tenant prefixes |
| `TestDataPlane`, `TestMTU` | v4/v6 both directions, full-size 9000 B |
| `TestGatewaysHealthy` | `srv6-dci status` healthy on both firewalls |
| `TestSelfHealAfterFRRReload` | `frr-reload.py` (as metal-networker does) wipes all srv6-dci lines → back within one interval |
| `TestSelfHealMTU` | DCI devices reset to 9000 → raised again, 9000 B packets pass |
| `TestRemovesStaleConfig` | a peer dropped from the config is removed from FRR |

## Roadmap

| Phase | Content | Status |
|---|---|---|
| 0 / 0b | Feasibility: [standalone](docs/phase0-findings.md), [metal-stack firewall + DCI network](docs/phase0b-findings.md) | done |
| 1 | Tool MVP: config, validation, kernel, FRR render/diff/apply/reconcile, status, golden + e2e tests | done |
| 2 | Robustness: strip the DCI RT from EVPN exports, SoO, redundant firewalls / multiple peers, BFD, pinned SIDs, nftables, prefix policies | next |
| 3 | Operations: Prometheus metrics, packaging (container image, systemd unit, releases) | |
| 4 | metal-stack integration: DCI network as metal-stack network, config from metal-api / firewall-controller | |

## Repository layout

```
cmd/srv6-dci/        CLI (validate, render, diff, apply, run, status)
internal/config/     config schema, defaults, validation
internal/frr/        rendering (dci.conf.tpl), running-config parser, drift/removals, vtysh client
internal/kernel/     netlink primitives: veth, MTU path discovery, ip rules, sysctls
internal/gateway/    reconcile loop, pre-flight checks, status
lab/                 containerlab lab: topology, configs/<node>/, e2e tests, labnode
docs/                findings of the feasibility phases, routing tables of the lab
```
