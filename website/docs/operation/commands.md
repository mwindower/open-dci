# Commands

```sh
open-dci validate -c config.yaml        # check the config
open-dci render   -c config.yaml        # FRR lines open-dci adds (--asn/--router-id to render offline)
open-dci diff     -c config.yaml        # what is missing in the running FRR, and what is stale
open-dci apply    -c config.yaml        # reconcile kernel + FRR once
open-dci run      -c config.yaml -i 10s # reconcile continuously (service / sidecar)
open-dci run      -c config.yaml --listen :9469   # ... and serve /metrics and /healthz
open-dci status   -c config.yaml        # exits non-zero if not healthy
open-dci status   -c config.yaml --routes vrf3981   # plus the routes of a network (VRF, VNI or all)
open-dci drain    -c config.yaml        # planned maintenance: the partner takes over
open-dci undrain  -c config.yaml        # announce again
open-dci version
```

Common flags:

| Flag | Default |
|---|---|
| `--vtysh` | `vtysh` |
| `--state` | `/var/lib/open-dci/applied.conf` |
| `--refresh` | off: with a [network source](../configuration/metal-apiserver.md), `render`, `diff`, `apply` and `status` use its last answer (cached by `run`); `--refresh` polls it first |
| `-v` | off (debug logging) |

`run` is meant to run permanently, as a service or as a sidecar next to FRR. It retries until
FRR is up, and it re-applies its configuration whenever the base system (e.g. a
configuration management running `frr-reload.py`) removes it. In the lab, that takes one
interval. With `--listen` it serves [metrics and its health](metrics.md) over HTTP.
