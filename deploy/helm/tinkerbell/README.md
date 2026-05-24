# Tinkerbell stack (region-deploy provisioning)

This directory holds the values overrides for the **monorepo** Tinkerbell
Helm chart, configured for the Region Deploy workflow (see
[`docs/dev/region-deploy.md`](../../../docs/dev/region-deploy.md)).

## Heads-up: deprecated layout

The original separate-repo / `stack` umbrella chart at `tinkerbell/charts`
was deprecated in 2025 alongside `tinkerbell/smee`, `tinkerbell/tink`,
`tinkerbell/hegel`, and `tinkerbell/rufio` — all consolidated into the
single [`tinkerbell/tinkerbell`](https://github.com/tinkerbell/tinkerbell)
monorepo. See roadmap issue
[#41](https://github.com/tinkerbell/roadmap/issues/41).

If you find docs or PRs that reference `tinkerbell/charts/stack` or
hegel/smee/tink/rufio as separate sub-charts, they're stale.

## What's here

| File          | Purpose                                                               |
| ------------- | --------------------------------------------------------------------- |
| `values.yaml` | Overrides for the monorepo chart. Pinned config is documented inline. |

## Quick install (central cluster)

```powershell
# 1. Fetch the chart from the monorepo's OCI registry.
$publicIP   = "10.88.0.42"   # whatever the cluster's BGP-advertised LB IP is
$artifacts  = "http://10.88.0.43:7173"   # HookOS artifacts file server
$trusted    = (kubectl get nodes -o jsonpath='{.items[*].spec.podCIDR}') -replace ' ', ','

helm install tinkerbell oci://ghcr.io/tinkerbell/charts/tinkerbell `
  -n tinkerbell --create-namespace `
  -f deploy/helm/tinkerbell/values.yaml `
  --set publicIP=$publicIP `
  --set artifactsFileServer=$artifacts `
  --set "trustedProxies={$trusted}"
```

## Image dependency

The Helm values reference **`ghcr.io/1456055067/tinkerbell:dev-dhcpv6`**,
not the upstream `ghcr.io/tinkerbell/tinkerbell:*` image. This is
required: upstream Tinkerbell has no DHCPv6 support (smee#433,
roadmap#44), and our single-IPv6-VLAN region-deploy plan depends on it.

The fork lives at
[`github.com/1456055067/tinkerbell` branch `feat/dhcpv6`](https://github.com/1456055067/tinkerbell/tree/feat/dhcpv6).

To rebuild the image:

```bash
cd ~/projects/tinkerbell-monorepo && git checkout feat/dhcpv6
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
  go build -ldflags="-s -w" -o out/tinkerbell-linux-amd64 ./cmd/tinkerbell
podman build -t ghcr.io/1456055067/tinkerbell:dev-dhcpv6 \
  -f Dockerfile.tinkerbell --build-arg TARGETOS=linux \
  --build-arg TARGETARCH=amd64 --platform=linux/amd64 .
podman push ghcr.io/1456055067/tinkerbell:dev-dhcpv6
```

## CLI flag wiring

The fork's `feat/dhcpv6` branch exposes four DHCPv6 CLI flags on the
`tinkerbell` binary, mirroring the v4 bind-flag style:

| Flag                       | Default            | Notes                                                            |
| -------------------------- | ------------------ | ---------------------------------------------------------------- |
| `--dhcp-v6-enabled`        | `false`            | Master switch; off by design so upgrades are no-ops              |
| `--dhcp-v6-bind-addr`      | `::` (unspecified) | netip.Addr; falls back to `::` when zero                         |
| `--dhcp-v6-bind-port`      | `547`              | uint16; falls back to 547 when zero                              |
| `--dhcp-v6-bind-interface` | `""`               | Required when binding the wildcard on port 547 (multicast joins) |

The chart wires these via `deployment.additionalArgs` because the
upstream monorepo chart's values schema doesn't yet have first-class
`dhcpv6:` keys. When upstream lands DHCPv6, those keys will replace
the additionalArgs passthrough.

## When upstream lands DHCPv6

When [tinkerbell/roadmap#44](https://github.com/tinkerbell/roadmap/issues/44)
ships in upstream Tinkerbell:

1. Bump `deployment.imageTag` back to the matching upstream version.
2. Replace `deployment.image` with `ghcr.io/tinkerbell/tinkerbell`.
3. Switch additionalArgs to first-class chart values (or remove the
   passthrough entirely if upstream values cover what we need).
4. Delete the FORK / Phase 0a notes from `docs/dev/region-deploy.md`.

## Verification

After install:

```powershell
kubectl get pods -n tinkerbell
kubectl logs -n tinkerbell deploy/tinkerbell | Select-String "DHCPv6 server listening"
# Expect (once CLI flags land + flag set): "DHCPv6 server listening on [::]:547"
```

For end-to-end testing against a real UEFI HTTP Boot v6 client, the
QEMU + EDK2 OVMF setup will land alongside the integration-test work
in the Phase 0a workstream.
