# Try it in the lab

The repository contains a complete [containerlab](https://containerlab.dev) lab: three
metal-stack-like partitions with two exits each, an IPv6 core, two tenants, and a
redundant gateway pair per partition running open-dci. It is open-dci's integration test
and runs in CI.

```
  partition A (DCI network)    partition B (underlay)     partition C (underlay)
  m-a, m-a2 ── leaf-a          m-b, m-b2 ── leaf-b        m-c, m-c2 ── leaf-c
                │                            │                          │
             spine-a                      spine-b                    spine-c
             ╱     ╲                      ╱     ╲                    ╱     ╲
       exit-a1   exit-a2            exit-b1   exit-b2          exit-c1   exit-c2
           │ ╲   ╱ │                    │ ╲   ╱ │                  │ ╲   ╱ │
       gw-a1   gw-a2                gw-b1   gw-b2              gw-c1   gw-c2
                      all six exits ── core (IPv6 only)
```

You need Linux, Docker, [containerlab](https://containerlab.dev) and Go. Then:

```sh
git clone https://github.com/mwindower/open-dci && cd open-dci
make lab-up        # build open-dci, deploy the 25 nodes
make lab-check     # the end-to-end tests
docker exec clab-open-dci-gw-a1 open-dci status -c /etc/open-dci/config.yaml
docker exec clab-open-dci-m-a ping 10.0.32.10      # tenant 1, partition A → B
make lab-down
```

The gateways' configs are in `lab/configs/gw-*/open-dci.yaml`, a good starting point for
your own. Tenant 2 comes from a fake metal-apiserver, as
[networks from metal-apiserver](../configuration/metal-apiserver.md) would.

What the lab models, all its tests and every node's routing table:
[the lab](https://github.com/mwindower/open-dci/blob/main/docs/lab.md) in the repository.
