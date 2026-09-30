# Lab

A 13-node [containerlab](https://containerlab.dev) lab with two metal-stack-like partitions,
an IPv6 core and two tenants. It is the integration test for open-dci and runs in CI.

```
  m-a ─ leaf-a ─ spine-a ─ exit-a ─┐              ┌─ exit-b ─ spine-b ─ leaf-b ─ m-b
 m-a2 ──┘                     │     └──── core ────┘     │                     └── m-b2
                             gw-a                       gw-b
                         DCI network                default VRF
```

Both tenants (m-a/m-b with VNIs 3981/4011, m-a2/m-b2 with VNIs 3982/4012) are stitched by
the dedicated gateways at the exits, which provision their VRFs.

```sh
make lab-up        # build open-dci + labnode, deploy
make lab-check     # Go e2e tests (build tag e2e)
make lab-capture   # SRv6-in-VXLAN between gw-a and exit-a
make lab-down
```

Requirements:
- Linux with `vrf`/`vxlan`/SRv6
- Docker
- containerlab (SUID-root or root; in CI: `make lab-up CLAB="sudo containerlab"`)
- Go ≥ 1.26
- the image `quay.io/frrouting/frr:10.6.0`

## What it models

- **Dedicated gateways** gw-a/gw-b hang off the exits like any fabric peer and start with a
  base config without tenant VRFs (`configs/gw-*/{node.yaml,frr.conf}`). `open-dci run`
  runs as a sidecar with `configs/gw-*/open-dci.yaml`: for each network it creates the VRF,
  bridge and VXLAN device and joins the partition's EVPN with them.
- **Both transport modes side by side:**
  - gw-a's base config has the DCI network (`vrf104100`, at MTU 9000). SRv6 runs in VXLAN
    to exit-a, which routes the DCI VRF into the core; open-dci raises the MTU.
  - gw-b and exit-b carry the transport in partition B's IPv6 underlay.
- **Different tenant VNIs per partition** (3981/4011, 3982/4012), each tenant stitched via
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
| `TestDataPlane`, `TestMTU` | both tenants, v4/v6 in both directions, full-size 9000 B packets |
| `TestTenantIsolation` | the two tenants have no routes to, and no reachability of, each other |
| `TestRemovesProvisionedNetwork` | a network dropped from the config: FRR VRF, BGP instance and kernel devices removed |
| `TestRefusesForeignVRF` | a network whose VRF exists without open-dci's tag is refused, the VRF left untouched |
| `TestGatewaysHealthy` | `open-dci status` healthy on both gateways |
| `TestSelfHealAfterFRRReload` | `frr-reload.py` of the base config wipes all open-dci lines → back within one interval |
| `TestSelfHealMTU` | DCI devices reset to 9000 → raised again, 9000 B packets pass |
| `TestRemovesStaleConfig` | a peer dropped from the config is removed from FRR |

Debugging:

```sh
docker exec clab-open-dci-gw-a open-dci status -c /etc/open-dci/config.yaml
docker exec clab-open-dci-gw-a vtysh -c 'show bgp ipv4 vpn'
docker logs clab-open-dci-gw-a        # labnode + open-dci output (FRR's own logs don't show here)
```
