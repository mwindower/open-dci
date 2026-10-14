# Transport

Where the SRv6 transport (locators, gateway loopbacks) is routed. The two modes and when to
pick which: [How it works](../introduction/how-it-works.md).

| Field | Default | Meaning |
|---|---|---|
| `vrf` | empty | EVPN VRF of the base config (a "DCI network") that carries the SRv6 transport. Empty: the transport is routed in the default VRF. |
| `mtu` | `9166` | DCI network mode only: MTU for the DCI VRF's bridge, VXLAN port, SVI and the veth pair. It must be ≥ `tenantMTU` + 48. |
| `tenantMTU` | `9000` | Largest tenant packet. Used to validate `mtu`, and as MTU of the provisioned L3VNIs' bridge and VXLAN device. |
| `veth`, `vethPeer` | `dci0`, `dci1` | DCI network mode only: names of the veth ends in the default VRF and in the DCI VRF. |
