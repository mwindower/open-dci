# CLAUDE.md

Guidance for AI agents working in this repository.

## What this is

`open-dci` stitches tenant VRFs across independent EVPN/VXLAN domains (e.g. metal-stack
partitions) using SRv6 L3VPN. It turns an FRR-based EVPN VTEP (a metal-stack firewall, or a
dedicated gateway at the exit) into a DCI gateway: VPNv4/v6 with an End.DT46 SID per tenant
VRF, SRv6 transport in a DCI network (EVPN VRF) or the default VRF. VNIs may differ per
partition.

Scope decisions, which should not be revisited without the user:
- **L3 only** (type-5 ↔ VPNv4/v6). No L2 stretch.
- **Stock FRR + Linux kernel.** No custom data plane.
- **Augment the base system, own only what it provisions.** The tool never writes
  `frr.conf`. It adds lines to the running FRR via `vtysh` and re-adds them when the base
  system reloads. Block headers of the base config are never removed. Tenant VRFs of the
  base system (e.g. a firewall's) are only augmented. Only on gateways that are not the
  tenant's VTEP (dedicated gateways, `networks[].vni`) does it create VRF, bridge and VXLAN
  device, tagged with the interface alias `open-dci`, and it only ever changes or deletes
  devices with that tag.
- **Placement-agnostic core.** Firewall and dedicated gateway share all code paths except
  augment vs. provision (see `docs/placement.md`).
- **Keep the code independent of metal-stack.** Discover state from kernel and FRR (ASN,
  router-id, VNI device chain) instead of assuming metal-networker's names. metal-stack
  specifics belong in the lab and in docs.
- **Two transport modes**:
  - `transport.vrf` set: a DCI network (EVPN VRF) joined to the default VRF by a veth pair.
    Route leaking does not work (see `docs/phase0b-findings.md`).
  - unset: the default VRF; the locator is announced by the default BGP instance.

  The lab runs fw-a in the first mode and fw-b, gw-a and gw-b in the second.
- **Go** for everything, lab tooling included. No bash scripts.

## Layout

- `cmd/open-dci`: CLI. `internal/config`: schema and validation. `internal/frr`:
  `dci.conf.tpl`, parser, drift/removals, vtysh. `internal/kernel`: netlink.
  `internal/gateway`: reconcile, pre-flight, status.
- `internal/frr/testdata/{fw,gw}-{a,b}.golden` are the rendered configs for the lab's gateways
  (`go test ./internal/frr -update` rewrites it; review the diff!).
  `testdata/{fw,gw}-a.running.conf` are real FRR running-configs: every rendered line must
  appear in them verbatim, otherwise drift detection re-applies forever.
- `lab/`: the containerlab lab (15 nodes, `clab-open-dci-<node>`).
  - The firewalls start as plain metal-stack firewalls (`configs/fw-*/{node.yaml,frr.conf}`)
    and run `open-dci run` as a sidecar with `configs/fw-*/open-dci.yaml`. fw-a uses a DCI
    network; fw-b and partition B's fabric run the transport in the IPv6 underlay.
  - Tenant 2 (m-a2, m-b2) is stitched by dedicated gateways gw-a/gw-b at the exits, which
    provision its VRFs. gw-a's transport uplink is a routed port in exit-a's DCI VRF.
  - `lab/cmd/labnode` is the container entrypoint (node.yaml → netlink → sidecars → FRR).
  - e2e tests: `lab/*_test.go`, build tag `e2e`.
- `docs/`: findings of phases 0/0b (historical design reasoning), the lab's routing
  tables, and `packet-flow.svg`: the README animation. It's plain SVG + SMIL, no scripts,
  because GitHub renders it via `<img>`. Edit it by hand, and check both colour schemes plus a
  few moments of the animation in a browser.
- Publishing: `LICENSE` (MIT), `Dockerfile` (FRR base image for vtysh),
  `deploy/systemd/`, `.goreleaser.yaml` + `.github/workflows/release.yaml` (tag `v*`), and
  `.github/workflows/ci.yaml` (unit tests + the full lab on a GitHub runner; containerlab and
  FRR versions pinned there).

## Commands

```sh
make test            # unit tests
make lint            # gofmt + go vet (as in CI)
make build           # lab/bin/open-dci (static)
make lab-up          # build + deploy
make lab-check       # e2e tests against the running lab
make lab-redeploy    # down + up + check
make lab-capture
make lab-down
docker exec clab-open-dci-fw-a open-dci status -c /etc/open-dci/config.yaml
docker exec clab-open-dci-<node> vtysh -c '<cmd>'
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
- Removing a provisioned VRF only works in this order: `no vni`, then (after zebra has told
  bgpd, asynchronously) `no router bgp X vrf Y`, then the kernel devices, then `no vrf Y`.
  `no vrf` of an active VRF fails and makes vtysh fail the whole batch.
- `frr-reload.py` may exit 1 from its own second pass ("Refusing to remove a non-existent
  route") even though it worked.
- vtysh reports config errors on stdout (`% ...`), not always via the exit code;
  `frr.Vtysh` checks both.
- FRR daemons' stdout doesn't reach `docker logs`; open-dci's and labnode's output does.
- Auto RTs with 4-byte ASNs use the low 16 bits of the ASN; import falls back to the VNI.
- containerlab needs SUID-root; the agent has no sudo, so ask the user. Only touch
  `clab-open-dci-*` containers (others, e.g. `clab-open-dci-sonic-simple-*`, are unrelated).

## Conventions

- Lab configs are readable reference configs; keep them commented.
- New behaviour gets a unit test (config/render/parse) and, if it touches the data plane or
  FRR, an e2e test.
- The README stays short (intro, animation, modes, quick start, links). Details belong in
  `docs/` (installation, configuration, operation, development) and `lab/README.md`.
- Keep `docs/configuration.md` in sync with `internal/config`, and keep `docs/operation.md`,
  `docs/lab-routing.md`, the golden files and the lab configs in sync.
