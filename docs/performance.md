# Failure measurements

What tenant traffic experiences when a gateway or an exit fails, measured in the
[lab](../lab/README.md) with `make lab-perf` (`lab/perf_test.go`). For the reasoning behind
the behaviour, see [Operation](operation.md#failure-semantics).

## Method

- While traffic runs between the partitions, partition B's gw-b2 or exit-b1 fails at
  t = 10 s and returns at t = 27 s; one run lasts 45 s.
- Traffic:
  - pings every 20 ms on six flows: m-a, m-c → m-b; m-b → m-a, m-c; m-a2 → m-b2;
    m-b2 → m-a2
  - two iperf3 clients (m-a and m-c → m-b), each with 8 TCP streams at 20 Mbit/s,
    reporting every 200 ms
- Results:
  - **Loss:** the longest run of lost pings around the failure and around the return. One
    lost ping is 20 ms, so the resolution is ±20 ms.
  - **TCP stall:** the longest time a stream moved less than 1 Mbit/s, at 200 ms resolution.
- Two ways to fail:
  - **link:** all links of the node go down, so its neighbours see the loss of carrier at
    once (crash, power loss, pulled cable).
  - **hung:** the node silently drops everything (nftables, prerouting and output), and its
    links stay up (frozen kernel, broken NIC firmware, a one-way link). The neighbours only
    notice when the BGP hold timer expires.
- Timers: the sessions between gateways and exits (and between exits and spines) use
  metal-stack's `timers 2 8` (keepalive 2 s, hold 8 s). All other sessions use FRR's
  `datacenter` defaults (3 s, 9 s). No BFD.
- Caveats:
  - The lab runs as containers on one host, so absolute throughput means nothing. The
    timings come from the protocols (carrier detection, hold timers, FRR route
    installation) and carry over to real hardware.
  - ECMP hashing decides which flows a failed gateway carries. In these runs, the m-c ↔ m-b
    flows went through gw-b1.

## Results

One run of `make lab-perf`. Loss is shown as "on failure / on return", in seconds.

| Flow | gateway, link | gateway, hung | exit, link | exit, hung |
|---|---|---|---|---|
| m-a → m-b | 0.16 / 0.54 | 7.50 / 0.30 | 0.12 / 1.90 | 6.88 / 0 |
| m-c → m-b | 0 / 0 | 0 / 0 | 0 / 1.12 | 6.88 / 0 |
| m-b → m-a | 0.16 / 0.54 | 7.52 / 0.30 | 0.12 / 1.90 | 6.90 / 0 |
| m-b → m-c | 0 / 0 | 0 / 0 | 0 / 1.12 | 6.92 / 0 |
| m-a2 → m-b2 | 0.16 / 0.54 | 7.50 / 0.30 | 0.14 / 1.90 | 6.88 / 0 |
| m-b2 → m-a2 | 0.16 / 0.54 | 7.50 / 0.30 | 0.14 / 1.90 | 6.90 / 0 |
| TCP m-a → m-b, longest stall | 0.2 | 13.0 | 2.8 | 12.8 |
| TCP m-c → m-b, longest stall | 0.4 | 1.2 | 2.8 | 12.8 |
| TCP connections broken | 0 / 16 | 0 / 16 | 0 / 16 | 0 / 16 |

Across repeated runs:
- Loss on failure is stable: link failures cost 0.12–0.18 s, hung nodes 6.9–8.5 s.
- Loss on return varies: up to 1.8 s for the gateway, 1.1–2.0 s for the exit.

## Reading the results

- **No TCP connection broke.** Gateways and exits keep no per-flow state, so every lost
  packet is retransmitted. The only question is how long a flow stalls.
- **Link failures are cheap.** About 150 ms: losing carrier tears down the BGP session at
  once, and FRR removes the next hop.
- **Hung nodes cost a hold time.** The loss lasts about as long as the 8 s hold timer, minus
  the time since the last keepalive.
- **TCP stalls about twice as long as the loss.** TCP's retransmission timeout doubles with
  each attempt (200 ms, 400 ms, …). After a 7 s outage, the next attempt comes at about
  12.6 s, which matches the 12.8–13.0 s stalls.
- **A gateway only affects its share of the flows.** The flows hashed to gw-b1 (m-c ↔ m-b)
  lost nothing when gw-b2 failed. The 1.2 s stall of one m-c stream is the exception; its
  pings lost nothing.
- **An exit affects the whole partition.** Leaves, spines, gateways and the core all spread
  over both exits, so every flow uses both.
- **The return loses packets too.** When links come back, the returning node attracts
  traffic for up to 2 s before all its routes are in place. A hung node that returns gets
  its sessions back with its routing tables still intact, and loses next to nothing. The
  exact cause, and a remedy such as delaying advertisements on startup, is still open.
- **Nothing outside partition B reacted.** With an anycast locator and SIDs, the remote
  gateways' encapsulation routes stay the same during a failure.

## Reproduce

```sh
make lab-up
make lab-perf      # ~4 min; logs one table per scenario
```
