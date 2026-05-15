# services/ — Go ports of hot Python paths

Three Go services that replace the highest-throughput / highest-fan-out
loops from the FastAPI backend. Each runs against the **same Postgres
schema, same Redis, same OpenSearch** — no migrations required.

| service        | replaces                                               | port  |
|----------------|--------------------------------------------------------|-------|
| `go-ingest`    | `POST /v1/ingest/telemetry` + freshness upsert        | 8100  |
| `go-alerts`    | `worker.evaluate_rules` + `worker.sweep_collectors`    | 8101 (healthz) |
| `go-dns-probe` | `worker.dns_health_checks`                             | 8102 (healthz) |

## Quick start

```sh
# build + run the three Go services alongside the Python stack
docker compose --profile go-services -f infra/docker/docker-compose.yml up --build
```

Point collectors at `http://go-ingest:8100/v1/ingest/telemetry` (or
`http://localhost:8100` from the host) instead of the API. Wire format
is byte-identical to the Python endpoint.

## Cutover strategy

These services are additive. Cutover happens by **shifting traffic /
disabling the matching Python cron**, not by deploying a breaking
change:

1. **`go-ingest`** — bring it up on a new port; switch collectors over
   one at a time; leave `POST /v1/ingest/telemetry` on the API as a
   fallback until all collectors have flipped.
2. **`go-alerts`** — when this service is running, comment out the
   `evaluate_alerts` and `sweep_collectors` cron entries in
   `backend/src/dcim/worker.py`. Both engines writing to `alerts` at
   once is safe (same dedupe key) but wasteful.
3. **`go-dns-probe`** — comment out `dns_health_checks` in worker.py.
   Same idempotency story.

## Notification bridge (alerts service)

`go-alerts` does evaluation only. Notification dispatch (email, webhook,
PagerDuty, Slack) stays in `services/notifications.py`. To bridge, the
Go service LPUSHes a JSON payload onto the Redis list
`dcim:notify:bridge`:

```json
{"kind": "fire", "alert_id": "9d…"}
```

Add a small ARQ worker function (or a one-shot Python loop in
`worker.py`) that BLPOPs from that key and calls
`notif_svc.dispatch_fire(db, alert)` / `dispatch_resolve(db, alert)`.
Until that bridge exists, alerts still get persisted to Postgres — the
UI sees them — but the external webhooks won't fire from the Go path.

## Auth (ingest only)

`go-ingest` accepts the same `Authorization: Bearer dcim_<token>` API
tokens the Python endpoint uses. The token must have the
`collectors:ingest:write` permission code (wildcard glob like
`collectors:*` or `*` also works, matching `security.deps.
find_matching_capability`). JWTs are rejected — collectors are expected
to use long-lived API tokens, not session JWTs.

The other two services are internal cron workers — no inbound auth.

## What's intentionally NOT here

- **API CRUD surface**, **IPAM/DNS/BGP models**, **migrations**,
  **renderers**, **forecast math** — those stay in Python. See the
  "Tier 3 — do not migrate" section of the original evaluation.
- **Notification delivery** — port later if the dispatch path itself
  becomes a bottleneck; today it's per-alert IO at low rates.

## Building locally without Docker

Each service is its own Go module. From `services/`:

```sh
cd go-ingest    && go build ./...
cd ../go-alerts  && go build ./...
cd ../go-dns-probe && go build ./...
```

`go.work` ties the three together for IDE convenience; production
builds use the per-service Dockerfiles.

## Expected gains (from the evaluation)

| service        | throughput           | RAM per pod         |
|----------------|----------------------|---------------------|
| `go-ingest`    | 5–10× vs. Python     | ~70% reduction      |
| `go-alerts`    | sub-second cycles up to ~1000 rules | minor |
| `go-dns-probe` | 10k probes / 30s on one small pod   | ~80% reduction |

## Known follow-ups

- `go-ingest` doesn't yet integrate with the TimescaleDB hypertable
  added in migration `20260513_0046_timescaledb_telemetry.py`. The
  current write path is ES-only, matching the existing Python service;
  a second write to the hypertable can be added once that migration is
  rolled out and the schema's settled.
- `go-alerts` skips the `asset_filter_json` predicate on rules — the
  current Python implementation does the same, so this is parity, not
  a regression. Filter evaluation should land in both at once.
- ICMP in `go-dns-probe` uses unprivileged SOCK_DGRAM by default. Set
  `DNS_PROBE_ICMP_PRIVILEGED=true` + `NET_RAW` capability to use raw
  sockets on hosts that don't have `net.ipv4.ping_group_range` set.
