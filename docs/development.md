# Development

## Repository layout

```
cmd/open-dci/        CLI (validate, render, diff, apply, run, status, version)
cmd/dci-controller/  the metal-stack controller (metal-apiserver → gateway API)
api/opendci/v1/      the gateway API (gateway.proto + generated Go code; make proto)
internal/config/     config schema, defaults, validation
internal/frr/        rendering (dci.conf.tpl), running-config parser, drift/removals, vtysh client
internal/kernel/     netlink primitives: veth, MTU path discovery, ip rules, sysctls
internal/gateway/    reconcile loop, pre-flight checks, status, Monitor (what run publishes)
internal/metrics/    /metrics (Prometheus) and /healthz from the Monitor
internal/api/        the gateway API server (run --api-listen)
internal/controller/ dci-controller: rounds, pushes, metrics
internal/metalstack/ metal-apiserver: mapping (labels → networks), client, fakeapi for tests and the lab
lab/                 containerlab lab: topology, configs/<node>/, e2e tests, labnode, fakeapi
website/            the website (Docusaurus): docs/ (user documentation), static/img/ (drawings)
docs/                developer documentation (this file, internals, measurements, lab, history)
tools/packetflow/    generator of the drawings logical-view.svg and packet-flow.svg (make docs-svg)
deploy/              systemd units, dci-controller example config
Dockerfile           container image (FRR base for vtysh)
.github/workflows/   ci (unit + lab e2e), release (goreleaser), pages (the website)
```

## Tests

```sh
make test          # unit tests: config, rendering, parser, lab specs
make lint          # gofmt + go vet (incl. e2e code)
make lab-redeploy  # lab down + up + e2e tests (see the lab docs)
make website       # build this website; make website-serve to preview it
make proto         # regenerate the API code after changing api/**/*.proto
```

- **Golden files:** `internal/frr/testdata/gw-{a1,b1}.golden` hold the rendered FRR lines for
  the lab's gateways. Regenerate them with `go test ./internal/frr -update`, and review the
  diff.
- **Canonical form:** `testdata/gw-a1.running.conf` is a real FRR running-config. Every
  rendered line must appear in it verbatim, otherwise drift detection re-applies forever.
- **Lab:** a change counts as verified once `make lab-redeploy` passes from a clean deploy.
- **Docs:** the website's build fails on any broken link or anchor; `go test
  ./tools/doclinks` (part of `make test`) checks the links of `docs/`, the READMEs, and
  between them and the website.

## CI and releases

- **`ci.yaml`:** gofmt, vet and unit tests, then the whole lab on a GitHub runner
  (containerlab and FRR pinned there) with diagnostics on failure.
- **`release.yaml`:** a `v*` tag runs goreleaser, which produces static linux/amd64+arm64
  binaries (with the systemd unit) and a multi-arch image
  `ghcr.io/<owner>/open-dci:<version>`.
- **`pages.yaml`:** builds the website on every push to `main` that touches it, and
  publishes it to GitHub Pages.
