# Development

## Repository layout

```
cmd/srv6-dci/        CLI (validate, render, diff, apply, run, status, version)
internal/config/     config schema, defaults, validation
internal/frr/        rendering (dci.conf.tpl), running-config parser, drift/removals, vtysh client
internal/kernel/     netlink primitives: veth, MTU path discovery, ip rules, sysctls
internal/gateway/    reconcile loop, pre-flight checks, status
lab/                 containerlab lab: topology, configs/<node>/, e2e tests, labnode
docs/                user docs, feasibility findings, lab routing tables, packet-flow.svg
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

- **Golden files:** `internal/frr/testdata/fw-{a,b}.golden` hold the rendered FRR lines for
  the lab's firewalls. Regenerate them with `go test ./internal/frr -update`, and review the
  diff.
- **Canonical form:** `testdata/fw-a.running.conf` is a real FRR running-config. Every
  rendered line must appear in it verbatim, otherwise drift detection re-applies forever.
- **Lab:** a change counts as verified once `make lab-redeploy` passes from a clean deploy.

## CI and releases

- **`ci.yaml`:** gofmt, vet and unit tests, then the whole lab on a GitHub runner
  (containerlab and FRR pinned there) with diagnostics on failure.
- **`release.yaml`:** a `v*` tag runs goreleaser, which produces static linux/amd64+arm64
  binaries (with the systemd unit) and a multi-arch image
  `ghcr.io/<owner>/srv6-dci:<version>`.

## Roadmap

| Phase | Content | Status |
|---|---|---|
| 0 / 0b | Feasibility: [standalone](phase0-findings.md), [metal-stack firewall + DCI network](phase0b-findings.md) | done |
| 1 | Tool MVP: config, validation, kernel, FRR render/diff/apply/reconcile, status, golden + e2e tests | done |
| 1.1 | Default-VRF transport, lab in CI, releases (binaries, image, systemd unit) | done |
| 2 | Robustness: strip the DCI RT from EVPN exports, SoO, redundant firewalls / multiple peers, BFD, pinned SIDs, nftables, prefix policies, validate RT/RD local-part widths per admin type and decouple the default RD from the RT | next |
| 3 | Operations: Prometheus metrics, health endpoint | |
| 3.1 | Multi-site config ([day2.md](day2.md)): shared inventory file, RTs derived from network names, VPN route reflectors with `bgp listen range` | |
| 4 | metal-stack integration: DCI network as metal-stack network, config from metal-api / firewall-controller | |
