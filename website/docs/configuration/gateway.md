# Gateway

The gateway's identity in the SRv6 domain and in BGP.

| Field | Default | Meaning |
|---|---|---|
| `locator` | required | The gateway's SRv6 locator, e.g. `fd00:dc1:a::/48`. The redundant gateways of a partition share it (anycast): they announce the same locator and, with the same `sid`s, the same SIDs. |
| `loopback` | `<locator>::1` | The gateway's own address: SRv6 encap source, and session endpoint for `address` peers. The default lies inside the locator (function 0 is never a SID), which only works for a single gateway. A redundant pair needs a unique loopback per gateway, inside `locatorBlock` but outside the locator; open-dci announces it next to the locator. |
| `locatorBlock` | required | Contains the locators of all gateways, e.g. `fd00:dc1::/32`. Traffic to it is routed into the transport. |
| `nodeLength` | `16` | Node bits of the locator. Block length + node length must equal the locator's prefix length. 16 function bits follow. |
| `asn` | discovered | ASN of the existing default BGP instance. If set, it must match the running FRR. |
| `routerID` | discovered | Router-id of the existing BGP instance, used for route distinguishers. If set, it must match the running FRR. |
| `vtep` | router-id | VXLAN source address (IPv4) of the provisioned L3VNIs. |
| `siteOfOrigin` | `<asn>:<locator node>` | Site of origin (SoO, `<asn>:<nn>` or `<ipv4>:<nn>`) the gateway sets on the type-5 routes it announces into its fabric. A route carrying it is never exported into the VPN: a remote partition's route that comes back from the own fabric (e.g. via the partner gateway) must not be announced as the own partition's. It must be the same on both gateways of a pair. The default (e.g. `4200000016:10` for locator `fd00:dc1:a::/48` in block `fd00:dc1::/32`) is, as long as they share ASN and locator; set it explicitly if they don't, or if the locator node doesn't fit the local part (16 bits after a 4-byte ASN). |
