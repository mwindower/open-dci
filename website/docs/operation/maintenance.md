# Maintenance

`open-dci drain` makes the gateway announce nothing:
- no locator and loopback (as while not ready, see [Health and failover](health-and-failover.md#announcing-the-locator))
- no type-5 routes from its tenant VRFs (`advertise ipv4/ipv6 unicast` in their
  `l2vpn evpn` family)

The exits and the leaves stop sending it traffic within seconds, and its anycast partner
carries everything. The gateway can then be rebooted, upgraded or unplugged. Its VRFs,
SIDs and sessions stay in place.

- The state is a marker file next to the state file (`/var/lib/open-dci/drained`). A
  running `open-dci run` keeps the gateway drained across reconciles and restarts.
- `status` shows `locator DRAINED`; a drained gateway counts as healthy.
- `open-dci undrain` removes the marker. The gateway announces again as soon as it is
  ready.
- Drain one gateway of a pair at a time; draining both disconnects the partition.

Measured in the lab: drain, wait 3 s, take the links down, bring them back, undrain. No
flow lost a packet ([measurements](failure-measurements.md)).
