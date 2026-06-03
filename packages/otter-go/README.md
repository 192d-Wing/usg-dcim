# otter-go

Phase-2 Go port of [packages/otter](../otter). Built around **chi** for
routing, **pgx** for Postgres, and **sqlc** for typed query codegen.

> [!WARNING]
> **Auth is a stub.** Every authenticated request is granted `*`
> capabilities. The process **refuses to start** unless
> `OTTER_GO_INSECURE_AUTH_STUB=true` is set, and every request logs an
> `auth_stub_in_use` warning. Do not unset the env-gate until the real
> OIDC + capability middleware lands (Phase 3 in
> [the migration doc](../../docs/dev/otter-go-migration.md)).

**Status: vertical-slice scaffold.** Routes implemented:
- `GET  /healthz`
- `GET  /readyz`
- `GET  /api/v1/sites`         — list with pagination + filters
- `GET  /api/v1/sites/{id}`    — fetch by id

Everything else is still served by the Python otter. See
[docs/dev/otter-go-migration.md](../../docs/dev/otter-go-migration.md)
for the phased plan to port the remaining 20 routers.

## Layout

```
packages/otter-go/
├── cmd/otter-go/main.go      # entrypoint: chi router, pgx pool, signals
├── internal/
│   ├── auth/                 # bearer-token middleware (capability stub)
│   ├── httpx/                # JSON helpers, error mapping
│   └── sites/                # one resource — handler + SQL-backed store
├── db/
│   ├── sqlc.yaml             # generator config
│   ├── queries/              # raw SQL queries (input to sqlc)
│   └── generated/            # sqlc output (DO NOT EDIT; regen with `sqlc generate`)
├── go.mod
└── Containerfile
```

## Schema ownership

**[cmd/otter-go-migrate](cmd/otter-go-migrate) is the source of truth
for the schema** — a Go binary that embeds 67 goose .sql migrations
and runs them via `goose up`. Alembic was retired in PR #277 along
with the rest of the Python; the bootstrap shim handles the cutover
from the alembic_version table.

## Regenerating queries

Adding a new query? Run `sqlc generate` locally against a Postgres
that's been migrated by `otter-go-migrate`, then commit both the
query and the regenerated `db/generated/`:

```sh
# 1. Spin up Postgres (any way — docker, podman, brew, k3d…) and set
#    DCIM_POSTGRES_DSN to a connection string that can reach it.

# 2. Apply migrations.
go run ./cmd/otter-go-migrate -cmd up

# 3. Dump the schema sqlc needs for type inference.
pg_dump -s -O -x -d "$DCIM_POSTGRES_DSN" > db/schema.sql

# 4. Regenerate.
cd db && sqlc generate

# 5. Commit db/generated/ (schema.sql is gitignored).
```

CI does **not** currently validate sqlc drift — the old `sqlc-drift`
job was waiting on an alembic schema dump that never landed, and
after the alembic→goose cutover the dump path is gone entirely. A
Postgres-in-CI replacement (service container + the workflow above)
is a tracked follow-up; until it lands, the contract is "regenerate
before you commit query changes".
