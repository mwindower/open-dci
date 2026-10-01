# Lab

A 25-node [containerlab](https://containerlab.dev) lab with three metal-stack-like
partitions, each with two exits, an IPv6 core, two tenants and a redundant gateway pair per
partition whose gateways are attached to both exits. It is the integration test for
open-dci and runs in CI.

```
  partition A (DCI network)    partition B (underlay)     partition C (underlay)
  m-a, m-a2 ── leaf-a          m-b, m-b2 ── leaf-b        m-c, m-c2 ── leaf-c
                │                            │                          │
             spine-a                      spine-b                    spine-c
             ╱     ╲                      ╱     ╲                    ╱     ╲
       exit-a1   exit-a2            exit-b1   exit-b2          exit-c1   exit-c2
           │ ╲   ╱ │                    │ ╲   ╱ │                  │ ╲   ╱ │
       gw-a1   gw-a2                gw-b1   gw-b2              gw-c1   gw-c2
                      all six exits ── core (IPv6 only)
```

Every gateway has one uplink to each exit of its partition (`uplink0` → exit-x1,
`uplink1` → exit-x2).

Both tenants (m-a/m-b/m-c with VNIs 3981/4011/5011, m-a2/m-b2/m-c2 with VNIs
3982/4012/5012) are stitched by the gateway pairs at the exits, which provision their VRFs.

```sh
make lab-up        # build open-dci + labnode, deploy
make lab-check     # Go e2e tests (build tag e2e)
make lab-capture   # SRv6-in-VXLAN between gw-a1 and exit-a1
make lab-down
```

Requirements:
- Linux with `vrf`/`vxlan`/SRv6
- Docker
- containerlab (SUID-root or root; in CI: `make lab-up CLAB="sudo containerlab"`)
- Go ≥ 1.26
- the image `quay.io/frrouting/frr:10.6.0`

## What it models

- **Dedicated gateways** hang off both exits like any fabric peer and start with a base
  config without tenant VRFs (`configs/gw-*/{node.yaml,frr.conf}`). `open-dci run` runs as
  a sidecar with `configs/gw-*/open-dci.yaml`: for each network it creates the VRF, bridge
  and VXLAN device and joins the partition's EVPN with them.
- **Redundant pairs:** the two gateways of a partition share locator, SIDs (pinned to the
  VNI) and ASN; each has its own loopback (`fd00:dc1:ff::a1` …) as encap source. Exits and
  leaves spread traffic over both.
- **Dual attachment:** each gateway reaches the fabric, the transport and the VPN routes via
  both exits (ECMP); a whole exit can fail. The spines connect to both exits, the core to
  all six.
- **No full mesh:** gateways run VPNv4/v6 only on their sessions to their two exits
  (`peers: [{interface: uplink0}, {interface: uplink1}]`); the exits relay the VPN routes
  in a ladder between their loopbacks (`2001:db8:e::a1` …): each exit peers with both exits
  of the neighbouring partitions, not with its own partner. With three partitions the
  closed ladder is the full partition mesh (four sessions per exit); a fourth partition
  would be the first that isn't connected to every other one. exit-a1/a2 reach the
  core in the default VRF via an extra link each (`swp5`).
- **Both transport modes side by side:**
  - Pair A's base config has the DCI network (`vrf104100`, at MTU 9000). SRv6 runs in VXLAN
    to either exit, which routes the DCI VRF into the core; open-dci raises the MTU.
  - Pairs B and C and their exits carry the transport in their partition's IPv6 underlay.
- **The edge of the SRv6 domain:** the exits filter the locator block (`edgeFilter` in
  their `node.yaml`, standing in for switch ACLs); the gateways run open-dci's ingress
  filter.
- **Different tenant VNIs per partition** (3981/4011/5011, 3982/4012/5012), each tenant stitched via
  its own route target (65535:1001, 65535:1002).
- **Leaves** mirror metal-core's SONiC template (auto RTs, one tenant VRF per machine port).
  **Machines** announce their IPs via BGP to the leaf, as in metal-stack.
- **`labnode`** (`cmd/labnode`) is every node's entrypoint:
  1. waits for the links
  2. applies the node's `node.yaml` via netlink
  3. starts sidecars
  4. execs FRR

Routing tables of every node: [docs/lab-routing.md](../docs/lab-routing.md).

## e2e tests

| Test | Checks |
|---|---|
| `TestControlPlane` | all sessions; per tenant VRF: provisioned devices (alias `open-dci`), L3VNI Up, End.DT46 into the VNI's table; remote locators, EVPN→VPN with SID, SRv6 encap routes, machines learn remote machines, exits/core carry no tenant prefixes |
| `TestDataPlane`, `TestMTU` | both tenants between every pair of partitions, v4/v6 in both directions, full-size 9000 B packets |
| `TestTenantIsolation` | the two tenants have no routes to, and no reachability of, each other |
| `TestNoFallThrough` | a lookup that finds nothing in a tenant VRF (gateways, leaves) is unreachable, never the main table |
| `TestFailover` | one gateway per pair loses its uplink: all flows continue via the partner, then it rejoins |
| `TestGatewaysPeerWithTheirExit` | each gateway's only VPN sessions are the ones to its two exits; the exits relay every gateway's routes |
| `TestGatewaysAreNotTransit` | the exits never reach each other through a gateway (only-self-out in the gateways' base config) |
| `TestFabricHasNoTransportRoutes` | no leaf or spine has a route into the locator block, in any table (both transport modes) |
| `TestCoreAnnouncesPerVRF` | the core announces exit-a1/a2's default VRF only the exit loopbacks, their DCI VRF only the locator block |
| `TestExitLadder` | each exit's VPN sessions: its two gateways and both exits of each other partition, not its partner |
| `TestExitFailover` | a whole exit (exit-a1, exit-b2) goes down: all flows continue via the other exit, then it rejoins |
| `TestBothPathsSameSID` | remote gateways get every prefix from both gateways of a pair, with the same anycast SID |
| `TestEdgeDropsForgedSRv6FromFabric` | an underlay device forges SRv6 with a source spoofed inside the block: dropped at the exit |
| `TestGatewayDropsSRv6FromOutsideBlock` | forged SRv6 from inside the domain but outside the block: dropped by the gateway |
| `TestGatewayDropsTenantToTransport` | a tenant forges SRv6 to another tenant's SID (via a misrouted fabric): dropped by the gateway |
| `TestRemovesProvisionedNetwork` | a network dropped from the config: FRR VRF, BGP instance and kernel devices removed |
| `TestRefusesForeignVRF` | a network whose VRF exists without open-dci's tag is refused, the VRF left untouched |
| `TestGatewaysHealthy` | `open-dci status` healthy on all six gateways |
| `TestSelfHealAfterFRRReload` | `frr-reload.py` of the base config wipes all open-dci lines → back within one interval |
| `TestSelfHealMTU` | DCI devices reset to 9000 → raised again, 9000 B packets pass |
| `TestRemovesStaleConfig` | a peer dropped from the config is removed from FRR |
| `TestExportFilter` | a machine announces a prefix outside the allowlist and a default route: learned by the gateway, never exported |
| `TestImportFilter` | the remote gateway exports a prefix outside the local allowlist: received as VPN route, never imported |
| `TestPeerRouteTargetFilter` | the remote gateway sends a route target that isn't configured: dropped at the session |
| `TestMaxPrefixes` | a peer exceeding `maxPrefixes` loses its session; limit restored by open-dci, session back after `clear bgp` |

Debugging:

```sh
docker exec clab-open-dci-gw-a1 open-dci status -c /etc/open-dci/config.yaml
docker exec clab-open-dci-gw-a1 vtysh -c 'show bgp ipv4 vpn'
docker logs clab-open-dci-gw-a1        # labnode + open-dci output (FRR's own logs don't show here)
```
