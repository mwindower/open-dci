# The SRv6 domain and its edge

End.DT46 decapsulates every packet addressed to a SID into the SID's VRF, whatever its
source. Whoever can send packets to a SID can inject traffic into that tenant. The SRv6
domain (gateways, their transport, exits, core) is therefore closed at its edge
(RFC 8754, section 5.1):

| Where | What is dropped | Who implements it |
|---|---|---|
| Gateway, from its tenants | packets arriving on a tenant bridge (`dcibr*`) addressed into the locator block | open-dci (`tenant-to-transport`) |
| Gateway, towards its SIDs | packets to its locator from a source outside the block | open-dci (`locator-from-outside`) |
| Gateway, towards its loopback | packets to its own loopback from outside the block (anycast mode) | open-dci (`loopback-from-outside`) |
| Exit, fabric side | anything entering the block from the fabric, and any source inside the block | the exit's ACLs (lab: `edgeFilter` in `node.yaml`) |
| Exit, core side | packets into the block with a source outside it | the exit's ACLs |

The gateway rules match packets as they arrive (nftables prerouting), before routing and
before SRv6 processing; the gateway's own encapsulated packets are never affected. Exempt
from the source rules are the gateway's own packets (via `lo`) and link-local sources,
which only the attached link can send: the own DCI VRF via the veth, or the exit (e.g.
ICMPv6 errors to the encap source). The source rules can't
stop a spoofed source *inside* the block: that is what the exits' rules are for. In
default-VRF mode, the partition's underlay is part of the transport. Any device on it that
may be untrusted (e.g. a tenant's firewall peering with the leaves) must not reach the
block. Two things ensure that: the exits announce the locators only to gateways and core,
so no fabric device has a route into the block (e2e `TestFabricHasNoTransportRoutes`), and
the exit's fabric-side rule drops what is sent there anyway. In DCI-network mode the
transport VRF doesn't exist on fabric devices at all, which is why it is the recommended
mode.

The e2e tests `TestEdgeDropsForgedSRv6FromFabric`, `TestGatewayDropsSRv6FromOutsideBlock`
and `TestGatewayDropsTenantToTransport` forge such packets and check that the respective
rule counts them and the victim sees nothing.
