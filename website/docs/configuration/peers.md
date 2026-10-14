# Peers

The BGP sessions that carry the VPN routes, in one of two forms:

| Field | Meaning |
|---|---|
| `interface` | **To the exit (recommended).** The base config's existing session over this interface (e.g. the unnumbered `uplink0` to the exit). open-dci only activates VPNv4/v6 on it, with its filters. The exits relay the VPN routes between the partitions, so a gateway needs no session to remote gateways, and a new partition touches no existing gateway. |
| `address`, `asn` | **Direct to a remote gateway (full mesh).** Its loopback and ASN; a different ASN gives eBGP multihop (`update-source` = own loopback), an equal one iBGP. The address must be inside `locatorBlock` and not inside the own locator. |
| `maxPrefixes` | Maximum VPN prefixes accepted from the peer per address family (default `10000`). FRR tears the session down when it is exceeded; it stays down until `clear bgp <neighbor>`. A session to the exit carries the routes of **all** partitions: size it accordingly. |

Every peer only delivers routes that carry one of the configured `routeTarget`s (an inbound
route-map `DCI-PEER-IN` on the VPN sessions); routes with other RTs are dropped at the
session, before any VRF import.
