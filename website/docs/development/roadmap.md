# Roadmap

| Phase | Content | Status |
|---|---|---|
| 0 / 0b | Feasibility: [standalone](phase0-findings.md), [metal-stack firewall + DCI network](phase0b-findings.md) (the firewall placement was dropped later) | done |
| 1 | Tool MVP: config, validation, kernel, FRR render/diff/apply/reconcile, status, golden + e2e tests | done |
| 1.1 | Default-VRF transport, lab in CI, releases (binaries, image, systemd unit) | done |
| 2 | Robustness: type-5 routes without the VPN's extended communities (no DCI RT in the fabrics), site of origin (`gateway.siteOfOrigin`) kept out of the VPN, RT/RD/SoO local-part widths validated per admin type, default RD `<routerID>:<sid>` | done |
| 2.5 | Failure detection: BFD on the gateway's, exit's, spine's and core's sessions (base configs, [measured](../operation/failure-measurements.md)), locator withheld until End-of-RIB and withdrawn while unhealthy | done |
| 3 | Operations: Prometheus metrics and health endpoint (`run --listen`) | done |
| 3.1 | Multi-site config ([Adding partitions and networks](../operation/adding-partitions-and-networks.md)): shared inventory file, VPN route reflectors with `bgp listen range` | |
| 2.2 | Redundant gateway pairs: anycast locator, unique loopbacks, pinned SIDs (`networks[].sid`), failover tests | done |
| 2.3 | SRv6 domain edge: gateway ingress filter (nftables), exit edge ACLs in the lab, no fall-through in tenant VRFs, forged-packet tests | done |
| 2.4 | No full mesh: gateways peer only with their exit (`peers[].interface`), the exits relay the VPN routes | done |
| 2.1 | Safety net: per-network prefix allowlists (`prefixes`), inbound route-target filter and `maxPrefixes` per peer | done |
| 3.2 | Dedicated gateways: provisioned tenant L3VNIs (`networks[].vni`), lab gateways at the exits; the firewall placement removed | done |
| 3.3 | Scale test for dedicated gateways: number of provisioned VRFs, packets per second | |
| 4 | metal-stack integration, step 1: networks from metal-apiserver (`source.metalStack`: labelled networks, RTs derived from project and label value, cached last answer, label changes without interruption), no new metal-stack entities; fake API in the lab | done |
| 4.1 | metal-stack integration, step 2: a network stitch entity, `dci-controller` and gateway role ([proposal](../metal-stack/integration-proposal.md)) | |
