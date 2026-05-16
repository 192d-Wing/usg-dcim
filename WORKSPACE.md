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

1. Skeleton + this doc (no moves). **← current**
2. Go services → `packages/{badger,heron,magpie,beagle}/`.
3. `backend → otter`, `frontend → finch`.
4. `collector → mole`.
5. `infra/ → deploy/`; split charts per animal.
6. Docs, root Makefile/Taskfile, CI workflows.
