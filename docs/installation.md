# Installation

`open-dci` runs on the gateway itself, next to FRR: a dedicated box at the exit of an EVPN
fabric. Write the [configuration](configuration.md) first, then pick one of the following.

## Binary + systemd

Static Linux binaries (amd64, arm64) are attached to each
[GitHub release](https://github.com/mwindower/open-dci/releases), together with the systemd
unit [`deploy/systemd/open-dci.service`](../deploy/systemd/open-dci.service). Alternatively:
`go install github.com/mwindower/open-dci/cmd/open-dci@latest`.

```sh
install -m 0755 open-dci /usr/local/bin/
install -D -m 0644 config.yaml /etc/open-dci/config.yaml
open-dci validate -c /etc/open-dci/config.yaml
open-dci render   -c /etc/open-dci/config.yaml     # review what will be added
cp open-dci.service /etc/systemd/system/ && systemctl enable --now open-dci
open-dci status   -c /etc/open-dci/config.yaml
```

## Container

`ghcr.io/mwindower/open-dci` is based on the FRR image, for `vtysh`. It must share the host's
network namespace and FRR's sockets:

```sh
docker run -d --name open-dci --network host --privileged \
  -v /var/run/frr:/var/run/frr -v /etc/frr:/etc/frr:ro \
  -v /etc/open-dci:/etc/open-dci:ro -v /var/lib/open-dci:/var/lib/open-dci \
  ghcr.io/mwindower/open-dci
```

Keep the image's FRR major version in line with the FRR it talks to.

## metal-stack

The gateway needs no metal-stack changes. It peers EVPN with the exit like any fabric
member, and open-dci provisions the tenant VRFs from its config: each tenant's VNI in that
partition (the VRF ID of the tenant's metal-stack private network) as `networks[].vni`.
- The fabric must pass these VNIs' type-5 routes between leaves and gateway.
- **The exits** route the DCI network, or the IPv6 underlay in default-VRF mode, to the
  other partitions.

The [lab](../lab/README.md) reproduces this setup.
