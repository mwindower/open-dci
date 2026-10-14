# Installation

`open-dci` runs on the gateway itself, next to FRR: a dedicated box at the exit of an EVPN
fabric. Write the [configuration](../configuration/overview.md) first, then pick one of the following.

## Binary + systemd

Static Linux binaries (amd64, arm64) are attached to each
[GitHub release](https://github.com/mwindower/open-dci/releases), together with the systemd
unit [`deploy/systemd/open-dci.service`](https://github.com/mwindower/open-dci/blob/main/deploy/systemd/open-dci.service). Alternatively:
`go install github.com/mwindower/open-dci/cmd/open-dci@latest`.

```sh
install -m 0755 open-dci /usr/local/bin/
install -D -m 0644 config.yaml /etc/open-dci/config.yaml
open-dci validate -c /etc/open-dci/config.yaml
open-dci render   -c /etc/open-dci/config.yaml     # review what will be added
cp open-dci.service /etc/systemd/system/ && systemctl enable --now open-dci
open-dci status   -c /etc/open-dci/config.yaml
curl -s localhost:9469/healthz                     # the unit runs with --listen 127.0.0.1:9469
```

The unit serves Prometheus metrics and the health on `127.0.0.1:9469` (`/metrics`,
`/healthz`, see [Operation](../operation/metrics.md)). To scrape it from a
Prometheus server, change `--listen` to a management address.

## Container

`ghcr.io/mwindower/open-dci` is based on the FRR image, for `vtysh`. It must share the host's
network namespace and FRR's sockets:

```sh
docker run -d --name open-dci --network host --privileged \
  -v /var/run/frr:/var/run/frr -v /etc/frr:/etc/frr:ro \
  -v /etc/open-dci:/etc/open-dci:ro -v /var/lib/open-dci:/var/lib/open-dci \
  ghcr.io/mwindower/open-dci
```

The image runs `open-dci run` without `--listen`. For metrics and health, pass the command
after the image name: `... ghcr.io/mwindower/open-dci run -c /etc/open-dci/config.yaml --listen :9469`.

Keep the image's FRR major version in line with the FRR it talks to.
