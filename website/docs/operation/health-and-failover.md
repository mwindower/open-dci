# Health and failover

## Announcing the locator

A gateway announces its locator and loopback only when it can deliver what they attract.
After its EVPN sessions come up, the tenant VRFs are empty until the fabric's type-5 routes
have arrived. A locator announced earlier makes the exits send the gateway packets that
are decapsulated into an empty VRF and dropped. Its anycast partner carries the traffic
in the meantime.

- **Ready:** at least one established EVPN session has delivered its initial table, marked
  by End-of-RIB. FRR sends End-of-RIB by default (graceful-restart helper mode); a session
  without it counts as ready after 30 s.
- **Withheld:** until then, and whenever no EVPN session is established, open-dci leaves
  out the announcement:
  - DCI-network mode: `redistribute static` in the transport VRF's BGP instance
  - default-VRF mode: the `network` statements

  The SIDs, the VRFs and everything else stay configured.
- `run` checks readiness every 500 ms and acts once a change has held for 1.5 s. A link
  flap thus withdraws the locator before the sessions are back (unnumbered sessions wait
  for router advertisements, ~3 s). If FRR can't be queried, or answers without any
  neighbors (e.g. while the base system reloads it), the last state stays.

In the lab, this cut the loss when a gateway's links return from 1.5–2.3 s to under
0.1 s without load ([measurements](failure-measurements.md)).

## Withdrawing an unhealthy gateway

A gateway can fail in ways neither BGP nor BFD notice: its sessions are up, but it can't
forward. `run` checks every 2 s:
- every network's L3VNI is `Up` (fabric side)
- every network's SID is installed in the kernel as End.DT46 (decapsulation)
- no tenant VRF has prefixes with valid paths but no best path
- at least one remote SID the tenant VRFs encapsulate to has a usable route: one inside
  the locator block, not a blackhole or `unreachable`. In DCI-network mode the transport
  VRF's table is checked. If only some remote SIDs are unreachable, that's a remote
  partition's problem, and withdrawing would only spread it.

After two failed checks in a row, the gateway withdraws like a drain: no locator, no type-5
routes, and its anycast partner carries everything. After three good checks it announces
again. `status` shows `health UNHEALTHY: <reason>` and exits non-zero; the log has
`locator withdrawn reason=...`.

A network counts for the withdrawal only once it has passed the checks: a network that is
still being provisioned fails them for its first seconds (L3VNI not up yet, no SID yet), and
both gateways of a pair provision a new network at the same time. Withdrawing for that would
take the partition down whenever a network is added; `status` shows the problem instead. A
network that leaves the config and comes back starts over.

`apply` and `status` run the checks too (every network counts there), but only `run`
withdraws.

## Failure semantics

Gateways and exits keep no per-flow state: no NAT, no connection tracking (the ingress
filter is stateless), no sequence numbers. Any surviving gateway or exit can carry any
packet of any flow. A failure therefore never resets a TCP connection; it only loses
packets until the routing has removed the failed node. TCP retransmits them. Which flows
are affected depends on ECMP hashing: a failed gateway only hits the flows hashed to it
(roughly half of its partition's), a failed exit hits every flow of the partition, since
leaves, spines, gateways and the core all spread over both exits.

Nothing outside the partition reacts to a gateway failure: both gateways own the same
locator and SIDs (anycast), so remote gateways keep their encapsulation routes unchanged.
The exits only drop the failed gateway's next hop for the locator, and the leaves drop it
for the tenant prefixes (EVPN type-5).

How long packets are lost depends on how the failure is detected. Measured in the lab
(`make lab-perf`: pings every 20 ms and 2 × 8 TCP streams between partitions while gw-b2 or
exit-b1 fails, then returns; per-flow results and method in
[Failure measurements](failure-measurements.md)):

| Failure | Detected by | Loss without BFD | Loss with BFD (lab) | Return |
|---|---|---|---|---|
| Gateway loses its links (crash, power, cable) | link down at the exits | ~0.15 s | ~0.15 s | ≤ 2.3 s loss |
| Gateway hangs, links stay up | BGP hold timer (8 s) or BFD | ~6.5–7.5 s, TCP stalls ~13 s | ~1 s, TCP ~1.2 s | ≤ 0.3 s loss |
| Exit loses its links | link down at its neighbours | ≤ 0.15 s | ≤ 0.15 s | ≤ 3 s loss |
| Exit hangs, links stay up | BGP hold timers (8–9 s) or BFD | ~7–8 s, all flows of the partition | ~0.9 s, TCP ~1.2 s | none |

- **TCP stalls longer than the loss** because of exponential retransmission backoff: after
  a 7.5 s outage, the next retransmission comes at ~12.6 s (200 ms RTO doubled per
  attempt). Connections survive; a connection only gives up after ~15 min
  (`tcp_retries2`), but applications with shorter timeouts may not.
- **A silent failure costs a hold time,** unless BFD detects it. The lab runs BFD (300 ms ×
  3) on every session of gateways and exits: gateway ↔ exit, exit ↔ spine, exit ↔ core.
  All of them are needed: a neighbour without BFD keeps forwarding into a hung node until
  its own hold timer expires. BFD is part of the base configs (and the exit switches);
  open-dci doesn't render it.
- **Recovery also loses packets** for a moment when a node's links come back: the returning
  node attracts traffic before all its routes are in place. The cause isn't analysed in
  detail yet; a hung node that returns, whose sessions come back with routes already in
  its tables, loses next to nothing.
