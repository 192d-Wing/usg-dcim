# Kubernetes Deployment (Podman Desktop)

This directory contains Kubernetes manifests for deploying USG DCIM on Podman Desktop's built-in k8s cluster.

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

- `dcim-api:dev` — FastAPI backend
- `dcim-worker:dev` — Async task worker
- `dcim-frontend:dev` — React UI
- `dcim-go-ingest:dev` — Telemetry ingest service
- `dcim-go-alerts:dev` — Alert evaluation service
- `dcim-go-dns-probe:dev` — DNS health checker
- `dcim-go-collector:dev` — Site collector agent

### 2. Deploy Central Stack

```powershell
kubectl apply -k infra/k8s/central/
```

Deploys: postgres (TimescaleDB), redis, keycloak, api, worker, frontend, go-ingest, go-alerts, go-dns-probe into the `dcim` namespace.

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
     --from-file=tls.crt=infra/docker/site-dns/tls/tls.crt.pem `
     --from-file=tls.key=infra/docker/site-dns/tls/tls.key.pem
   ```

4. Apply site manifests:

   ```powershell
   kubectl apply -k infra/k8s/site1/
   kubectl apply -k infra/k8s/site2/
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
- [Collectors page](/frontend/src/pages/collectors.tsx) — UI for managing collectors
