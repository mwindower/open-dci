# Security

open-dci joins tenant networks across partitions. The same mechanisms that join them could
join the wrong ones, so it matters who can reach what. This page lists the trust
boundaries and what you have to do at each one. The full analysis is in the developer
documentation: [Security analysis](https://github.com/mwindower/open-dci/blob/main/docs/security.md).

## Who is trusted

| | Trusted with | Why |
|---|---|---|
| Gateways, exits and core (the SRv6 domain) | every tenant's traffic | a SID decapsulates whatever reaches it; there is no authentication in SRv6 |
| The gateways and exits of *all* partitions | every stitched network's routes | a gateway imports what any partition announces with the network's route target, within the network's prefixes |
| Whoever holds the gateway API token | every tenant | an API network can join any VNI of the partition to any route target |
| Tenants | their own networks only | the fabric, the edge filters and the controller keep them there |

## Your checklist

**The SRv6 domain's edge**
- Configure the exits' ACLs from [the SRv6 domain and its edge](srv6-domain-edge.md). open-dci
  installs the gateway rules itself but can't check the exits.
- Set [`gateway.vxlanSources`](../configuration/gateway.md) to your fabric's VTEP range.
- Prefer the DCI-network transport: in default-VRF mode, the partition's underlay is part
  of the transport.

**The core**
- SRv6 and VXLAN carry tenant traffic unencrypted. If the links between your data centres
  cross anyone else's infrastructure, encrypt them underneath (MACsec, or IPsec).
- The core is inside the domain: whoever controls it can forge SRv6 with a source inside
  the locator block.

**The API**
- Serve it with TLS (`--api-tls-cert`, `--api-tls-key`), only on a management address,
  never on an address tenants can reach.
- Keep the token file readable by root only (mode `0600`), and treat the token like a key
  to every tenant.
- The gateway checks the token before it reads a request, limits requests to 4 MiB and
  closes slow or idle connections. An API network that the kernel or FRR already use for
  something else (a device name, a VNI, a table) is set aside and reported; it never stops
  the other networks.

**dci-controller**
- Set [`metalStack.routeTargetKeyFile`](../metal-stack/controller.md). Without the key,
  route targets are a plain hash of `<project>/<label value>`, and a tenant can compute a
  label value whose route target collides with another project's. The older group keeps
  its route target either way, so such a collision only costs a group that is *created*
  later. Without a key, though, a tenant can still win with an old network that they
  relabel.

**The hosts**
- Run open-dci with the systemd unit from
  [`deploy/systemd`](https://github.com/mwindower/open-dci/tree/main/deploy/systemd): it keeps
  only `CAP_NET_ADMIN` and FRR's `frrvty` group. The container image needs `--privileged`
  (it writes `net.*` sysctls).
- Keep `/metrics` and `/healthz` (`--listen`) on localhost or a management address.

**BGP between partitions**
- Every partition can announce any stitched network's prefixes. Use `aggregates` (exact
  prefixes) rather than `prefixes` with `le`, which also lets more specific routes in.
  dci-controller only uses aggregates.
