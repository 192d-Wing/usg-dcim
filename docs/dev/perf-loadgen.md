# loadgen — synthetic telemetry-ingest load

`packages/loadgen/` is a single-binary load generator that simulates
site-collector telemetry POSTs against [`packages/heron`'s ingest
endpoint](../../packages/heron/main.go). Use it to measure ingest
behavior under load before deciding whether the Phase 4 scale items
(alert eval rearchitecture, telemetry retention tuning) need work.

## What it does

Spawns N concurrent goroutines, one per simulated collector. Each
goroutine POSTs one batch of `assets × metrics` samples to
`/api/v1/ingest/telemetry` every `poll` interval, then records the
HTTP latency. Reports rolling p50 / p95 / p99 every `report`
interval and a final summary on shutdown.

Defaults match the 184-site fleet target from
[`docs/ROADMAP.md`](../ROADMAP.md):

| flag | default | meaning |
|---|---|---|
| `-collectors` | 184 | concurrent collectors |
| `-assets` | 50 | assets per collector |
| `-metrics` | `cpu_pct,mem_pct,disk_pct,temp_c` | 4 metrics per asset |
| `-poll` | `30s` | per-collector poll interval |
| `-duration` | `5m` | total run time |
| `-report` | `15s` | reporter cadence |

At defaults: 184 × 50 × 4 = 36,800 samples per 30s = **~1,227 samples/sec
≈ 73,600 samples/min**. (The roadmap's 5M samples/min target is for the
full 184 × 50 × 20 metrics × 30s ceiling; rerun with `-metrics` widened
to reach that.)

## Build

```sh
cd packages/loadgen
go build -o /tmp/loadgen .
```

## Run against compose stack

The compose stack exposes heron on `localhost:8081`. Bring it up
first ([`docs/DEPLOYMENT.md`](../DEPLOYMENT.md)) then:

```sh
# Quick smoke (laptop-friendly load)
/tmp/loadgen -collectors 10 -assets 5 -poll 5s -duration 30s

# Roadmap-rated load (real numbers; needs decent Postgres)
/tmp/loadgen -collectors 184 -assets 50 -poll 30s -duration 5m

# Authenticated path (when ABAC requires collectors:ingest:write)
/tmp/loadgen -bearer "$(cat ~/.dcim-token)" ...
```

## Reading the output

```
[ 15s] reqs=850 errs=0  p50=  12ms p95=  47ms p99=  89ms max= 142ms  rate=56.6/s  sent=14.3 MB
```

- **`reqs` / `errs`** — total POST attempts / non-2xx + transport
  failures. Non-zero errs needs investigation (heron logs +
  Postgres-side timeouts). Loadgen exits 1 if any errors at final
  summary so CI can gate on this.
- **`p50` / `p95` / `p99` / `max`** — request-side latency over a
  4096-sample rolling reservoir. Targets to validate before the
  Phase 4 scale items become relevant:
  - p95 ≤ 100 ms — alert engine has time to query the
    `telemetry_hourly` continuous aggregate without falling behind.
  - p99 ≤ 250 ms — collector retry queues stay shallow.
  - `max` ≤ 1 s — no individual stalls bad enough to time out a
    collector batch.
- **`rate`** — successful requests/sec, smoothed over elapsed time.
  Should equal `collectors / poll` once steady state is reached.
- **`sent`** — total post-marshal bytes pushed. Useful as a sanity
  check against the ingest container's network metrics.

## What the harness does NOT cover

- **Read load.** Dashboard p95 isn't measured here. Add a parallel
  reader (e.g. `oha` or another loadgen pass against `/api/v1/...`)
  if you need both.
- **Alert eval lag.** The roadmap calls out `O(rules × assets)` as a
  scale concern. Measure separately by running this harness for
  ≥ 5 min then scraping the magpie Prometheus metrics
  (`dcim_alerts_eval_*`) to see eval-loop duration.
- **Real-world distribution.** Every sample is synthetic random data.
  Tests the ingest path's throughput, not its alerting accuracy.

## Operational notes

- The simulator creates `collectors`-many unique site IDs, collector
  IDs, and asset IDs on each run — they don't exist in the database.
  heron writes telemetry samples by `asset_id` without checking the
  asset exists (the telemetry hypertable has no FK), so this works.
  If a future change adds an FK, the loadgen will need to pre-seed
  assets via the otter REST API.
- The compose Postgres on a laptop usually saturates around
  3-5k samples/sec. To exercise the 5M/min target, point loadgen at
  a real-shaped Postgres cluster (RDS / Patroni) running on dedicated
  hardware.
- Reservoir size is 4096 samples — at 1k req/s that's a 4-second
  window. For longer-term percentile stability prefer scraping
  heron's own Prometheus histogram via Grafana.
