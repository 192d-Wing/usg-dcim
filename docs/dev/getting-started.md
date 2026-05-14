# Getting Started — Developer Environment

This guide walks through setting up a full local development environment for USG DCIM, including the central stack, two site collectors with full DNS infrastructure, and seeded demo data.

## Prerequisites

Install the following before proceeding:

| Tool | Version | Notes |
|---|---|---|
| [Podman Desktop](https://podman-desktop.io/) | latest | Enable the built-in Kubernetes cluster in settings |
| kubectl | 1.29+ | Bundled with Podman Desktop or install separately |
| [Node.js](https://nodejs.org/) | 20+ | For running the frontend locally (optional) |
| [Python](https://python.org/) | 3.12+ | For running the backend locally (optional) |
| [uv](https://docs.astral.sh/uv/) | latest | Python package manager: `pip install uv` |
| [PowerShell 7](https://github.com/PowerShell/PowerShell) | 7+ | Required for deployment scripts (`pwsh`) |
| [jq](https://jqlang.org/) | 1.6+ | Used by deployment scripts |

Verify Podman Desktop's k8s cluster is running:

```powershell
kubectl cluster-info
kubectl get nodes
# Should show a single Ready node
```

---

## Clone and Configure

```powershell
git clone https://github.com/192d-wing/usg-dcim.git
cd usg-dcim
```

Add these entries to `C:\Windows\System32\drivers\etc\hosts` (run Notepad as Administrator):

```text
127.0.0.1 dcim.prod.dev.mil
127.0.0.1 keycloak.prod.dev.mil
```

---

## Deploy the Central Stack

### 1. Build container images

```powershell
.\infra\k8s\scripts\build-images.ps1
```

This builds seven images locally using `podman build`:

- `dcim-api:dev` — FastAPI backend + Alembic
- `dcim-frontend:dev` — React UI (nginx)
- `dcim-go-collector:dev` — Site collector agent
- `dcim-go-ingest:dev` — High-volume telemetry receiver
- `dcim-go-alerts:dev` — Alert evaluation loop
- `dcim-go-dns-probe:dev` — DNS health prober

### 2. Deploy to Kubernetes

```powershell
kubectl apply -k infra/k8s/central/
```

Watch pods come up (takes ~2 minutes on first run — Keycloak is slow):

```powershell
kubectl get pods -n dcim -w
```

All pods should reach `Running`:

```text
NAME                          READY   STATUS    RESTARTS
api-xxxxx                     1/1     Running   0
frontend-xxxxx                1/1     Running   0
go-alerts-xxxxx               1/1     Running   0
go-dns-probe-xxxxx            1/1     Running   0
go-ingest-xxxxx               1/1     Running   0
keycloak-xxxxx                1/1     Running   0
postgres-xxxxx                1/1     Running   0
redis-xxxxx                   1/1     Running   0
worker-xxxxx                  1/1     Running   0
```

### 3. Run migrations and seed demo data

```powershell
.\infra\k8s\scripts\migrate-seed.ps1
```

This runs Alembic migrations then seeds the demo dataset:

- **6 sites** across 3 regions: `CONUS-001/002`, `EUCOM-001/002`, `INDOPACOM-001/002`
- **Admin user:** `admin@dcim.local` / `changeme`
- **Per-site inventory:** 4 racks × servers, PDUs, and sensors per site
- **Collector records** for each site (placeholders until real collectors enroll)

---

## Access the UI

The frontend is available at `http://dcim.prod.dev.mil` (NodePort 30080) or `http://localhost:30080`.

**Log in with local credentials:**

- Email: `admin@dcim.local`
- Password: `changeme`

**Log in via Keycloak SSO:**

- Click "Log in with OIDC"
- Keycloak: `http://keycloak.prod.dev.mil:30880`
- Use `dcim_admin` / `dcim_admin` (or `demo` / `demo`)

The API and its OpenAPI docs are at `http://localhost:30000/docs`.

---

## Deploy Site Collectors

Each site runs a full DNS stack: **go-collector + CoreDNS (auth) + Hickory (recursive) + GoBGP**.

### 1. Enroll a collector at a site

```powershell
.\infra\k8s\scripts\enroll-site.ps1 -SiteCode CONUS-001
```

Output:

```text
COLLECTOR_ID:     xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
ENROLLMENT_TOKEN: enroll_xxxxxxxxxxxxxxxxxxxxxxxxxxxxx
SITE_ID:          yyyyyyyy-yyyy-yyyy-yyyy-yyyyyyyyyyyy
EXPIRES_IN:       3600s
```

Repeat for the second site:

```powershell
.\infra\k8s\scripts\enroll-site.ps1 -SiteCode CONUS-002
```

### 2. Create site namespaces and secrets

For each site, substitute the values from the enrollment output:

```powershell
kubectl create namespace dcim-site1

kubectl create secret generic collector-enrollment -n dcim-site1 `
  --from-literal=token=<ENROLLMENT_TOKEN> `
  --from-literal=collector_id=<COLLECTOR_ID> `
  --from-literal=site_id=<SITE_ID>

kubectl create secret generic hickory-tls -n dcim-site1 `
  --from-file=tls.crt=infra/docker/site-dns/tls/tls.crt.pem `
  --from-file=tls.key=infra/docker/site-dns/tls/tls.key.pem
```

Repeat with `dcim-site2` for CONUS-002.

### 3. Deploy the site stacks

```powershell
kubectl apply -k infra/k8s/site1/
kubectl apply -k infra/k8s/site2/
```

After ~30 seconds both collectors appear as **healthy** in the UI under **Site collectors**.

---

## Day-to-Day Development

### Running the backend locally (no container)

```powershell
uv sync --all-packages --all-extras
make backend
# API at http://localhost:8000
```

Point it at the k8s postgres by overriding the DSN:

```powershell
$env:DCIM_POSTGRES_DSN = "postgresql+asyncpg://dcim:dcim@localhost:5432/dcim"
make backend
```

(Expose postgres first: `kubectl port-forward svc/postgres 5432:5432 -n dcim`)

### Running the frontend locally (hot reload)

```powershell
cd frontend
npm install
npm run dev
# UI at http://localhost:5173
```

### Running the background worker locally

```powershell
make worker
```

### Useful make targets

| Command | What it does |
|---|---|
| `make migrate` | Run Alembic migrations (inside k8s api pod) |
| `make seed` | Run demo seed script (inside k8s api pod) |
| `make migrate-local` | Run migrations via `uv` (no k8s) |
| `make seed-local` | Run seed via `uv` (no k8s) |
| `make test` | Run pytest + npm test |
| `make lint` | ruff + eslint |
| `make fmt` | ruff format + prettier |

---

## Rebuilding After Code Changes

After changing backend or frontend code, rebuild the affected image and redeploy:

```powershell
# Rebuild just the API image
podman build -t dcim-api:dev backend

# Roll the deployment to pick up the new image
kubectl rollout restart deployment/api -n dcim

# Rebuild and restart the frontend
podman build -t dcim-frontend:dev frontend
kubectl rollout restart deployment/frontend -n dcim
```

---

## Teardown

Remove all Kubernetes resources:

```powershell
kubectl delete ns dcim dcim-site1 dcim-site2
```

This deletes pods, services, the postgres PVC, and all data. Re-run from [Deploy the Central Stack](#deploy-the-central-stack) to start fresh.

---

## Troubleshooting

### Pod stuck in `Pending` or `ImagePullBackOff`

The images need to be built locally first. Podman Desktop k8s uses images from the local podman store — there is no registry pull.

```powershell
podman images | Select-String dcim
# Verify all dcim-*:dev images are listed
.\infra\k8s\scripts\build-images.ps1
```

### API pod crashes on startup

Check logs and the postgres readiness:

```powershell
kubectl logs deploy/api -n dcim
kubectl exec -n dcim deploy/api -- nc -zv postgres 5432
```

The api pod has an init container (`wait-postgres`) that retries until postgres accepts connections.

### Keycloak `unhealthy` after deploy

Keycloak takes 60–90 seconds on first boot. Wait it out:

```powershell
kubectl logs deploy/keycloak -n dcim -f
# Watch for "Listening on: http://0.0.0.0:8080"
```

### Site collector shows `stale` or `pending`

The enrollment token expires after **1 hour**. If deployment takes longer, re-run enrollment:

```powershell
.\infra\k8s\scripts\enroll-site.ps1 -SiteCode CONUS-001
# Update the secret and restart the site pod
kubectl delete secret collector-enrollment -n dcim-site1
kubectl create secret generic collector-enrollment -n dcim-site1 `
  --from-literal=token=<NEW_TOKEN> ...
kubectl rollout restart deployment -n dcim-site1
```

### Port conflicts on port 53

The site stacks use `hostNetwork: true` and bind port 53. If another DNS resolver (e.g., Windows DNS Client) holds port 53, the site pod will `CrashLoopBackOff`. Disable the Windows DNS Client service or adjust the `DNS_RECURSIVE_BIND_IP` before deploying.

---

## Architecture Overview

See [ARCHITECTURE.md](../ARCHITECTURE.md) for the full system design. In the dev environment:

- The **central stack** (k8s namespace `dcim`) runs all server-side services
- Each **site stack** (namespaces `dcim-site1`, `dcim-site2`) simulates a remote site with a full DNS resolver and collector
- The collector heartbeats to the central API every 30 seconds, receiving any config overrides set via the UI
- DNS zone data flows from the central API → collector → CoreDNS/Hickory on each heartbeat cycle
