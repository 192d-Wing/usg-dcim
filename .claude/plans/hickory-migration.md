# Migration plan — Hickory DNS for the recursive (hybrid)

> Goal: move the **recursive** pod at each site from CoreDNS to Hickory.
> The **authoritative** pod stays on CoreDNS — we use split-horizon
> views, on-the-fly DNSSEC signing, and regex template RPZ rules there,
> none of which port cleanly. The recursive workload doesn't use those
> features, and at 30k clients per site (~750k total) the latency-tail +
> per-core throughput advantages of Hickory's no-GC runtime are real.
>
> Status: design + scope confirmed. Execution starts with the engine-
> selector + skeleton Hickory renderer commit.

## Why hybrid

Auth-side and recursive-side workloads differ enough that a single
engine choice doesn't optimize either:

| Pod | Engine | Reason |
| --- | --- | --- |
| Authoritative | CoreDNS | DNSSEC on-the-fly signing, split-horizon `view` plugin, regex-template RPZ — all CoreDNS-only. qps is low (hundreds), so Hickory's perf advantages don't apply. |
| Recursive | Hickory | 30k clients/site puts each recursive node at 10-30k qps sustained, 40-80k qps under boot-storm spikes. Hickory's no-GC tail + lower per-query CPU matter at this load. Native DoH/DoT for clients defaulting to encrypted DNS. None of the features we'd lose (views, on-the-fly signing) are used on the recursive. |

The performance case is real on the recursive side at this scale:

- p99 GC tails in CoreDNS climb to 50-150ms under sustained 30k+ qps;
  Hickory keeps p99 within single-digit ms of p50.
- 2 Hickory nodes per site can carry what previously needed 3
  CoreDNS nodes for headroom — direct compute-cost reduction.
- Boot storms (every host querying the same names at the same time)
  are exactly the workload that triggers CoreDNS's worst GC behavior.

## What lifts directly to Hickory (recursive side)

- **Apex stub forwards** (`<fabric_apex>:53 { forward . <auth>:53 }`):
  becomes a Hickory `[[zones]]` entry with `zone_type = "Forward"`.
- **Conditional forwarders** (per-zone forward to operator upstreams):
  same `Forward`-zone shape.
- **Catch-all upstream** (the recursive's `.:53 { forward . 1.1.1.1
  8.8.8.8 }`): Hickory's recursive store with `forward` mode pointing
  at the upstreams.
- **Health-gated records**: this happens at the *authoritative* render
  side, not the recursive. No change.
- **Per-fabric upstream override** (the
  `Fabric.dns_recursive_upstreams` we just shipped): just feed the
  list into the Hickory config instead of the Corefile.
- **Anycast sidecar** (gobgpd advertising the recursive's anycast IP):
  unchanged — gobgpd runs alongside Hickory the same way it ran
  alongside CoreDNS.

## What changes shape

- **RPZ blocklists**: instead of CoreDNS `template ANY ANY {match ...
  rcode NXDOMAIN}`, render an RPZ-format zone file per blocklist with
  one record per pattern (block ⇒ `CNAME .`, sinkhole ⇒ `A
  <sink_ipv4>`). Hickory loads it via its response-policy
  configuration. Operator UX stays the same — the blocklist + entries
  model is unchanged, only the renderer shifts.
- **Prometheus metrics**: Hickory's metric names differ from CoreDNS's
  (`hickory_dns_*` vs `coredns_dns_*`). Collector parser needs an
  engine-aware path; the central UI / metrics schema doesn't change.

## What we don't carry over (intentionally, recursive only)

- **CoreDNS `view` plugin** — the recursive never used it (views are an
  authoritative-side construct).
- **CoreDNS `dnssec` plugin signing** — the recursive validates DNSSEC
  but doesn't sign. Hickory validates natively.
- **CoreDNS `template` plugin** — only used for RPZ. Replaced by RPZ
  zone-file shape above.

## Execution phases

### Phase 1 — Engine selector + skeleton (1 day)

- Add `Fabric.recursive_engine` enum column (`coredns` | `hickory`),
  default `coredns`.
- Migration adds the enum + column.
- Schema field exposed on `FabricOut` / `FabricUpdate` so operators
  can flip it via the existing Fabric edit form.
- New `render_hickory_recursive_config(...)` in `services/dns.py`
  alongside `render_corefile_recursive`. Initial coverage: apex
  stubs + conditional forwarders + catch-all upstream. RPZ + metrics
  paths come in later phases.
- Bundle assembly picks the renderer based on the fabric's engine.

Deliverable: an opt-in Hickory-rendered recursive bundle. Site stack
work in Phase 2 lets operators actually run it.

### Phase 2 — Site stack option (1 day)

- New `infra/docker/site-dns-hickory/docker-compose.yml` swapping
  `coredns-recursive` for `hickory-dns/hickory-dns`.
- Collector's `dns_agent` recognizes the bundle format and writes
  `<output>/config.toml` instead of `Corefile` when the engine is
  Hickory.
- Reload signal mapping: Hickory uses SIGHUP for config + zone
  reload. The collector's existing SIGUSR1 path stays for CoreDNS;
  add SIGHUP for Hickory.

Deliverable: per-fabric operators can spin up a Hickory recursive
with the same `docker compose up` flow.

### Phase 3 — RPZ renderer (1 day)

- New `render_rpz_zone(blocklist, entries) -> str` in `services/dns.py`
  emitting RFC-2 compliant RPZ zone text.
- Hickory recursive config gets a `response_policy_zones` section
  pointing at one RPZ file per enabled blocklist.
- Bundle assembly grows `rpz_zones: dict[str, str]` for Hickory
  recursives.

### Phase 4 — Metrics parser dual-path (half day)

- Collector's `_parse_prom_text` learns Hickory's metric names
  alongside CoreDNS's, switched by a `metrics_flavor` field on the
  server config (or derived from the fabric's engine).
- Central API schemas unchanged.

### Phase 5 — Documentation + cutover (half day)

- Update `infra/docker/site-dns/README.md` covering both engine
  options.
- Smoke tests against an actual Hickory container.
- Operator runbook for cutover.

**Total: ~5-6 days.** Each phase is reversible per-fabric via the
`recursive_engine` enum.

## Unknowns to validate during Phase 1-2

1. Hickory's `Forward`-zone behavior with port-suffixed upstreams
   (`10.7.0.53:5353`). The config schema needs verifying.
2. Hickory's `health_check` / liveness endpoint — CoreDNS has
   `health :8080`; Hickory may need a TCP probe to :53 instead.
3. Hickory image to use (`hickory-dns/hickory-dns` upstream, or
   build from source for a specific version).
4. Metrics endpoint port + path in Hickory's prometheus feature.

## What stays out of scope

- Migrating the authoritative pod (no benefit, real cost).
- Migrating CoreDNS-only features (view plugin, on-the-fly signing,
  regex templates).
- DoH / DoT — separate ROADMAP item, but Hickory makes it cheaper
  when we get there.
