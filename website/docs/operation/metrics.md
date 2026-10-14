# Metrics and health

`run --listen ADDR` (e.g. `:9469`, off by default) serves two endpoints. Both answer from
what `run` collected last: after every reconcile, at least once per interval (`-i`), `run`
collects the same status as `open-dci status`. A scrape never calls into FRR. The endpoints
have no authentication: bind them to a management address or filter the port.

**`GET /healthz`** answers `200` if the gateway is healthy by the same rules as the exit
code of `status` (a drained gateway is healthy), `503` otherwise. The body says why:

```json
{"healthy":false,"locator":"withdrawn","reason":"no remote SID reachable (fd00:dc1:b:fab::)",
 "problems":["unhealthy: no remote SID reachable (fd00:dc1:b:fab::)"],"updated":"2026-10-13T12:00:00Z"}
```

A status older than three intervals plus 30 s counts as unhealthy (`run` is stuck), as
does one that could not be collected yet. `warnings` lists what needs attention without
making the gateway unhealthy: networks set through the [API](api.md) that can't be
applied (e.g. a conflict with the config file).

**`GET /metrics`** (Prometheus), all prefixed `open_dci_`:

| Metric | Labels | Meaning |
|---|---|---|
| `info` | `version`, `asn`, `router_id`, `loopback`, `locator` | always 1 |
| `healthy` | | 1 if `/healthz` answers 200 |
| `locator_state` | `state` | 1 for the current state: `announced`, `withheld` (waiting for the fabric), `withdrawn` (unhealthy), `drained` |
| `dataplane_healthy` | | 1 if the [health checks](health-and-failover.md#an-unhealthy-gateway-withdraws-itself) pass |
| `frr_missing_lines` | | drift: rendered lines missing in FRR |
| `reconciles_total`, `reconcile_errors_total` | | reconciles since start, failed ones |
| `frr_applies_total`, `frr_removals_total` | | reconciles that repaired drift or removed stale lines |
| `withdrawals_total` | | times the health checks withdrew the gateway |
| `last_reconcile_timestamp_seconds`, `status_timestamp_seconds` | | when `run` last reconciled successfully, collected the status |
| `peer_established` | `peer` | 1 if the VPN session is up |
| `peer_vpn_prefixes` | `peer`, `family`, `direction` | VPN prefixes `accepted` from / `sent` to the peer |
| `network_l3vni_up` | `vrf`, `vni` | 1 if the L3VNI is `Up` |
| `network_sid` | `vrf`, `sid` | 1 if the network has its End.DT46 SID |
| `network_prefixes` | `vrf`, `family`, `origin` | best paths in the tenant VRF, `local` (own partition) or `remote` (VPN) |
| `network_no_best_path` | `vrf` | prefixes with valid paths but no best path (a silent black hole) |
| `kernel_vrf_strict_mode` | | 1 if `net.vrf.strict_mode=1` |
| `filter_dropped_packets_total` | `rule` | packets dropped by the [ingress filter](srv6-domain-edge.md) |
| `filter_rules`, `filter_rules_required` | | rules installed / wanted |
| `transport_veth_up`, `transport_path_mtu_bytes`, `transport_required_mtu_bytes`, `transport_local_rule_last` | | DCI network mode only: the transport's kernel parts |
| `api_networks` | | with the [API](api.md) enabled: the networks applied from it |
| `api_last_change_timestamp_seconds` | | when the API networks last changed |
| `api_skipped` | `vrf`, `group`, `reason` | 1 per API network that isn't applied |

Plus the Go runtime and process metrics (`go_*`, `process_*`).

Useful alerts:
- `open_dci_healthy == 0` for a few minutes: the partner carries the traffic, but the pair
  has lost its redundancy.
- `increase(open_dci_frr_applies_total[1h]) > 0`: something keeps removing open-dci's
  configuration.
- `increase(open_dci_filter_dropped_packets_total[5m]) > 0`: someone sends SRv6 into the
  domain from outside.
- `open_dci_network_prefixes{origin="remote"}` dropping to 0: a partition disappeared.
- With the API: `open_dci_api_skipped == 1` (a network set through the API can't be
  applied). Alerts for metal-apiserver and the stitched networks belong to
  [dci-controller](../metal-stack/controller.md#operation) (`dci_controller_healthy == 0`,
  `dci_controller_skipped == 1`, `dci_controller_gateway_up == 0`).
