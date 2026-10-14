# The SRv6 domain and its edge

A gateway delivers every packet addressed to one of its SIDs into that tenant's network,
whoever sent it. Anyone who can send packets to a SID could therefore inject traffic into a
tenant. That's why the SRv6 domain (the gateways, their transport, the exits and the core)
is closed at its edge: packets into the gateways' locator block are only accepted from
inside the domain.

| Where | What is dropped | Who implements it |
|---|---|---|
| Gateway, from its tenants | packets arriving on a tenant bridge (`dcibr*`) addressed into the locator block | open-dci (`tenant-to-transport`) |
| Gateway, towards its SIDs | packets to its locator from a source outside the block | open-dci (`locator-from-outside`) |
| Gateway, towards its loopback | packets to its own loopback from outside the block (anycast mode) | open-dci (`loopback-from-outside`) |
| Exit, fabric side | anything entering the block from the fabric, and any source inside the block | the exit's ACLs (lab: `edgeFilter` in `node.yaml`) |
| Exit, core side | packets into the block with a source outside it | the exit's ACLs |

open-dci installs the gateway rules itself (nftables), and `status` and the metrics show
their drop counters. The exit rules are **your part**: they belong to the exits' ACLs.

In default-VRF mode, the partition's underlay is part of the transport, so the exits must
also never announce the locator block into the fabric. In DCI-network mode, the transport
doesn't exist on fabric devices at all, which is why it is the recommended mode.

How the rules match, and how they're tested: [The ingress filter in detail](https://github.com/mwindower/open-dci/blob/main/docs/internals.md#the-ingress-filter-in-detail).
