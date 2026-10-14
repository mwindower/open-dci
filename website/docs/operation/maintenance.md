# Maintenance

`open-dci drain` makes the gateway announce nothing:
- no locator and loopback, so no traffic arrives from the other partitions
- no tenant routes into its fabric, so no traffic arrives from the own partition

The exits and the leaves stop sending it traffic within seconds, and its anycast partner
carries everything. The gateway can then be rebooted, upgraded or unplugged. Its VRFs,
SIDs and sessions stay in place.

- The gateway stays drained across restarts (a marker file in `/var/lib/open-dci/`).
- `status` shows `locator DRAINED`; a drained gateway counts as healthy.
- `open-dci undrain` ends it: the gateway announces again as soon as it is
  [ready](health-and-failover.md#a-gateway-announces-itself-only-when-ready).
- Drain one gateway of a pair at a time; draining both disconnects the partition.

Measured in the lab: drain, wait 3 s, take the links down, bring them back, undrain. No
flow lost a packet ([measurements](https://github.com/mwindower/open-dci/blob/main/docs/performance.md)).
