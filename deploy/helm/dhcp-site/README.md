# dhcp-site

Per-`DhcpServer` Helm release that exposes a Kea Control Agent (and
optionally `kea-dhcp6`) via a `type: LoadBalancer` Service pinned to
an anycast IP through Cilium LB-IPAM, advertised by Cilium BGP.

**Scope:** the *control plane* (the URL DCIM stores in
`DhcpServer.kea_url`) and DHCPv6 unicast. **DHCPv4 is intentionally
not exposed here** — its broadcast nature requires DHCP Relay
(RFC 1542) at the router, which is a router-side configuration and out
of scope for this chart.

## Install

The values dict is produced by
[`packages/otter/src/dcim/regiondeploy/dhcp_site.py`](../../../packages/otter/src/dcim/regiondeploy/dhcp_site.py)
from a `DhcpServer` row + chosen anycast IP(s).

```bash
# Operator-owned: Kea config + auth (chart does not generate these).
kubectl -n dcim-dhcp create configmap kea-ctrl-agent-config \
  --from-file=kea-ctrl-agent.conf=/path/to/kea-ctrl-agent.conf
kubectl -n dcim-dhcp create secret generic kea-ctrl-agent-auth \
  --from-file=auth.csv=/path/to/auth.csv

helm install dhcp-<server-name> deploy/helm/dhcp-site \
  --namespace dcim-dhcp --create-namespace \
  -f values-<server-name>.yaml
```

After install, update the `DhcpServer.kea_url` row in DCIM to point at
the anycast IP (e.g. `https://192.0.2.67:8000`) so the worker calls
the LB-pinned endpoint.

## Prerequisites

- Cilium with BGP control plane + LB-IPAM enabled in the site cluster.
- A `CiliumBGPAdvertisement` matching `dcim.io/bgp-advertise=true`
  (umbrella PR 70 ships one; regional-deploy clusters get one through
  `regiondeploy/cilium.py`).
- Operator-managed `ConfigMap` containing `kea-ctrl-agent.conf` and
  `Secret` containing Kea's basic-auth CSV.
- Optional TLS Secret (`tls.crt` + `tls.key`) for HTTPS Kea REST.

## What this chart does NOT do

- **Generate Kea config.** PR 72 mounts what the operator provides.
  A future PR can mirror the DNS bundle pipeline
  (`/api/v1/dhcp/servers/{id}/bundle`).
- **Expose DHCPv4 broadcast.** Use a router-side DHCP relay agent
  pointing at the Service's anycast IP for DHCPv4.
- **Manage Kea HA / database backend.** Operator chooses
  memfile / MySQL / PostgreSQL through their Kea config.
