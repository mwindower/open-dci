# Security analysis

A review of open-dci's security: what is at stake, who could attack, what was found, what
has been fixed and what is still open. User-facing advice (what an operator has to do) is
on the website: [Security](https://mwindower.github.io/open-dci/docs/operation/security).

Scope: the open-dci gateway (API, config, FRR rendering, kernel and nftables), dci-controller
and its metal-stack mapping, the deploy files, the container image and CI/release. Method:
code review, then fixes with unit and e2e tests. `govulncheck ./...` finds no vulnerability
that the code reaches (as of 2026-10-22).

## Threat model

### What is at stake

| Asset | Why it matters |
|---|---|
| Tenant isolation | the product joins tenant VRFs; joining the wrong ones, or letting packets into a foreign VRF, is the worst failure |
| Integrity of the stitching | which VNI is joined to which route target, which prefixes go where |
| Availability of the DCI paths | a gateway that stops reconciling loses its configuration at the base system's next FRR reload |
| Credentials | the gateway API token (control over every tenant VRF of a gateway); dci-controller's metal-apiserver token (admin viewer) |

### Trust boundaries

```
 tenant machines/firewalls ─┐
                            │  (1) fabric: EVPN/VXLAN, tenant VRFs
            leaves/spines ──┤
                            │  (2) edge of the SRv6 domain: exit ACLs, gateway nftables
   exits ── gateways ───────┤
                            │  (3) between partitions: one VPN, every gateway and exit trusted
                core ───────┤
                            │  (4) inter-DC links
 management: API, controller, metal-apiserver labels  (5)
```

1. **Tenant ↔ fabric.** Tenants control their machines and, in metal-stack, their
   firewalls. In default-VRF transport mode those firewalls sit in the partition's underlay.
2. **Fabric ↔ SRv6 domain.** End.DT46 decapsulates any packet that reaches a SID; SRv6 has
   no authentication. Only filtering keeps forged packets away.
3. **Partitions among each other.** Every gateway and exit of every partition shares one
   VPN. A gateway imports what any of them announces with a network's route target, within
   the network's prefix allowlist.
4. **The core** carries SRv6 between data centres, unencrypted.
5. **Management.** The gateway API token can attach any VNI of the partition to any route
   target, with any prefixes: whoever holds it can join any tenant to any other.
   dci-controller derives the networks from labels that tenants set themselves.

### Attackers

| Attacker | Can |
|---|---|
| Tenant | set labels on their networks; announce routes in their own VRF; in metal-stack, run a firewall in the underlay (a VTEP) |
| Compromised or misconfigured router in another partition (gateway, exit) | announce any VPN route; send SRv6 with a source inside the block |
| On-path on the inter-DC links | read and modify tenant traffic; forge SRv6 |
| Reaching the management network | talk to the gateway API without a token |
| Holding the API token | everything the API allows: cross-tenant joins on every gateway that uses the token |

## Findings

| ID | Severity | Finding | Status |
|---|---|---|---|
| H1 | high | API plaintext by default; one token for all gateways and writers; controller accepts `http://` silently | **open** (deferred, see [below](#h1-api-transport-and-token)) |
| H2 | high | unauthenticated clients could make the gateway read and decode unlimited request bodies | fixed |
| M1 | medium | one API network that conflicts with the system failed every reconcile | fixed |
| M2 | medium | a tenant could compute a route-target collision and take another project's group off the air | fixed |
| M3 | medium | prefixes aren't bound to their partition: any partition can attract another's traffic | **open**, [proposal](#m3-binding-prefixes-to-their-partition) |
| M4 | medium | the gateway accepted SRv6 claiming its *own* source from outside; the exit ACLs are lab-only | fixed (gateway part); exit ACLs stay the operator's |
| M5 | medium | tenant traffic crosses the core unencrypted | documented (operator's part) |
| M6 | medium | anyone reaching the VTEP can inject VXLAN into tenant VRFs | fixed (`gateway.vxlanSources`, opt-in) |
| M7 | medium | no BGP session protection options; `maximum-prefix` tears down the session that carries all partitions | **open**, [proposal](#m7-bgp-session-protection) |
| M8 | medium | the service ran with full root, the container `--privileged` | fixed for systemd; container documented |
| L1 | low | API authorization is all-or-nothing: `owner` is self-declared, clients may backdate `created`, no audit log | open |
| L2 | low | no limit on the number of API networks or owners | open |
| L3 | low | state files are world-readable (`0644`) | partly: the systemd unit sets `UMask=0027`, `StateDirectoryMode=0750` |
| L4 | low | `/metrics`, `/healthz` unauthenticated | documented (bind to localhost or management) |
| L5 | low | actions pinned by tag, containerlab via `curl \| bash` in CI, unsigned releases, no SBOM | open |
| L6 | low | the lab's API token is committed | by intent (lab only) |

### H2: unauthenticated request bodies

The token was checked in a connect interceptor. connect decodes a unary request *before*
its interceptors run, and nothing limited the size, so anyone who reached the API port
could make `open-dci run` (which also runs the reconcile loop and the health withdrawal)
read and decode arbitrarily large or slow bodies, up to an OOM kill.

Fixed: an HTTP middleware checks the token before anything reads the request (also for the
reflection service), messages are limited to 4 MiB (`api.MaxRequestBytes`), and both HTTP
servers (API and metrics) have read, write and idle timeouts and a header limit.
Tests: `TestAuthBeforeBody` (no token: answered at once, even for a body that never ends;
with the token: an oversized message is refused).

### M1: a conflicting API network froze the gateway

The API only validated networks against the config. A network whose VRF, bridge or VXLAN
name already existed without the `open-dci` tag (e.g. `vrf: uplink0`), or whose VNI the
base config used, failed `ensureOwned` or the vtysh batch, and with it the *whole*
reconcile, every round. The network was persisted, so a restart didn't help, and the next
FRR reload of the base system left the gateway without any of its lines.

Fixed: every `Plan` screens the API networks against the kernel and the running FRR config
(`internal/gateway/screen.go`) and sets aside those that conflict: a device with one of
its names that open-dci didn't create, a foreign VXLAN device with its VNI, a foreign VRF
with its table (which would also share routes with it), a VNI that FRR maps to a VRF that
isn't open-dci's, or a BGP instance for its VRF that open-dci didn't add. They are reported
like other skipped API networks (`ListNetworks`, `status`, `open_dci_api_skipped`) and
applied once the conflict is gone. Networks of the config file aren't screened: a conflict
there is the operator's and still stops the reconcile. `validate` also rejects VRF names
that open-dci uses for its own devices (`dcibr*`, `dcivx*`, the veth names).
Tests: `TestKernelConflict`, `TestFRRConflict`, `TestScreen`; e2e `TestAPIConflictsSetAside`.

### M2: route-target collisions chosen by a tenant

Derived route targets are `fnv32a(<project>/<label value>)`, and when two groups collided
both were skipped. FNV is no secret: knowing another project's ID and label value, a tenant
finds a colliding label value in minutes, labels one of their networks with it, and the
other project's stitched network is withdrawn in every partition.

Fixed: of colliding groups, the oldest (by its oldest network, then name) keeps the route
target; the younger ones are skipped. That alone doesn't stop a tenant who relabels an
*old* network, so `metalStack.routeTargetKeyFile` keys the hash (HMAC-SHA256); without the
key nobody can compute collisions. The key is optional for compatibility: setting it
changes every derived route target, which re-stitches all groups once. The lab runs
without it (its tests rely on the known route target `65535:26782482`).
Tests: `TestRouteTargetCollision` (finds a real FNV collision, checks that the older group
wins either way, and that the keyed hash removes it), `TestRouteTargetKey`.

### M4: forged own source

The gateway accepts SRv6 to its SIDs from any source inside the locator block. Sources
inside the block from outside the domain are the exits' job, but those ACLs exist only in
the lab (`labnode`'s `edgeFilter`); in production they are the operator's configuration,
which open-dci can't see.

Fixed (the part the gateway can know): `own-locator-spoofed` and `own-loopback-spoofed`
drop packets that arrive with the gateway's own locator or loopback as source from
anywhere but `lo` (and, in DCI-network mode, the veth's DCI end, where the gateway's own
encapsulated packets enter the transport VRF, and the transport VRF device itself). e2e: `TestGatewayDropsOwnSourceSpoofed`. The exits' ACLs are listed as the operator's part
on the website, now also with what happens without them.

### M5: the core is unencrypted

SRv6 and VXLAN carry tenant traffic in the clear, and the exits' core-side rule accepts
any source inside the block from the core. Where the inter-DC links cross third-party
infrastructure, they need MACsec or IPsec underneath. This is now stated on the website
(Security).

### M6: VXLAN from anywhere

A Linux VXLAN device accepts packets for its VNI from any sender; it doesn't check them
against the VTEPs EVPN learned. Whoever can reach the gateway's VTEP (from the core, or a
device in the underlay) can inject frames into a tenant VRF, addressed to the gateway's
router MAC, and the gateway routes them on into every partition.

Fixed, opt-in: `gateway.vxlanSources` (the fabric's VTEP prefixes) adds the rule
`vxlan-from-outside` in a new IPv4 nftables table: UDP 4789 from any other source is
dropped. It can't separate a VTEP inside the range, e.g. a metal-stack firewall, which the
fabric trusts anyway. The lab sets it on all gateways. e2e:
`TestGatewayDropsVXLANFromOutside`.

### M8: privileges

`deploy/systemd/open-dci.service` now keeps only `CAP_NET_ADMIN` (netlink, nftables, the
`net.*` sysctls) and the `frrvty` group (vtysh), and adds the usual sandboxing
(`ProtectSystem=strict`, `PrivateTmp`, `NoNewPrivileges`, a system-call filter, restricted
address families, no module loading). `dci-controller.service` gets no capabilities at all
and its secrets through `LoadCredential`. **Not tested on a real host**: the lab runs
open-dci as a container sidecar. The thing most likely to need adjusting is vtysh's access
to FRR's sockets (root without `CAP_DAC_OVERRIDE` relies on the `frrvty` group).

The container image still needs `--privileged`: open-dci writes `net.*` sysctls, which are
read-only in an unprivileged container. The Dockerfile says so and points to the unit.

## Open items

### H1: API transport and token

Deferred by decision (2026-10-22). Today the API speaks plaintext unless
`--api-tls-cert` is given, all gateways and writers share one token, and dci-controller
accepts `http://` endpoints. Since the token controls every tenant VRF of every gateway
that shares it, this remains the largest risk. Recommended:

- refuse to serve the API without TLS unless `--api-insecure` is given (the lab would set it);
- per-gateway tokens in dci-controller (`gateways[].tokenFile`), so one leaked token
  exposes one gateway;
- optional mTLS (`--api-client-ca`), with the owner derived from the client certificate
  (also solves L1);
- refuse token files that are readable by group or others;
- dci-controller: refuse `http://` endpoints without `insecure: true`.

### M3: binding prefixes to their partition

A stitched network's allowlist is the same on every gateway, and the import filter checks
route target and prefix, not who announced it. A compromised or misconfigured gateway or
exit in partition B can announce partition A's aggregate (or, where `prefixes` use `le`,
a more specific route) and attract its traffic from all other partitions.

Ideas, in order of strength:

1. **Bind each aggregate to its partition's locator.** A VPN route carries the SID of the
   gateway that announced it; the SID lies in that partition's locator, and the traffic
   goes there. If the import filter only accepted an aggregate with a SID from its own
   partition's locator, a hijack would have to forge the SID, which then leads the traffic
   to the real owner. Needs: the aggregate's partition in the network (dci-controller
   knows it; the config would need `aggregates: [{prefix, locator}]` or similar), and a way
   to match the SID. To be checked whether FRR 10.4.1's route-maps can match the SRv6 SID
   of a VPN route; if not, open-dci could read the VPN table (it already does for health and
   aggregates) and filter offending prefixes per route (`clear`/deny entries), at the cost
   of a reaction delay.
2. **Origin community per partition** (set on export, matched with the prefix on import).
   Self-asserted, so it only protects against misconfiguration, not a compromised gateway.
3. Prefer `aggregates` (exact match) over `prefixes` with `le` (more specifics);
   dci-controller already does.

### M7: BGP session protection

- `peers[].password` (TCP-MD5; TCP-AO when FRR supports it) and `peers[].ttlSecurity` (GTSM)
  for `address` peers, which open-dci renders itself. Interface peers are the base
  config's sessions to the exits; their protection belongs there.
- `peers[].maxPrefixRestart` (minutes) → `maximum-prefix N restart M`, so one flooding
  partition doesn't cut a gateway off from all partitions until someone clears the session;
  or a warning-only mode plus a per-VRF limit.
- Each needs the canonical FRR 10.4.1 form checked against a running config
  (`TestRenderMatchesRunningConfig`) and an e2e test.

### Low

- **L1** see H1 (mTLS identity); meanwhile log every write with the caller's address, and
  ignore a `created` in the future or older than the owner's existing networks.
- **L2** a configurable maximum of API networks (each one costs three devices and a BGP
  instance; `WithNetworks` validates in O(n²)).
- **L3** write state files with `0600`/`0640` in the code too, not only via the unit's umask.
- **L5** pin actions by SHA (with Dependabot), install containerlab from a pinned release
  asset with a checksum, sign releases and images (cosign, keyless), let goreleaser
  generate SBOMs, run `govulncheck` in CI.

## Assumptions that remain

- The SRv6 domain (gateways, exits, core) is trusted, and its edge is closed by the exits'
  ACLs, which open-dci can't verify.
- All partitions trust each other with the routes of the networks they share (M3).
- The holder of the API token is trusted with every tenant of the gateway (H1).
- In metal-stack, the fabric trusts its VTEPs, including tenants' firewalls; `vxlanSources`
  can't separate them.
