# Lab

An 11-node [containerlab](https://containerlab.dev) lab with two metal-stack-like partitions
and an IPv6 core. It is the integration test for srv6-dci and runs in CI.

```
 m-a ─ leaf-a ─ spine-a ─ exit-a ─┐              ┌─ exit-b ─ spine-b ─ leaf-b ─ m-b
         │                         └──── core ────┘                       │
        fw-a                                                             fw-b
  DCI network mode                                                default-VRF mode
```

```sh
make lab-up        # build srv6-dci + labnode, deploy
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
  reproduce what metal-networker sets up, including MTU 9000. `srv6-dci run` runs as a
  sidecar with `configs/fw-*/srv6-dci.yaml` and turns them into gateways.
- **Both transport modes side by side:**
  - fw-a uses a DCI network (`vrf104100`, SRv6 in VXLAN through partition A's fabric).
  - fw-b and partition B's fabric carry the transport in the IPv6 underlay.
- **Different tenant VNIs per partition** (3981 / 4011), stitched via one route target.
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
| `TestGatewaysHealthy` | `srv6-dci status` healthy on both firewalls |
| `TestSelfHealAfterFRRReload` | `frr-reload.py` (as metal-networker does) wipes all srv6-dci lines → back within one interval |
| `TestSelfHealMTU` | DCI devices reset to 9000 → raised again, 9000 B packets pass |
| `TestRemovesStaleConfig` | a peer dropped from the config is removed from FRR |

Debugging:

```sh
docker exec clab-srv6-dci-fw-a srv6-dci status -c /etc/srv6-dci/config.yaml
docker exec clab-srv6-dci-fw-a vtysh -c 'show bgp ipv4 vpn'
docker logs clab-srv6-dci-fw-a        # labnode + srv6-dci output (FRR's own logs don't show here)
```
