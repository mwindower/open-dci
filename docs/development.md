# Development

## Repository layout

```
cmd/open-dci/        CLI (validate, render, diff, apply, run, status, version)
internal/config/     config schema, defaults, validation
internal/frr/        rendering (dci.conf.tpl), running-config parser, drift/removals, vtysh client
internal/kernel/     netlink primitives: veth, MTU path discovery, ip rules, sysctls
internal/gateway/    reconcile loop, pre-flight checks, status
lab/                 containerlab lab: topology, configs/<node>/, e2e tests, labnode
docs/                user docs, feasibility findings, lab routing tables, packet-flow-*.svg
docs/packetflow/     generator of the packet-flow animations (make docs-svg)
deploy/systemd/      systemd unit
Dockerfile           container image (FRR base for vtysh)
.github/workflows/   ci (unit + lab e2e), release (goreleaser)
```

## Tests

```sh
make test          # unit tests: config, rendering, parser, lab specs
make lint          # gofmt + go vet (incl. e2e code)
make lab-redeploy  # lab down + up + e2e tests (see lab/README.md)
```

- **Golden files:** `internal/frr/testdata/gw-{a1,b1}.golden` hold the rendered FRR lines for
  the lab's gateways. Regenerate them with `go test ./internal/frr -update`, and review the
  diff.
- **Canonical form:** `testdata/gw-a1.running.conf` is a real FRR running-config. Every
  rendered line must appear in it verbatim, otherwise drift detection re-applies forever.
- **Lab:** a change counts as verified once `make lab-redeploy` passes from a clean deploy.

## CI and releases

- **`ci.yaml`:** gofmt, vet and unit tests, then the whole lab on a GitHub runner
  (containerlab and FRR pinned there) with diagnostics on failure.
- **`release.yaml`:** a `v*` tag runs goreleaser, which produces static linux/amd64+arm64
  binaries (with the systemd unit) and a multi-arch image
  `ghcr.io/<owner>/open-dci:<version>`.

## Roadmap

| Phase | Content | Status |
|---|---|---|
| 0 / 0b | Feasibility: [standalone](phase0-findings.md), [metal-stack firewall + DCI network](phase0b-findings.md) (the firewall placement was dropped later) | done |
| 1 | Tool MVP: config, validation, kernel, FRR render/diff/apply/reconcile, status, golden + e2e tests | done |
| 1.1 | Default-VRF transport, lab in CI, releases (binaries, image, systemd unit) | done |
| 2 | Robustness: strip the DCI RT from EVPN exports, SoO, BFD, conditional locator announcement (only while the fabric side is up), validate RT/RD local-part widths per admin type and decouple the default RD from the RT | next |
| 3 | Operations: Prometheus metrics, health endpoint | |
| 3.1 | Multi-site config ([day2.md](day2.md)): shared inventory file, RTs derived from network names, VPN route reflectors with `bgp listen range` | |
| 2.2 | Redundant gateway pairs: anycast locator, unique loopbacks, pinned SIDs (`networks[].sid`), failover tests | done |
| 2.3 | SRv6 domain edge: gateway ingress filter (nftables), exit edge ACLs in the lab, no fall-through in tenant VRFs, forged-packet tests | done |
| 2.1 | Safety net: per-network prefix allowlists (`prefixes`), inbound route-target filter and `maxPrefixes` per peer | done |
| 3.2 | Dedicated gateways: provisioned tenant L3VNIs (`networks[].vni`), lab gateways at the exits; the firewall placement removed | done |
| 3.3 | Scale test for dedicated gateways: number of provisioned VRFs, packets per second | |
| 4 | metal-stack integration: gateway configs (tenant VNIs, RTs) generated from metal-api | |
