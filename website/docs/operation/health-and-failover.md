# Health and failover

A partition's two gateways share their locator and SIDs (anycast), so either one can carry
any flow. open-dci makes sure that a gateway only attracts traffic it can actually deliver,
and the other one takes over otherwise. Nothing outside the partition has to react.

## A gateway announces itself only when ready

After a restart or when its links come back, a gateway's tenant VRFs are empty until the
fabric's routes have arrived. Until then open-dci **withholds** the locator, so the partner
keeps all traffic. As soon as the fabric has sent its routes, the gateway announces its
locator again. `status` shows `locator WITHHELD` with the reason meanwhile.

## An unhealthy gateway withdraws itself

A gateway can be broken while all its BGP sessions are up: a tenant VRF that isn't bound to
its VNI, a SID missing in the kernel, routes that FRR never installed, or no route to any
remote partition. `open-dci run` checks this every 2 seconds. After two failed checks the
gateway **withdraws** like a [drain](maintenance.md), and announces again once it is healthy.
`status` and [`/healthz`](metrics.md) say why.

A network that is just being added doesn't count until it works for the first time, so
adding a network never makes a gateway withdraw.

## What a failure costs

Gateways and exits keep no per-flow state, so a failure never resets a TCP connection; it
only loses packets until routing has moved the traffic. Measured in the lab with BFD on
every session of gateways and exits:

| Failure | Packet loss |
|---|---|
| Gateway or exit loses its links (crash, power, cable) | ~0.15 s |
| Gateway or exit hangs while its links stay up | ~1 s with BFD, ~7–8 s (the BGP hold time) without |
| A failed node comes back | under 0.3 s, at most ~3 s |

Run BFD on every session of the gateways and exits (see the
[requirements](../getting-started/requirements.md)): without it, a hung node is only noticed
after the BGP hold time. Method, per-flow results and BFD intervals:
[Failure measurements](https://github.com/mwindower/open-dci/blob/main/docs/performance.md).
