# usg-dcim Workspace

Monorepo layout. Each component is a self-contained package under `packages/`,
named after an animal that hints at its role. Deployment wiring lives under
`deploy/`.

## Package map

| Package | Animal rationale | Replaces | Language |
|---|---|---|---|
| `packages/otter/` | Dexterous, central — holds the API | `backend/` | Python (FastAPI + Alembic) |
| `packages/finch/` | Small, fast, user-facing | `frontend/` | TypeScript (Vite + React) |
| `packages/badger/` | Persistent SNMP digger | `services/go-collector/` | Go |
| `packages/heron/` | Wades through streams, ingests | `services/go-ingest/` | Go |
| `packages/magpie/` | Noisy — alerts on anything wrong | `services/go-alerts/` | Go |
| `packages/beagle/` | Sniffs DNS | `services/go-dns-probe/` | Go |
| `packages/mole/` | Burrows into devices (deprecated) | `collector/` | Python |
| `packages/wolf/` | Pack DNS / zone signing tooling | `infra/coredns-nsec3sign/`, `infra/hickory-prom/` | mixed |

## Conventions

- Each package owns its `Containerfile`, manifest (`pyproject.toml` /
  `package.json` / `go.mod`), `README.md`, and `tests/`.
- Shared Go code lives under `packages/<animal>/internal/`; promote to
  `packages/shared-go/` only when 2+ packages consume it.
- Root `Taskfile.yaml` delegates to per-package taskfiles: `task otter:test`,
  `task finch:build`, etc.
- `go.work` lives at the repo root and lists every Go package.

## Deployment

- `deploy/charts/<animal>/` — per-package Helm charts (split from
  `deploy/helm/`).
- `deploy/k8s/` — raw manifests / kustomize bases.
- `deploy/docker/` — compose files and shared base images.

## Migration phases

1. ✅ Skeleton + this doc.
2. ✅ Go services → `packages/{badger,heron,magpie,beagle}/`.
3. ✅ `backend → otter`, `frontend → finch`.
4. ✅ `collector → mole`.
5. ✅ `infra/{docker,k8s,helm} → deploy/`; `infra/{coredns-nsec3sign,hickory-prom} → packages/wolf/`.
6. ✅ Doc cross-links + root Makefile/Taskfile targets.

## Phase 7 — runtime contract rename (BREAKING, done)

Image names, k8s Service names, compose service names, Helm value keys,
and built binary/ENTRYPOINTs all renamed to match the animal packages:

| Old | New |
|---|---|
| `dcim-api` / Service `api` / Helm `api:` | `dcim-otter` / `otter` / `otter:` |
| `dcim-frontend` / `frontend` | `dcim-finch` / `finch` |
| `dcim-go-ingest` / `go-ingest` / Helm `ingest:` | `dcim-heron` / `heron` / `heron:` |
| `dcim-go-alerts` / `go-alerts` | `dcim-magpie` / `magpie` |
| `dcim-go-dns-probe` / `go-dns-probe` | `dcim-beagle` / `beagle` |
| `dcim-go-collector` / `go-collector` | `dcim-badger` / `badger` |
| `dcim-worker` / `worker` | `dcim-otter-worker` / `otter-worker` |
| `/go-collector` ENTRYPOINT | `/badger` (etc.) |

Intentionally **not** renamed:
- Buffer path `/var/lib/dcim-collector` and user `collector` in
  `packages/badger/Containerfile` — preserves site PV/PVC compatibility.
- External DNS hostname `go-ingest.infra.prod.dcim.mil` — operator-managed.
- Comment refs to original paths in `packages/badger/internal/**`
  (historical "this is a port of `collector/src/dcim_collector/...`"
  notes are accurate to the port's source-of-truth at the time).

## Phase 8 — Helm chart split (BREAKING, done)

`deploy/helm/dcim/` is now an **umbrella chart** with one subchart per
animal under `charts/`:

```
deploy/helm/dcim/
├── Chart.yaml                       # v0.2.0
├── values.yaml                      # `global:` + per-animal pass-through
├── values-k3d.yaml
├── templates/                       # cross-cutting: ingress, networkpolicy,
│   ├── ingress.yaml                 #   secrets, alembic migrations Job
│   ├── networkpolicy.yaml
│   ├── secrets.yaml
│   └── migrations-job.yaml
└── charts/
    ├── otter/                       # FastAPI API + Service + HPA
    ├── otter-worker/                # arq worker (otter image, separate scale)
    ├── finch/                       # React UI + Service
    ├── heron/                       # mTLS ingest (Python via uvicorn for now)
    ├── magpie/                      # Go alerts evaluator
    ├── beagle/                      # Go DNS probe (NET_RAW)
    └── badger/                      # Site collector (enabled=false default)
```

Shared values under `global:` (image registry/tag, postgresql/redis DSN,
OIDC, OTEL, podSecurityContext) propagate to subcharts automatically.

## Deferred follow-ups

- **`packages/heron` Go binary not yet wired into the Helm `heron`
  subchart.** The chart still uses the otter (Python) image under
  uvicorn:8443 with mTLS. Switching to the Go binary needs TLS support
  in `packages/heron` first; flip `heron.image` override afterward.

## Dev commands

```sh
task otter       # API on :8000 (alias: task backend)
task finch       # Vite dev server on :5173 (alias: task frontend)
task worker      # arq worker
task collector   # legacy Python collector (mole, deprecated)
task test        # otter pytest + finch vitest
```
