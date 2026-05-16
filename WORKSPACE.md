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

## Deferred follow-ups

- **Per-animal Helm charts.** The single `deploy/helm/dcim/` chart still
  groups all services. Splitting per-animal would invalidate every
  existing Helm release and ingress contract; defer until there is an
  operator-facing reason to break the contract.
- **Rename runtime contracts.** Kubernetes Service names (`go-ingest`,
  `frontend`, `api`), image names (`dcim-go-ingest:dev`,
  `dcim-frontend:dev`), Helm value keys (`frontend:`, `api:`), and
  in-cluster hostnames are intentionally still pre-rename. Any switch to
  the animal names is a runtime-breaking change requiring coordinated
  deploys.
- **Comment refs to old paths.** Historical "this Go file is a port of
  `collector/src/dcim_collector/...`" comments in `packages/badger/`
  describe the original location of the Python source at port time and
  were intentionally not rewritten.

## Dev commands

```sh
task otter       # API on :8000 (alias: task backend)
task finch       # Vite dev server on :5173 (alias: task frontend)
task worker      # arq worker
task collector   # legacy Python collector (mole, deprecated)
task test        # otter pytest + finch vitest
```
