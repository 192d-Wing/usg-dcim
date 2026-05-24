# loadgen

Synthetic telemetry-ingest load generator. Spawns N concurrent simulated
collectors that POST batches of samples to heron's `/api/v1/ingest/telemetry`
endpoint, then reports rolling p50/p95/p99 latency.

See [`docs/dev/perf-loadgen.md`](../../docs/dev/perf-loadgen.md) for the
runbook + interpretation guide.

Quick smoke:

```sh
go build -o /tmp/loadgen .
/tmp/loadgen -collectors 10 -assets 5 -poll 5s -duration 30s
```

Defaults target the 184-site fleet (`docs/ROADMAP.md`). Tune
`-collectors`, `-assets`, `-poll` to step the load up or down.
