# Developer documentation

The user documentation is the website, [mwindower.github.io/open-dci](https://mwindower.github.io/open-dci/)
(sources in [`website/docs`](../website/docs)). This directory holds what is needed to work
*on* open-dci: how it works inside, why it is built this way, and what was measured.

| | |
|---|---|
| [Internals](internals.md) | what open-dci changes in the kernel and FRR (with the rendered config), readiness, health checks and failure semantics in detail, the ingress filter, route targets and the fabrics |
| [Requirements in detail](requirements.md) | the reasons behind the requirements, exit topologies, per-mode details |
| [Design decisions in detail](design-decisions.md) | the full reasoning, incl. why not on the switches |
| [Scaling](scaling.md) | scale limits and how bandwidth grows |
| [dci-controller](metal-stack-controller.md) | the controller round, how networks, SIDs and route targets are derived, label changes in detail |
| [Failure measurements](performance.md) | packet loss and TCP stalls when a gateway or an exit fails, BFD intervals |
| [Lab](lab.md) | what the containerlab lab models, and every e2e test |
| [Routing tables](lab-routing.md) | which node knows which routes, in both modes |
| [Development](development.md) | layout, tests, CI, releases |
| [Roadmap](roadmap.md) | phases, and how the configs could be kept in sync |
| [metal-stack integration proposal](metal-stack-proposal.md) | a gateway role, a network stitch entity, a controller |
| [Alternatives](alternatives.md) | other products and Cilium for EVPN ↔ SRv6 stitching |
| [Phase 0](phase0-findings.md), [Phase 0b](phase0b-findings.md) | feasibility findings: EVPN ↔ SRv6, the DCI network design |

The website's build checks its own links; `go test ./tools/doclinks` checks the links of
these files, the READMEs, and the links between them and the website.
