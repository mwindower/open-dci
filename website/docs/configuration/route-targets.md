# Route targets

The route target (`networks[].routeTarget`) is a stitched network's identity: the same
value on all gateways of that network, and a different one for every other stitched
network.

- Pick any value, e.g. `65535:1001`, `65535:1002`, … per stitched network.
- After a 2-byte ASN, the number after the colon can be large (up to 4294967295); after a
  4-byte ASN or an IPv4 address it is limited to 65535. `validate` checks this.
- Values from 16777216 (2^24) upwards can never be confused with a VNI, which is a good
  habit, though open-dci keeps route targets out of the fabrics anyway.
- The route distinguisher is derived automatically (`<router-id>:<sid>`); you don't need
  to set `rd`.

With [networks from metal-apiserver](metal-apiserver.md), open-dci derives the route
targets itself.
