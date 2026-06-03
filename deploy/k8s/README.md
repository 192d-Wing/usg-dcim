# Kubernetes Deployment (raw manifests + kustomize)

This directory contains raw Kubernetes manifests for two scenarios where
the Helm chart at [`../helm/dcim`](../helm/dcim) isn't the right tool:

1. **Local Podman Desktop / kind** — quick `kubectl apply -k` cycle
   without a Helm install. See [Quick Start](#quick-start) below.
2. **Per-site air-gapped deploys** — sites where Helm isn't available
   (no chart-museum mirror, no Tiller, etc.). Each site overlays
   [`site-base/`](./site-base/) — `site42/` is the worked example.

For **the central cluster in any environment with Helm**, use the
chart, not the manifests here:

```sh
helm upgrade --install dcim deploy/helm/dcim \
  -f deploy/helm/dcim/values.yaml \
  -f my-site-overrides.yaml
```

> [!IMPORTANT]
> The raw manifests in [`central/`](./central/) and the Helm chart in
> `../helm/dcim/` deploy **the same workloads** but are maintained
> independently. If you change one, walk the other to keep them in
> sync — there is no CI gate that diffs them, and drift will silently
> ship one stack with the new behavior and one without. The long-term
> intent is to retire the central raw manifests once every operator
> has Helm available; until that's confirmed, treat both as canonical.

## Prerequisites

- **Podman Desktop** with kubectl installed
- **kubectl** configured to talk to your Podman k8s cluster:

  ```powershell
  kubectl cluster-info
  kubectl get nodes
  ```

- **Images built** locally (see below)

## Quick Start

### 1. Build Container Images

```powershell
.\infra\k8s\scripts\build-images.ps1
```

Builds and tags all images `:dev`:

- `dcim-finch:dev` — React UI
- `dcim-heron:dev` — Telemetry ingest service
- `dcim-magpie:dev` — Alert evaluation service
- `dcim-beagle:dev` — DNS health checker
- `dcim-badger:dev` — Site collector agent

### 2. Deploy Central Stack

```powershell
kubectl apply -k deploy/k8s/central/
```

Deploys: postgres (TimescaleDB), redis, keycloak, finch, heron, magpie, beagle into the `dcim` namespace.

Wait for all pods:

```powershell
kubectl get pods -n dcim -w
```

### 3. Set Up Host Entries

Add to `C:\Windows\System32\drivers\etc\hosts`:

```text
127.0.0.1 dcim.prod.dev.mil
127.0.0.1 keycloak.prod.dev.mil
```

### 4. Access Services

NodePort services:

- Frontend: `http://localhost:30080`
- API: `http://localhost:30000`
- Keycloak: `http://keycloak.prod.dev.mil:30880/admin` (admin / admin)

Or use `kubectl port-forward` per service if NodePorts aren't reachable.

### 5. Run Migrations and Seed Data

```powershell
.\infra\k8s\scripts\migrate-seed.ps1
```

- Runs Alembic migrations
- Seeds demo data (6 sites, admin user, full inventory hierarchy)

**Demo sites:** `CONUS-001`, `CONUS-002`, `EUCOM-001`, `EUCOM-002`, `INDOPACOM-001`, `INDOPACOM-002`

**Admin user:** `admin@dcim.local` / `changeme`

### 6. Log In

- Web UI: `http://dcim.prod.dev.mil` — click "Log in with OIDC", use `dcim_admin` / `dcim_admin`
- Direct (dev token): `admin@dcim.local` / `changeme`

---

## Enrolling Site Collectors

```powershell
.\infra\k8s\scripts\enroll-site.ps1 -SiteCode CONUS-001
```

Output:

```text
COLLECTOR_ID:     <uuid>
ENROLLMENT_TOKEN: enroll_xxx...
SITE_ID:          <uuid>
```

Then create the k8s secret:

```powershell
kubectl create secret generic collector-enrollment -n dcim-site1 `
  --from-literal=token=<TOKEN> `
  --from-literal=collector_id=<COLLECTOR_ID> `
  --from-literal=site_id=<SITE_ID>
```

---

## Site Stack Deployment

Each site runs: **go-collector + coredns-auth + Hickory recursive + gobgp**

### Network Requirements

Sites use `hostNetwork: true` and `hostPID: true`:

- DNS binds host port 53 (UDP + TCP)
- gobgp accesses kernel routing tables
- All containers share the host network namespace

### Steps

1. Create namespaces:

   ```powershell
   kubectl create namespace dcim-site1
   kubectl create namespace dcim-site2
   ```

2. Create enrollment secret (from `enroll-site.ps1` output):

   ```powershell
   kubectl create secret generic collector-enrollment -n dcim-site1 `
     --from-literal=token=<TOKEN> `
     --from-literal=collector_id=<COLLECTOR_ID> `
     --from-literal=site_id=<SITE_ID>
   ```

3. Create TLS secret for Hickory:

   ```powershell
   kubectl create secret generic hickory-tls -n dcim-site1 `
     --from-file=tls.crt=deploy/docker/site-dns/tls/tls.crt.pem `
     --from-file=tls.key=deploy/docker/site-dns/tls/tls.key.pem
   ```

4. Apply site manifests:

   ```powershell
   kubectl apply -k deploy/k8s/site1/
   kubectl apply -k deploy/k8s/site2/
   ```

After ~30s the collectors appear as `healthy` in the UI Collectors page.

---

## Troubleshooting

```powershell
# Pod status
kubectl get pods -n dcim
kubectl describe pod <pod-name> -n dcim
kubectl logs <pod-name> -n dcim

# Connectivity check
kubectl exec -n dcim deploy/api -- nc -zv postgres 5432

# API health
curl http://localhost:30000/health
```

---

## Cleanup

```powershell
kubectl delete ns dcim dcim-site1 dcim-site2
```

---

## References

- [Docker Compose setup](../docker/docker-compose.yml) — Original dev environment
- [Site DNS stack](../docker/site-dns/) — Site collector + DNS services
- [Collectors page](/packages/finch/src/pages/collectors.tsx) — UI for managing collectors
