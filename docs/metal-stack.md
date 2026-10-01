# open-dci in metal-stack

How open-dci could become part of [metal-stack](https://metal-stack.io) itself. Today the
gateway needs no metal-stack changes ([Installation](installation.md#metal-stack)): it's an
FRR box at the exits whose `open-dci.yaml` is written by hand. This page is a design
proposal for the next steps. None of it is implemented.

> [!NOTE]
> Statements about metal-stack internals (metal-api, metal-core, metal-networker,
> metal-roles) reflect the general architecture of metal-stack, not a review of its code.
> Check them against the version in use before building on them.

## Who does what

| metal-stack component | Role for open-dci |
|---|---|
| metal-api | a new **network stitch** entity (which private networks belong together); IPAM for the locators and loopbacks |
| `dci-controller` (new) | renders each gateway's `open-dci.yaml` from metal-api and ships it |
| metal-networker | renders the gateway's base FRR config, like a firewall's |
| metal-roles (Ansible) | the exits and the core: static, per partition |
| metal-core | unchanged |
| metalctl, cloud API | tenant UX: create and delete stitches |

## Gateways: a firewall flavor, the stitch a new entity

A gateway has a lot in common with a metal-stack firewall:
- a bare-metal machine with an image;
- an EVPN VTEP whose FRR base config is rendered from metal-api data;
- attached to the underlay, and optionally to a provider network: the DCI network, much
  like the internet network.

But it differs in a way that matters:
- A firewall belongs to one tenant project and that project's private network.
- A gateway is provider infrastructure shared by many tenants.
- Its tenant VRFs come and go at runtime without a new machine allocation. open-dci
  provisions them itself, so metal-api never attaches tenant networks to the machine.

So the proposal separates the machine from the intent.

**The machine: a firewall-like gateway role.**
- A pair per partition, allocated like firewalls but in a provider project, from a gateway
  image (the firewall image plus open-dci).
- Networks at allocation: the underlay and the DCI network.
- metal-networker renders the gateway's base config:
  - EVPN with `advertise-all-vni`
  - the DCI VRF as an L3VNI
  - the non-transit `only-self-out` filter
  - BFD towards both exits
  - `no zebra nexthop kernel enable`
  - the same ASN for both gateways of a pair
- metal-api's IPAM assigns the partition's locator (one `/48` from a provider block) and
  each gateway's loopback.

**The intent: a network stitch in metal-api.**
- It names the private networks, one per partition, that form one tenant network. It is
  scoped to a project; stitching across projects isn't possible by default.
- Its route target is derived from its ID ([day2.md](day2.md#deriving-route-targets-automatically)).
- This is the real new base entity. The gateways only execute it.

**The glue: a `dci-controller`**, in the spirit of the firewall-controller:
- It watches stitches and private networks.
- It renders each gateway's `open-dci.yaml`:
  - `vrf` and `vni` from the member network's VRF ID in that gateway's partition
  - `prefixes` from the prefixes of all member networks
  - `routeTarget` from the stitch
- It ships the file to both gateways of the partition; open-dci reconciles.
- It's also the one place with a global view for checks: route-target collisions,
  overlapping prefixes inside a stitch, a stitch whose partition has no gateways.

**Tenant UX**, for example:

```sh
metalctl network stitch create --name prod --network <network in partition A> --network <network in partition B>
```

A cloud API (e.g. a cluster spanning partitions) requests a stitch the same way.

## The exits: metal-roles, not metal-core

- **metal-core** manages the leaf switches' machine ports at runtime: VRF and VLAN per
  allocated machine. That's tenant-driven and dynamic.
- **What open-dci needs from the exits is static** ([Configuration](configuration.md#requirements-on-the-environment)):
  - VPN address families towards the gateways, with `allowas-in 1`
  - the relay between the exits (a ladder or route servers)
  - the DCI VRF or IPv6 towards the core
  - the edge ACLs of the SRv6 domain
  - BFD
- **It changes only when a partition joins:** the ladder neighbours. With route servers,
  not even then. A new tenant or stitch changes nothing on the exits; that's the point of
  the design.

That makes the exits a role in metal-roles, set up once per partition, like the rest of
the partition's static network.

## Gateways at the leaves: as servers yes, on the switches no

**As servers attached to leaves, like firewalls: possible, in DCI-network mode.**
- The DCI VRF reaches the exits as VXLAN through the fabric, so a gateway can sit at any
  leaf, and open-dci needs no change.
- Default-VRF mode doesn't fit: the locators would have to be routed in the fabric's
  underlay, which the design rules out.
- Costs:
  - stitched traffic crosses the fabric twice (leaf → gateway's leaf → exit), so the fabric
    needs the capacity;
  - every fabric link on the way must fit tenant MTU + 98 B;
  - the edge of the SRv6 domain stays at the exits: outside the DCI VRF, nothing can reach
    the locators.
- Benefits: the most metal-stack-native placement. The gateways use ordinary rack slots and
  the same cabling and provisioning as firewalls.
- Gateways at the exits, as in the lab, remain the bandwidth-optimal variant.

**On the leaf switches themselves: no**, for the reasons in the README
([Why not on the switches?](../README.md#design-decisions)):
- SRv6 VPN support in switch ASICs and SONiC is limited.
- VRF, next-hop and tunnel tables are fixed in size.
- Per-tenant DCI state would live in every leaf.
- It would couple the stitching to metal-core's tenant handling.

## Rollout

1. **Today:** a static `open-dci.yaml` per gateway, written by hand
   ([Installation](installation.md#metal-stack)).
2. **A shared inventory file** distributed to all gateways
   ([day2.md](day2.md#keeping-the-locations-in-sync)).
3. **The `dci-controller`** renders the configs from metal-api, first from labelled
   private networks ([day2.md](day2.md#deriving-route-targets-automatically)), then from the
   network stitch entity.
4. **The gateway role** in metal-api and metal-networker, and the exit role in metal-roles.

## Open questions

- **Lifecycle:** what happens to a stitch when a member network is deleted? Probably the
  member drops out and the stitch stays as long as two members are left.
- **Quotas:** stitches and stitched prefixes per project.
- **Permissions:** who may stitch networks, and whether stitching across projects is ever
  allowed (not by default).
- **Several metal-api instances:** route targets must be unique across them, e.g. by hashing
  `<instance>/<project>/<stitch>`.
- **Capacity:** placing tenants on gateway groups (sharding) when one pair isn't enough
  ([Capabilities](capabilities.md#scaling-bandwidth)).
