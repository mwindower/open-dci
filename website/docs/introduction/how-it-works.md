# How it works

![Logical view: three partitions, each with its own private network and VNI per tenant. Tenant 1 has 10.0.16.0/24 with VNI 3981 in A, 10.0.32.0/24 with VNI 4011 in B and 10.0.48.0/24 with VNI 5011 in C; tenant 2 has 10.0.17.0/24 (VNI 3982), 10.0.33.0/24 (VNI 4012) and 10.0.49.0/24 (VNI 5012). The gateway pair of each partition exports every tenant VRF with its own SRv6 SID; SRv6 L3VPN joins each tenant's networks into one routed network (route targets 65535:1001 and 65535:1002). Tenants stay separate.](../../static/img/logical-view.svg)

How a packet travels (per partition, the two exits, the gateway pair and the fabric are one box each):

![Animated packet flow across three partitions joined by the DCI network, an IPv6-only core. Each partition shows its two exits, its redundant gateway pair (attached to both exits) and its fabric (spines, leaves, machines) as one box each. A tenant packet comes from partition A's fabric over VXLAN with VNI 3981 via either exit to either gateway of pair A, which provisioned the tenant VRF. The gateway encapsulates it in SRv6 to partition B's anycast SID and sends it in partition A's EVPN VRF for the transport (VNI 104100) to either exit, into the DCI network. Partition B's exits deliver it to either gateway of pair B, which decapsulates it (End.DT46) and forwards it with partition B's VNI 4011 via the exits into partition B's fabric. Partition C works alike.](../../static/img/packet-flow.svg)

Between partitions only IPv6 is needed: exits and core see one locator prefix per gateway,
and nothing of tenants or VNIs. The SRv6 transport runs in one of two modes, which can be
mixed freely:

| Mode | SRv6 transport | Isolation from the fabric | Fits |
|---|---|---|---|
| DCI network (`transport.vrf`), **recommended** | in an EVPN VRF of the base config, over VXLAN through the fabric | by construction: the VRF only exists on gateways and exits | any setup, especially fabrics with untrusted devices in the underlay (e.g. tenant firewalls) |
| Default VRF | in the IPv6 underlay | by configuration: the exits must announce locators only to gateways and core, and filter the edge | gateways with their own routed uplink; no VXLAN overhead (−50 B) and no veth |

## What the network sees

A tenant packet on the wire from gw-a1 to exit-a1 (DCI network mode, `make lab-capture`):

```
IP 10.0.0.16 > 10.0.0.14.4789: VXLAN vni 104100                   ← gw-a1 → exit-a1, DCI network
  IP6 fd00:dc1:ff::a1 > fd00:dc1:b:fab::: RT6 (type=4, segleft=0)  ← SRv6 to pair B's anycast SID
    IP 10.0.16.10 > 10.0.32.10: ICMP echo request                  ← tenant packet
```

Which node knows which routes, for both modes: [Routing tables](https://github.com/mwindower/open-dci/blob/main/docs/lab-routing.md).
