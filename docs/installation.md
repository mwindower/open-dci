# Installation

`srv6-dci` runs on the gateway itself, next to FRR: on a metal-stack firewall, or any other
FRR-based EVPN VTEP. Write the [configuration](configuration.md) first, then pick one of the
following.

## Binary + systemd

Static Linux binaries (amd64, arm64) are attached to each
[GitHub release](https://github.com/mwindower/srv6-dci/releases), together with the systemd
unit [`deploy/systemd/srv6-dci.service`](../deploy/systemd/srv6-dci.service). Alternatively:
`go install github.com/mwindower/srv6-dci/cmd/srv6-dci@latest`.

```sh
install -m 0755 srv6-dci /usr/local/bin/
install -D -m 0644 config.yaml /etc/srv6-dci/config.yaml
srv6-dci validate -c /etc/srv6-dci/config.yaml
srv6-dci render   -c /etc/srv6-dci/config.yaml     # review what will be added
cp srv6-dci.service /etc/systemd/system/ && systemctl enable --now srv6-dci
srv6-dci status   -c /etc/srv6-dci/config.yaml
```

## Container

`ghcr.io/mwindower/srv6-dci` is based on the FRR image, for `vtysh`. It must share the host's
network namespace and FRR's sockets:

```sh
docker run -d --name srv6-dci --network host --privileged \
  -v /var/run/frr:/var/run/frr -v /etc/frr:/etc/frr:ro \
  -v /etc/srv6-dci:/etc/srv6-dci:ro -v /var/lib/srv6-dci:/var/lib/srv6-dci \
  ghcr.io/mwindower/srv6-dci
```

Keep the image's FRR major version in line with the FRR it talks to.

## metal-stack

On a metal-stack firewall, two things are needed besides srv6-dci itself:
- **DCI network mode:** the DCI network is a metal-stack network attached to the firewall.
  That creates its VRF and VNI (via metal-networker) and lets the leaves send its VNI to the
  firewall (via metal-core's `match evpn vni` route-map).
- **The exits** route the DCI network, or the IPv6 underlay in default-VRF mode, to the
  other partitions.

The [lab](../lab/README.md) reproduces exactly this setup.
