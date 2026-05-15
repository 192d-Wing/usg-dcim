# Tinkerbell stack (region-deploy provisioning)

This directory holds the values overrides for the upstream Tinkerbell
[`stack`](https://github.com/tinkerbell/charts/tree/main/tinkerbell/stack)
Helm chart, configured for the Region Deploy workflow (see
[`docs/dev/region-deploy.md`](../../../docs/dev/region-deploy.md)).

## What's here

| File | Purpose |
| --- | --- |
| `values.yaml` | Overrides for the upstream chart. Pinned config is documented inline. |

## Quick install (central cluster)

```powershell
# 1. Add the chart repo.
helm repo add tinkerbell https://tinkerbell.org/charts
helm repo update

# 2. Override the environment-specific bits.
#    - global.publicIP: the central cluster's public IPv4 endpoint
#    - smee.additionalArgs '-dhcp-v6-iface=<NIC>': the interface name
#      the v6 listener should join multicast groups on
$publicIP = "10.88.0.42"   # whatever the cluster's egress IP is
$nic      = "eth0"         # whatever the smee pod's NIC is named

helm install tinkerbell tinkerbell/stack `
  -n tinkerbell --create-namespace `
  -f infra/helm/tinkerbell/values.yaml `
  --set global.publicIP=$publicIP `
  --set-string "smee.additionalArgs[2]=-dhcp-v6-iface=$nic"
```

## Image dependency

The Helm values reference **`ghcr.io/1456055067/smee:dev-dhcpv6`**, not the
upstream `quay.io/tinkerbell/smee:*` image. This is required: upstream
Smee does not support DHCPv6 (smee#433, roadmap#44), and our
single-IPv6-VLAN region-deploy plan depends on it. Until upstream lands
the feature, this stack must run the fork at
[`github.com/1456055067/smee` `feat/dhcpv6`](https://github.com/1456055067/smee/tree/feat/dhcpv6).

To produce the image:

```bash
# In ~/projects/tinkerbell-smee on branch feat/dhcpv6:
docker build -t ghcr.io/1456055067/smee:dev-dhcpv6 .
docker push ghcr.io/1456055067/smee:dev-dhcpv6
```

Or, for kind / local podman testing where no registry is involved, build
and load directly:

```powershell
podman build -t ghcr.io/1456055067/smee:dev-dhcpv6 .
podman save ghcr.io/1456055067/smee:dev-dhcpv6 -o smee-dev-dhcpv6.tar
podman load -i smee-dev-dhcpv6.tar   # on the kind node
```

## When upstream lands DHCPv6

When [tinkerbell/roadmap#44](https://github.com/tinkerbell/roadmap/issues/44)
ships in upstream Smee:

1. Bump `smee.image` back to the matching `quay.io/tinkerbell/smee:vX.Y.Z` tag.
2. Replace the `additionalArgs` passthrough with first-class `dhcpv6:`
   keys once the chart adds them.
3. Delete the FORK / Phase 0a notes from `docs/dev/region-deploy.md`.

## Verification

After install:

```powershell
kubectl get pods -n tinkerbell
kubectl logs -n tinkerbell deploy/smee | grep -i "dhcpv6"
# Expect: "DHCPv6 server listening on [::]:547"
```

Sending a v6 Solicit (from a v6-reachable host on the bound NIC):

```bash
# Quick smoke test — sends a Solicit, doesn't expect a meaningful
# reply because no Hardware CR is registered yet.
dhclient -6 -d -v <iface>
```

For end-to-end testing against a real UEFI HTTP Boot v6 client, the
QEMU + EDK2 OVMF setup will land alongside the integration-test work
in the Phase 0a workstream.
