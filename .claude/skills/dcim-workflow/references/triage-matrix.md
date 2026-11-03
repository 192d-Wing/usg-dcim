# Triage Matrix — which roles run

When in doubt, include the reviewer: a skipped reviewer is a silent gap in ATO
evidence, an extra one costs only tokens.

## Always-on for any code change

- `qa-engineer`
- `security-reviewer`
- `nist-800-53-reviewer`
- `compliance-scribe`

## Path-triggered roles

| Touched path / topic | Add |
|----------------------|-----|
| `packages/otter-go/db/**`, migrations, `cmd/otter-go-migrate` | `db-migration-reviewer`, `release-engineer` |
| `packages/otter-go/internal/**` handlers/routers | `api-contract-reviewer`, `db-migration-reviewer` (if queries change) |
| otter-go auth, tokens, SSO/ICAM, RBAC/ABAC | (always-on; flag AC-2/AC-3/AC-6/IA-2/IA-5 focus) |
| `packages/finch/**` | `api-contract-reviewer` (if it calls new/changed endpoints), `performance-reviewer` (if list/table/rack views change) |
| `packages/heron/**` (ingest) | `performance-reviewer`, `db-migration-reviewer`, `api-contract-reviewer` (collector wire contract) |
| `packages/badger/**` (collector) | `protocol-reviewer`, `performance-reviewer`, `api-contract-reviewer` (wire contract) |
| `packages/magpie/**` (alerts) | `performance-reviewer`, `db-migration-reviewer` |
| `packages/beagle/**`, `packages/wolf/**` (DNS) | `protocol-reviewer` |
| `packages/shared-go/**` | every reviewer of every consumer |
| telemetry hypertable, compression, retention | `db-migration-reviewer`, `performance-reviewer`, flag AU-11 / SI-12 |
| `go.mod`/`go.sum`, `package.json`/lockfile | `release-engineer` |
| `Containerfile`, `deploy/**` (Helm, Flux, k8s, compose) | `release-engineer` |
| `.github/workflows/**`, `Taskfile.yaml`, `Makefile` | `release-engineer` |
| `packages/loadgen/**`, tests only | `qa-engineer` only (+ `performance-reviewer` for loadgen) |

## Phase skips

| Situation | Skip |
|-----------|------|
| Docs / comments only | Phases 1–4; run scribe + `markdownlint` only |
| Bug fix with an obvious one-file cause | Phase 1 may be a short inline plan by the orchestrator |
| Internal refactor, no behaviour/API/schema change | Phase 6 (but still run loadgen if an ingest path is touched) |
| `review` mode | Phases 1–2 |

Record every skipped role or phase, with the reason, in `brief.md`.
