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
- Ways to fail:
  - **link:** all links of the node go down, so its neighbours see the loss of carrier at
    once (crash, power loss, pulled cable).
  - **hung:** the node silently drops everything (nftables, prerouting and output), and its
    links stay up (frozen kernel, broken NIC firmware, a one-way link). The neighbours only
    notice when BFD or the BGP hold timer expires.
  - **drained:** planned maintenance, `open-dci drain` 3 s before the links go down.
  - **gray:** BGP and BFD are fine, but the gateway can't forward. Kernel `unreachable`
    routes hide every remote locator from it. Only open-dci's health check notices
    ([Operation](operation.md#withdrawing-an-unhealthy-gateway)).
- Timers: the sessions between gateways and exits, and between exits and spines, use
  metal-stack's `timers 2 8` (keepalive 2 s, hold 8 s). All other sessions use FRR's
  `datacenter` defaults (3 s, 9 s).
- Four setups, each adding to the one before:
  1. **no BFD**
  2. **BFD on gateway ↔ exit and exit ↔ core** (`bfd profile dci`: 300 ms × 3, so detection
     in ≤ 0.9 s)
  3. **BFD on every session of gateways and exits**: setup 2 plus exit ↔ spine
  4. **plus clean returns**: open-dci withholds the locator until the gateway is ready
     ([Operation](operation.md#announcing-the-locator)), and the FRR nodes run without
     kernel nexthop groups (`no zebra nexthop kernel enable`, see below). This is the lab's
     configuration.
- FRR: setups 1–3 ran on 10.6.0, setup 4 on 10.4.1, which the lab pins (10.5–10.7 don't
  withdraw leaked routes, see [Configuration](configuration.md#requirements-on-the-environment)).
  10.4.1 ignores BFD on the core's sessions to the exits, so the core uses a 3 s hold time
  there (`timers 1 3`) as a fallback.
- The failed exit is the one spine-b forwards leaf-b's VXLAN traffic through, so every
  direction of every flow crosses it.
- Caveats:
  - The lab runs as containers on one host, so absolute throughput means nothing. The
    timings come from the protocols (carrier detection, hold timers, FRR route
    installation) and carry over to real hardware.
  - ECMP hashing decides which flows a failed gateway carries. In these runs, the m-c ↔ m-b
    flows went through gw-b1.

## Results

Loss of the affected flows on failure (and on return), and the longest TCP stall, per setup.

| Failure | no BFD | BFD gateway ↔ exit, exit ↔ core | BFD on every session | + clean returns |
|---|---|---|---|---|
| gateway, link | 0.16 s (return ≤ 1.5 s), TCP 1.4 s | 0.16 s (return 1.8 s), TCP 3.0 s | 0.17 s (return 2.3 s), TCP 0.8 s | 0.26 s (return 0 s), TCP 0.4 s |
| gateway, hung | **6.4 s**, TCP **12.6 s** | 0.8 s, TCP 1.2 s | 1.0 s, TCP 1.2 s | 1.0 s (return 0 s), TCP 1.4 s |
| gateway, drained, then down | – | – | – | **0 s** (return 0 s), TCP 0 s |
| gateway, gray failure | – | – | – | 2.8 s (return 0 s), TCP 2.8 s |
| exit, link | 0 s (return 1.9–3.2 s), TCP 6.2 s | 0.12 s (return 1.3–2.3 s), TCP 3.0 s | 0.12 s (return 1.9–2.1 s), TCP 2.8 s | 0.18 s (return 0 s), TCP 0 s |
| exit, hung | **7.9 s, all flows**, TCP **12.8 s** | **7.9 s, all flows**, TCP **12.6 s** | 0.85 s, all flows, TCP 1.2 s | 0.9 s, all flows, TCP 1.4 s |

- No TCP connection broke in any run (16 per scenario).
- In the "link" rows, the TCP stalls come from the return, not the failure.

Per flow, in the lab's configuration (setup 4, FRR 10.4.1). Loss is shown as "on failure /
on return", in seconds. In this run the m-a ↔ m-b and tenant 2 flows were the ones hashed
to gw-b2.

| Flow | gateway, link | gateway, hung | gateway, drained | exit, link | exit, hung |
|---|---|---|---|---|---|
| m-a → m-b | 0.26 / 0 | 0.96 / 0 | 0 / 0 | 0.18 / 0 | 0.90 / 0 |
| m-c → m-b | 0 / 0 | 0 / 0 | 0 / 0 | 0.18 / 0 | 0.90 / 0 |
| m-b → m-a | 0.26 / 0 | 0.98 / 0 | 0 / 0 | 0.18 / 0 | 0.88 / 0 |
| m-b → m-c | 0 / 0 | 0 / 0 | 0 / 0 | 0.18 / 0 | 0.88 / 0 |
| m-a2 → m-b2 | 0.26 / 0 | 0.96 / 0 | 0 / 0 | 0.18 / 0 | 0.86 / 0 |
| m-b2 → m-a2 | 0.26 / 0 | 0.96 / 0 | 0 / 0 | 0.18 / 0 | 0.86 / 0 |
| TCP m-a → m-b, longest stall | 0.4 | 1.4 | 0 | 0 | 1.4 |
| TCP m-c → m-b, longest stall | 0 | 0.4 | 0 | 0 | 1.4 |

## Reading the results

- **No TCP connection broke.** Gateways and exits keep no per-flow state, so every lost
  packet is retransmitted. The only question is how long a flow stalls.
- **Link failures are cheap,** with or without BFD. Losing carrier tears down the BGP
  session at once, and FRR removes the next hop within ~150 ms.
- **Without BFD, a hung node costs a hold time.** The loss lasts about as long as the 8 s
  hold timer, minus the time since the last keepalive.
- **TCP stalls about twice as long as the loss.** TCP's retransmission timeout doubles with
  each attempt (200 ms, 400 ms, …). After a 7 s outage, the next attempt comes at about
  12.6 s.
- **BFD cuts a hung node to under a second.** Detection takes ≤ 0.9 s (300 ms × 3), and
  TCP then stalls ~1.2 s instead of ~13 s.
- **BFD only helps on every neighbour.** With BFD on gateway ↔ exit and exit ↔ core only, a
  hung exit still lost 7.9 s on all flows: the spine doesn't run BFD towards it, keeps
  forwarding the fabric's traffic into it, and only stops when its hold timer expires.
  Every session of a node that can hang needs BFD.
- **A gateway only affects its share of the flows.** The flows hashed to gw-b1 (m-c ↔ m-b)
  lost nothing when gw-b2 failed.
- **An exit affects the whole partition.** Leaves, spines, gateways and the core all spread
  over both exits.
- **Returns were the last big loss, and had two causes:**
  - **The returning gateway announced its anycast locator as soon as its sessions were
    up.** The exits sent it traffic it decapsulated into tenant VRFs that didn't hold the
    fabric's routes yet. open-dci now waits for the EVPN End-of-RIB.
  - **FRR's zebra revived withdrawn next hops** (10.4.1 and 10.6.0). On link-down, zebra keeps reusing the
    kernel nexthop group, with the dead next hop inside, for the route BGP re-sent
    without it. On link-up it reactivates that next hop before BGP has a path through it,
    so exits, spines and core sent traffic to a neighbour that wasn't ready yet.
    `no zebra nexthop kernel enable` avoids it.

  Together they took the return from up to 3 s down to 0 s. A hung node returns
  without loss either way: its sessions come back with its routing tables intact.
- **A gray failure costs the health check's detection time.** Two failed checks 2 s apart,
  then the withdrawal: ~2.8 s for the flows through that gateway. Without the check they
  would be lost for as long as the failure lasts.
- **Planned maintenance costs nothing.** `open-dci drain` before taking a gateway down
  moves all traffic to its partner while both are up; no packet was lost.
- **Nothing outside partition B reacted.** With an anycast locator and SIDs, the remote
  gateways' encapsulation routes stay the same during a failure.

## Reproduce

```sh
make lab-up
make lab-perf      # ~4 min; logs one table per scenario
```
