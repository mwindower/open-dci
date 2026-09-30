# Lab

A 15-node [containerlab](https://containerlab.dev) lab with two metal-stack-like partitions,
an IPv6 core and two tenants. It is the integration test for open-dci and runs in CI.

```
  m-a ─ leaf-a ─ spine-a ─ exit-a ─┐              ┌─ exit-b ─ spine-b ─ leaf-b ─ m-b
 m-a2 ──┘  │                  │     └──── core ────┘     │                  │  └── m-b2
          fw-a               gw-a                       gw-b               fw-b
   firewall, DCI network   dedicated gateways, default VRF      firewall, default VRF
```

- **Tenant 1** (m-a, m-b; VNIs 3981/4011) is stitched by its metal-stack firewalls: open-dci
  augments their tenant VRFs.
- **Tenant 2** (m-a2, m-b2; VNIs 3982/4012) is stitched by dedicated gateways at the exits:
  open-dci provisions the tenant VRFs there.

```sh
make lab-up        # build open-dci + labnode, deploy
make lab-check     # Go e2e tests (build tag e2e)
make lab-capture   # SRv6-in-VXLAN on spine-a's fabric link
make lab-down
```

Requirements:
- Linux with `vrf`/`vxlan`/SRv6
- Docker
- containerlab (SUID-root or root; in CI: `make lab-up CLAB="sudo containerlab"`)
- Go ≥ 1.26
- the image `quay.io/frrouting/frr:10.6.0`

## What it models

- **Firewalls** start as **plain metal-stack firewalls**. Their `node.yaml` and `frr.conf`
  reproduce what metal-networker sets up, including MTU 9000. `open-dci run` runs as a
  sidecar with `configs/fw-*/open-dci.yaml` and turns them into gateways.
- **Both transport modes side by side:**
  - fw-a uses a DCI network (`vrf104100`, SRv6 in VXLAN through partition A's fabric).
  - fw-b and partition B's fabric carry the transport in the IPv6 underlay.
- **Dedicated gateways** gw-a/gw-b start as plain FRR boxes without tenant VRFs
  (`configs/gw-*/`). Their open-dci config has a `vni` per network, so open-dci creates
  the VRF, bridge and VXLAN device and joins the partition's EVPN with them. gw-a has two
  uplinks into exit-a: the fabric side (underlay + EVPN) and a routed port in exit-a's DCI
  VRF for the SRv6 transport. gw-b has a single uplink into partition B's fabric.
- **Different tenant VNIs per partition** (3981/4011, 3982/4012), each tenant stitched via
  its own route target (65535:1001, 65535:1002).
- **Leaves** mirror metal-core's SONiC template (auto RTs, `FIREWALL` peer-group, VNI
  route-map). **Machines** announce their IPs via BGP to the leaf, as in metal-stack.
- **`labnode`** (`cmd/labnode`) is every node's entrypoint:
  1. waits for the links
  2. applies the node's `node.yaml` via netlink
  3. starts sidecars
  4. execs FRR

Routing tables of every node: [docs/lab-routing.md](../docs/lab-routing.md).

## e2e tests

| Test | Checks |
|---|---|
| `TestControlPlane` | all sessions, End.DT46 SIDs, EVPN→VPN with SID, SRv6 encap routes, machines learn remote machines, exits/core carry no tenant prefixes |
| `TestDataPlane`, `TestMTU` | v4/v6 in both directions, full-size 9000 B packets |
| `TestDedicatedGatewayControlPlane` | provisioned devices (alias `open-dci`), L3VNI Up, End.DT46 into the provisioned table, EVPN→VPN with SID, encap routes, machines learn remote machines |
| `TestDedicatedGatewayDataPlane` | tenant 2 v4/v6 in both directions, full-size packets |
| `TestTenantIsolation` | the two tenants have no routes to, and no reachability of, each other |
| `TestRemovesProvisionedNetwork` | a provisioned network dropped from the config: FRR VRF, BGP instance and kernel devices removed |
| `TestRefusesToProvisionBaseVRF` | a `vni` on the firewall's existing tenant VRF is refused |
| `TestGatewaysHealthy` | `open-dci status` healthy on both firewalls and both dedicated gateways |
| `TestSelfHealAfterFRRReload` | `frr-reload.py` (as metal-networker does) wipes all open-dci lines → back within one interval |
| `TestSelfHealMTU` | DCI devices reset to 9000 → raised again, 9000 B packets pass |
| `TestRemovesStaleConfig` | a peer dropped from the config is removed from FRR |

Debugging:

```sh
docker exec clab-open-dci-fw-a open-dci status -c /etc/open-dci/config.yaml
docker exec clab-open-dci-fw-a vtysh -c 'show bgp ipv4 vpn'
docker logs clab-open-dci-fw-a        # labnode + open-dci output (FRR's own logs don't show here)
```
