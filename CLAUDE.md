# CLAUDE.md

Guidance for AI agents working in this repository.

## What this is

`srv6-dci` stitches tenant VRFs across independent EVPN/VXLAN domains (e.g. metal-stack
partitions) using SRv6 L3VPN. It turns an existing FRR-based EVPN VTEP (a metal-stack
firewall) into a DCI gateway: VPNv4/v6 with an End.DT46 SID per tenant VRF, SRv6 transport
in a dedicated DCI network (EVPN VRF). VNIs may differ per partition.

Scope decisions, which should not be revisited without the user:
- **L3 only** (type-5 ↔ VPNv4/v6). No L2 stretch.
- **Stock FRR + Linux kernel.** No custom data plane.
- **Augment, never own.** The tool never creates VRFs/VXLAN devices and never writes
  `frr.conf`. It adds lines to the running FRR via `vtysh` and re-adds them when the base
  system reloads. Block headers of the base config are never removed.
- **Keep the code independent of metal-stack.** Discover state from kernel and FRR (ASN,
  router-id, VNI device chain) instead of assuming metal-networker's names. metal-stack
  specifics belong in the lab and in docs.
- **Transit via a DCI network + veth pair** between the default VRF and the DCI VRF. Route
  leaking does not work (see `docs/phase0b-findings.md`).
- **Go** for everything, lab tooling included. No bash scripts.

## Layout

- `cmd/srv6-dci`: CLI. `internal/config`: schema and validation. `internal/frr`:
  `dci.conf.tpl`, parser, drift/removals, vtysh. `internal/kernel`: netlink.
  `internal/gateway`: reconcile, pre-flight, status.
- `internal/frr/testdata/fw-a.golden` is the rendered config for the lab's fw-a
  (`go test ./internal/frr -update` rewrites it; review the diff!).
  `testdata/fw-a.running.conf` is a real FRR running-config: every rendered line must appear
  in it verbatim, otherwise drift detection re-applies forever.
- `lab/`: the containerlab lab (11 nodes, `clab-srv6-dci-<node>`).
  - Firewalls start as plain metal-stack firewalls (`configs/fw-*/{node.yaml,frr.conf}`) and
    run `srv6-dci run` as a sidecar with `configs/fw-*/srv6-dci.yaml`.
  - `lab/cmd/labnode` is the container entrypoint (node.yaml → netlink → sidecars → FRR).
  - e2e tests: `lab/*_test.go`, build tag `e2e`.
- `docs/`: findings of phases 0/0b (historical design reasoning) and the lab's routing
  tables.

## Commands

```sh
make test            # unit tests
make build           # lab/bin/srv6-dci (static)
make lab-up          # build + deploy
make lab-check       # e2e tests against the running lab
make lab-redeploy    # down + up + check
make lab-capture
make lab-down
docker exec clab-srv6-dci-fw-a srv6-dci status -c /etc/srv6-dci/config.yaml
docker exec clab-srv6-dci-<node> vtysh -c '<cmd>'
```

A change counts as verified only after `make lab-redeploy` passes from a clean deploy.
Binaries are bind-mounted, and `go build` replaces the inode, so a running lab keeps the
old binary until it is redeployed (or you `docker cp` for a quick look).

## Gotchas (learned the hard way)

- `net.vrf.strict_mode=1` is required for End.DT46 and only exists after the first VRF. If
  it is missing, FRR shows the SID but the kernel route is rejected (`B>r`).
- FRR `import vrf <evpn-vrf>` leaks EVPN routes as **invalid** (VTEP next hop resolved in the
  source VRF).
- An incoming BGP connection is assigned to the BGP instance of the VRF it arrived in, so the
  VPN session must enter via a default-VRF interface (the veth).
- The local `ip rule` at priority 0 makes default-VRF addresses "local" inside VRFs; it
  must go behind l3mdev.
- metal-stack's 9000 B on bridge/vni/vlan black-holes full-size SRv6 packets without an
  ICMP error; the DCI chain needs ≥ tenant MTU + 48.
- The veth uses link-local next hops (`fe80::1`/`fe80::2`), so metal-networker's
  `redistribute connected` in the DCI VRF doesn't announce a transfer net.
- FRR prints some lines differently from how they are typed (e.g. `func-bits 16` is dropped
  as the default). Render in canonical form; `TestRenderMatchesRunningConfig` guards this.
- `frr-reload.py` may exit 1 from its own second pass ("Refusing to remove a non-existent
  route") even though it worked.
- vtysh reports config errors on stdout (`% ...`), not always via the exit code;
  `frr.Vtysh` checks both.
- FRR daemons' stdout doesn't reach `docker logs`; srv6-dci's and labnode's output does.
- Auto RTs with 4-byte ASNs use the low 16 bits of the ASN; import falls back to the VNI.
- containerlab needs SUID-root; the agent has no sudo, so ask the user. Only touch
  `clab-srv6-dci-*` containers (others, e.g. `clab-srv6-dci-sonic-simple-*`, are unrelated).

## Conventions

- Lab configs are readable reference configs; keep them commented.
- New behaviour gets a unit test (config/render/parse) and, if it touches the data plane or
  FRR, an e2e test.
- Keep `README.md`, `docs/lab-routing.md`, the golden file and the lab configs in sync.
