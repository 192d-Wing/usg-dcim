# dns-site

Per-DnsServer Helm release. One release per `DnsServer` row on the
site cluster, providing a CoreDNS pod + bundle-puller sidecar, fronted
by a `type: LoadBalancer` Service whose IP(s) come from the matching
`AnycastGroup` and are advertised to upstream routers by **Cilium BGP**
(not by a GoBGP sidecar).

This replaces the `deploy/k8s/site42/` pattern: same bundle contract,
same Corefile/zone semantics, no per-pod GoBGP container.

## Install

The values dict is produced by
[`packages/otter/src/dcim/regiondeploy/dns_site.py`](../../../packages/otter/src/dcim/regiondeploy/dns_site.py)
from a `DnsServer` + `AnycastGroup` row. Render to a file and apply:

```bash
helm install dns-<server-name> deploy/helm/dns-site \
  --namespace dcim-dns --create-namespace \
  -f values-<server-name>.yaml
```

## Prerequisites in the site cluster

1. **Cilium** with BGP control plane and LB-IPAM enabled (the regional
   deploy provisions this — see
   [`packages/otter/src/dcim/regiondeploy/cilium.py`](../../../packages/otter/src/dcim/regiondeploy/cilium.py)).
2. **CiliumBGPAdvertisement** matching `dcim.io/bgp-advertise=true`
   (the umbrella chart `deploy/helm/dcim/templates/bgp.yaml` ships
   one — same selector value).
3. **Bundle bearer token Secret**:

    ```bash
    kubectl -n dcim-dns create secret generic dcim-dns-site-token \
      --from-literal=token="$(cat dns-server-token.txt)"
    ```

4. (Optional) **Private CA bundle** for the bundle API:

    ```bash
    kubectl -n dcim-dns create secret generic dcim-dns-ca \
      --from-file=ca.crt=/path/to/ca.crt
    ```

   Then set `bundle.caBundleSecretName=dcim-dns-ca` in values.

## What the chart deploys

- One **Deployment** with two containers:
    - `coredns` — reads `/var/lib/dcim-dns/<role>/Corefile`.
    - `bundle-puller` — polls
      `<apiBaseUrl>/api/v1/dns/servers/<server-id>/bundle`,
      etag-skips, writes new files into the shared emptyDir,
      signals coredns via `SIGUSR1`.
- One **Service** of `type: LoadBalancer`:
    - Pinned to `AnycastGroup.anycast_ipv4` / `anycast_ipv6` via
      `io.cilium/lb-ipam-ips` annotation.
    - Carries `dcim.io/bgp-advertise=true` so Cilium's
      `CiliumBGPAdvertisement` selects it.
    - `externalTrafficPolicy: Local` so resolver sees the real
      client IP (important for `view`/`acl` plugins).

## What this chart does NOT do

- **Cilium install** — the regional deploy owns that.
- **BGP cluster/peer config** — owned by the umbrella chart (or the
  regiondeploy renderer) on the cluster level. The chart only
  *advertises* via label.
- **Cilium L2 announcements** — if the upstream is L2-only (no BGP),
  emit a `CiliumL2AnnouncementPolicy` separately (out of scope here).
- **Auth-server anycast** — `server.role=auth` is unicast-only;
  `service.anycastIPs` is empty and Cilium picks from a pool.
