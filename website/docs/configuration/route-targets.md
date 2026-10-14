# Route targets

## Choosing route targets

Any value works as long as it is unique per stitched network and identical on all its
members.

- **Local part vs. VNIs.** EVPN auto RTs import by the local part only
  ([Phase 0 findings](../development/phase0-findings.md#route-target-behaviour-frr-106-l3vni-tested-live-in-the-lab)),
  so a DCI RT `X:N` that reached a fabric would be imported by any partition VRF whose VNI
  is `N`. The gateways strip all extended communities a VPN route brought along before
  they announce it as type-5 (`DCI-ADV`, see [Operation](../operation/what-open-dci-changes.md#frr-via-vtysh-never-touching-frrconf)),
  so no DCI RT reaches a fabric. As a second line of defence, local parts ≥ 2^24 (16777216)
  can never collide with a 24-bit VNI.
- **Width of the local part.** A 2-byte admin ASN allows a 32-bit local part
  (`65535:16777217`). A 4-byte ASN or an IPv4 admin part allows only 16 bits; `validate`
  rejects wider ones.
- **The RD** is `<routerID>:<sid>` by default, independent of the RT, so any RT works
  without an explicit `rd`.

## Deriving route targets automatically

Implemented for the [metal-apiserver source](metal-apiserver.md).
If every stitched network has a stable **name**, its RT can be computed instead of
allocated:

```
RT = <admin ASN2> : 2^24 + (fnv32a(name) mod (2^32 − 2^24))
```

- The RT is deterministic, and no registry is needed.
- It can't collide with a VNI (auto RTs on the EVPN side).
- The admin part is a 2-byte namespace (`routeTargetASN`), independent of the gateways'
  ASNs: after a 4-byte ASN the local part only has 16 bits, too few for hashing.
- Hash collisions between two names are unlikely but possible. Only a component with a
  global view can detect them. The source sees all groups, so every gateway skips the
  colliding ones, and reports them.

The source takes the name from a label on the private networks (`source.metalStack.label`,
e.g. `open-dci.io/stitch: prod`). Label values are only unique within a project, so the
name is `<projectID>/<value>`: nothing is stitched across projects, a project can have
several stitched networks, and stitching is an explicit opt-in by the tenant. Stitching all
private networks of a project automatically was implemented once and dropped: a project's
networks in one partition are usually separate on purpose.

The VRF ID of a designated anchor network as the RT local part was considered and dropped:
it is unique within one metal-api without hashing, but the RT depends on the anchor network
staying alive, and it isn't unique across several metal-api instances.

The RD doesn't depend on the RT (`<routerID>:<sid>`), so derived RTs need no RD scheme.
