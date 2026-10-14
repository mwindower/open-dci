# API

`open-dci run --api-listen ADDR` serves the gateway's API: gRPC (also gRPC-Web and Connect)
to read the gateway's state and to add or remove networks.
[dci-controller](../metal-stack/controller.md) uses it; operators and other tools can too.
The definition: [`api/opendci/v1/gateway.proto`](https://github.com/mwindower/open-dci/blob/main/api/opendci/v1/gateway.proto).

```sh
open-dci run -c /etc/open-dci/config.yaml --listen :9469 \
  --api-listen 10.1.0.5:9470 --api-token-file /etc/open-dci/api-token \
  [--api-tls-cert /etc/open-dci/tls.crt --api-tls-key /etc/open-dci/tls.key]
```

- Every call needs the token: `Authorization: Bearer <token>`.
- Without a certificate the API is plaintext (HTTP/2 without TLS), meant for a management
  network; with `--api-tls-cert`/`--api-tls-key` it is TLS.
- Networks set through the API survive restarts (`/var/lib/open-dci/api-networks.json`).
  While the API is enabled but has never received a network, the gateway doesn't change
  anything yet: it waits for the first write.

## Methods

| Method | What it does |
|---|---|
| `GetInfo` | the gateway's identity (ASN, router-id, locator), health and locator state |
| `ListNetworks` | every network, from the config file (`config`) or the API (`api` and its owner), with its SID and state |
| `SetNetworks` | replaces all networks of an owner; networks that conflict or are invalid are reported and skipped |
| `PutNetwork` | adds or replaces one network of an owner; refused if it conflicts or is invalid |
| `DeleteNetwork` | removes one network of an owner |

A network has the same fields as in [the config file](../configuration/networks.md); the
SID is assigned by the gateway if not given.

**Owners.** Every API network belongs to an owner, a name the writer chooses: the
controller uses `dci-controller`, an operator might use their own name. An owner only
changes its own networks, so manual changes and the controller don't overwrite each
other. Networks from the config file always win a conflict; between API networks, the
older one wins.

## With grpcurl

```sh
TOKEN=$(cat /etc/open-dci/api-token)
grpcurl -plaintext -H "Authorization: Bearer $TOKEN" 10.1.0.5:9470 list
grpcurl -plaintext -H "Authorization: Bearer $TOKEN" 10.1.0.5:9470 opendci.v1.GatewayService/ListNetworks
grpcurl -plaintext -H "Authorization: Bearer $TOKEN" -d '{
  "owner": "ops",
  "network": {"vrf": "vrf4050", "vni": 4050, "route_target": "65535:4050", "aggregates": ["10.50.0.0/24"]}
}' 10.1.0.5:9470 opendci.v1.GatewayService/PutNetwork
grpcurl -plaintext -H "Authorization: Bearer $TOKEN" -d '{"owner": "ops", "vrf": "vrf4050"}' \
  10.1.0.5:9470 opendci.v1.GatewayService/DeleteNetwork
```

The network appears (or disappears) within seconds: the gateway reconciles right after a
change. The [metrics](metrics.md) show the API networks (`open_dci_api_*`).
