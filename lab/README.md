# Lab

A 25-node [containerlab](https://containerlab.dev) lab with three metal-stack-like
partitions, each with two exits, an IPv6 core, two tenants and a redundant gateway pair per
partition whose gateways are attached to both exits. It is the integration test for
open-dci and runs in CI.

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

Every gateway has one uplink to each exit of its partition (`uplink0` → exit-x1,
`uplink1` → exit-x2).

Both tenants (m-a/m-b/m-c with VNIs 3981/4011/5011, m-a2/m-b2/m-c2 with VNIs
3982/4012/5012) are stitched by the gateway pairs at the exits, which provision their VRFs.

```sh
make lab-up        # build open-dci + labnode, deploy
make lab-check     # Go e2e tests (build tag e2e)
make lab-perf      # failure semantics under load (not part of lab-check)
make lab-capture   # SRv6-in-VXLAN between gw-a1 and exit-a1
make lab-down
```

Requirements:
- Linux with `vrf`/`vxlan`/SRv6
- Docker
- containerlab (SUID-root or root; in CI: `make lab-up CLAB="sudo containerlab"`)
- Go ≥ 1.27
- the image `quay.io/frrouting/frr:10.4.1`

What the lab models, every e2e test, and the routing tables of all nodes:
[Lab](https://mwindower.github.io/open-dci/docs/lab/overview) on the website (source:
[`website/docs/lab`](../website/docs/lab)).
