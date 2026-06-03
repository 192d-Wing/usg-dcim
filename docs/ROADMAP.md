# USG DCIM — Roadmap

> Living document. Captures what's shipped, what's next, and the rough order
> we'd ship features to get from "operator-grade prototype" to "production
> deployment across 184+ sites."

The roadmap is grouped into themed phases. Each phase is roughly two to four
weeks of focused work for a small team and is broadly *parallelizable* — a
backend engineer could be doing scale + multi-tenancy while a UX engineer is
doing operational completeness. The phases are ordered by what unblocks the
most downstream work.

---

## Where we are today

What's shipped on `main`:

### Inventory & physical model

- Multi-site hierarchy: Region → Site → Building → Room → Row → Rack → Asset, with orthogonal SiteGroup tags (MAJCOM/mission/enclave).
- Full Asset model with face (front/rear), mount (rack / 0U vertical), PDU side, PSU count.
- Rack visualization with vendor stencil + colored-block view modes, drag-and-drop reposition, U-grid + vertical PDU side rails, face toggle, redundancy outlines.
- Power chain: Outlet + PowerConnection tables, redundancy classifier (redundant / single / unpowered / n/a), connect/disconnect editor.
- Capacity rollups (U %, kW %, contiguous free runs) per rack + free-space finder page.
- Variable rack heights with orphan-protected shrinks.
- Vendor stencil catalog (procedural SVG by manufacturer + kind, optional `image_url` for real vendor SVGs).
- `Cable` model (a/b ports, medium, color, length, label, face) — backend complete; UI partial (see Phase 3).
- `patch_panel` asset kind with `port_count` — placeholder only; structured port list is Phase 3 work.

### Telemetry & alerting

- Telemetry samples live in a **TimescaleDB `telemetry_samples` hypertable** in the same Postgres database as inventory: monthly chunks, columnar compression after 7 days, 24-month retention, hourly continuous aggregate, freshness tracking. Migration 0046 set this up; the full OpenSearch → Timescale cutover (writes and all three reader paths) completed in PRs #42, #45, #46, #47, #48 and OpenSearch was removed in the follow-up PR.
- Idempotent batch ingest, freshness tracking per device.
- Alert engine: threshold rules with duration, dedup, suppression, maintenance windows, collector-down sweep.
- arq Python worker with cron jobs (alert eval, collector sweep, freshness).
- Go ports of hot loops: `go-ingest`, `go-alerts`, `go-dns-probe` — additive, traffic-shifted at cutover (see [services/README.md](../services/README.md)).
- Full **otter-go** backend at `packages/otter-go/`: the long-running Python→Go migration of the main API has now cut over the Auth, Audit, Admin, Telemetry-read, Search, Inventory, Dashboards, BGP, Alerts, Notifications, and `/dns/bgp-peers` surfaces. A separate `otter-go-scheduler` binary runs six ported cron jobs (DNS purge metrics, freshness sweep, DNS sync from IPAM, DHCP tombstone purge, DNS ZSK rotation, DHCP bundle rerender). See "Python → Go backend migration" below for the full status.
- Notifications: webhook, Slack, email adapters with per-channel fire/resolve filters and severity routing.

### Site collector

- SNMP / Redfish / Modbus TCP / REST / IPMI drivers, store-and-forward SQLite buffer, mTLS or bearer-token auth, retry/backoff.
- Enrollment UX: one-time enrollment token + Docker / systemd bootstrap snippets.

### IPAM & networking

- Fabric → VRF → Supernet (nestable) → Subnet → IPAddress with per-VRF uniqueness, purpose inheritance, IPv6, free-space finder, IP grid, global IP search.
- Kea DHCP lease ingest.
- VXLAN/GENEVE overlay tracking (Overlay → VNI → VTEP), subnet→L2-VNI binding, inline VTEP↔VNI membership editor on the Overlays tab.
- Drag-and-drop subnet reparenting in the supernet tree.

### DNS

- Authoritative + recursive split, Hickory migration on the recursive, DoT/DoH end-to-end, NSEC3, apex DNSSEC delegation, per-fabric CIDR ACLs (with `allow_networks_strict` for Hickory), zone freeze/unfreeze write lock, runtime-editable system upstream forwarders, top-names in query metrics.
- RFC 9432 catalog zones fully shipped (model + renderer + per-fabric AXFR ACL + catalog DNSSEC + UI sub-panel + BIND 9.20 consumer smoke test + RFC 9432 §4.2.3 `primaries` property records).
- Per-neighbor `afi-safis` in the GoBGP config so IPv6 anycast `/128`s actually get advertised.
- RFC 7344 CDS/CDNSKEY auto-propagation (per-zone `publish_cds` opt-out, emit at apex for active KSKs).
- ICMP health-check probes (RFC 792 echo, unprivileged ICMP + CAP_NET_RAW fallback, helm chart wires the cap by default).

### Operator UX (formerly Phase 1)

- Site detail page: KPIs, capacity rollups, hierarchy tree (buildings → rooms → rows → racks), rack tiles.
- Cross-rack/cross-site asset moves with site→rack→U picker, collision detection, overflow validation.
- Maintenance window editor: list + create + edit, scoped to site (asset-filter predicate is a stub — see follow-ups).
- Alert rule editor: metric, operator, threshold, duration, severity, scope (enterprise default vs site override), runbook URL.
- User / Role / ScopeAssignment / OIDC mapping CRUD pages (single Admin page with tabs).
- API token issuance UI: list, issue (one-time plaintext reveal), revoke; capability-gated.
- Bulk CSV import: assets, subnets, IP addresses — drag-drop → parse → validate → preview → import with per-row outcome.
- Decommission workflow: impact preview (power connections + downstream loss-of-power), sanitization note, name-confirmation, audit trail.
- Audit log viewer: server-paged table with filters (action, site, target_type, target_id, actor_label, since / until date range) and per-row detail with diff_json.
- Maintenance window asset filter: `asset_filter_json` predicate applied during alert suppression — operators can suppress on `kind`, `manufacturer`, `model`, `rack_id`, `lifecycle_state` instead of the whole site. Schema validates the key set; fail-safe miss on typos.

### Production hardening (formerly Phase 2)

- GitHub Actions CI: ruff + pytest (backend), vitest (frontend), container builds, Helm lint, alembic up→down→up reversibility gate.
- Real OIDC against Keycloak (with pre-seeded realm in compose): code exchange, JWKS validation, nonce + at_hash, refresh-token rotation, RFC 8176 `amr` MFA.
- Notifications service with webhook / Slack / email adapters and per-channel routing.
- Helm chart at `deploy/helm/dcim/` (api, worker, ingest, frontend, migrations job, NetworkPolicy templates, `values-k3d.yaml`).
- Prometheus middleware in `backend/src/dcim/metrics.py` (HTTP histograms + business counters: telemetry samples, alerts fired, eval runs).
- OpenTelemetry traces for api + worker (off by default; enable via `DCIM_OTEL_ENABLED=true`). FastAPI / asyncpg / httpx auto-instrumented, OTLP/HTTP exporter, local-dev collector behind the `otel` compose profile.
- Frontend bundle split: `React.lazy` per route + `manualChunks` for Refine/RQ/Cloudscape in `vite.config.ts`.
- Dependabot across uv / npm / gomod / docker / actions; CodeQL workflow for Python/JS/Go.

### Repo & ops

- Public repo at <https://github.com/192d-Wing/usg-dcim>.
- docker compose for local dev. Helm chart for k8s with k3d values.
- Audit log on every write.

---

## Open follow-ups (small, non-blocking)

Items that fall out of recently shipped work. None of these gate the next phase.

All three remaining items are blocked on upstream releases. They each have a documented in-tree workaround; revisit when upstream ships.

| Area | Item | Notes |
|---|---|---|
| Hickory DNS | `allow_networks_strict` upstream PR merge | Live pilot on `hickory-prom:v0.26.0-2` accepts the ACL config but the carve-out semantics aren't what operators expect when both allow + deny are non-empty. DCIM-side wiring already passes the strict flag through. PR drafted as commit `0bdf8cd61` on branch `fix/access-allowlist-bypassed-when-deny-nonempty` and submitted upstream. When merged + released, bump `packages/wolf/hickory-prom/` to the new tag and drop the `Dockerfile.local` workaround. |
| BIND interop | `primaries` catalog property support | DCIM emits RFC 9432 §4.2.3 `primaries.<member_id>.zones A/AAAA` records, but BIND 9.20.22 only honors `coo` / `ext` properties — member zones provision as stubs without primaries. Knot DNS 3.4+ and PowerDNS 4.7+ already honor the records. Wait for BIND; until then operators using BIND must declare member zones manually in named.conf. |
| DNS QPS | Per-second rate limiting on the recursive | Hickory 0.26 has no native QPS limiter. The 0037 CIDR ACLs only gate *who* can ask, not *how fast*. Real options today are out-of-band: nftables hashlimit on the recursive host or a dnsdist sidecar. Revisit when upstream lands a token-bucket. |
| Alerting | "Every reading violates" vs MAX(value) semantics | The threshold check uses MAX(value) per asset within `duration_seconds` — fires if *any* reading in the window violates. The file's top comment says "violated for the entire duration", which would imply MIN for `>` and MAX for `<`. Pick one interpretation, document it, and align the SQL. Pre-existing from before the OpenSearch migration; noted in #47. |

---

## Python → Go backend migration (in flight)

Goal: retire `packages/otter/` (Python FastAPI + SQLAlchemy + arq) in favor of
`packages/otter-go/` (Go chi + pgx + sqlc + robfig/cron). Driven by performance,
maintainability, and a smaller deployable surface. Migration runs as
PR-per-endpoint with unit-test parity; each cutover is an ingress flip plus a
Python deletion (no parallel-write window).

The work is **not a discrete phase** — it ran alongside Phase 1-2 from late
2026-Q1 and is roughly 70% complete as of 2026-06. The remaining items here
are the long tail, ordered by what unblocks the most downstream Python
deletion.

### Cut over (Python routers retired)

| Surface | Notes |
|---|---|
| `/api/v1/auth/*` | OIDC, JWKS, refresh, /me — otter-go canonical since PR #179. |
| `/api/v1/audit/*` | Server-paged audit log + filters (PR #180). |
| `/api/v1/admin/*` | Users, roles, scope assignments, OIDC mappings, capabilities/catalog, system DNS settings (PRs #182 + #184). |
| `/api/v1/telemetry/series` | Read-side moved (PR #178); ingest stays on Python via `/api/v1/ingest/telemetry`. |
| `/api/v1/search` | Global search across four buckets + IP-parse path (PR #187). |
| `/api/v1/dashboards/*` | Enterprise + free-space + sites/at-risk + assets/sites/racks detail + 3 forecast endpoints (PRs #188–#194). |
| `/api/v1/inventory/*` | Sites, regions, buildings, rooms, rows, racks, assets — cables PATCH closed the last gap (PRs #195, #197, #198). |
| `/api/v1/lir` + `/api/v1/ipam/supernets/{id}/move` | LIR catalog + tenant supernet relocation (PR 175). |
| `/api/v1/bgp/*` | Full BGP catalog: ASNs, prefix-lists, community-lists, route-maps + entries; TCP-AO keychains + keys + rotate-batch (PRs #203 + #204 + #205 + #206). |
| `/api/v1/alerts/*` | List/ack + rules CRUD + maintenance-windows CRUD (PR #207). Alert evaluation loop still on Python's arq. |
| `/api/v1/notifications/*` | Channels CRUD + test endpoint (PR #215). Channel test reuses the dispatcher service; STARTTLS default fixed in port. |
| `/api/v1/dns/bgp-peers` | Sub-prefix cutover (PR #214). The rest of `/dns/*` still routes to Python. |

### Six cron jobs ported to `otter-go-scheduler`

| Job | Cadence | Notes |
|---|---|---|
| `dns_purge_metrics` | hourly `:23` | Migration 0067 adds a missing index (PR #208). |
| `freshness_sweep` | every 5 min | Constant-memory single-UPDATE instead of Python's load-loop (PR #210). |
| `dns_sync_from_ipam` | every 5 min `:04-:59` | Re-projects IPAM allocations into source=ipam DNS records (PR #211). |
| `dhcp_scope_tombstone_purge` | daily `03:30` | Migration 0068 adds a partial index `WHERE deleted_at IS NOT NULL` (PR #212). |
| `dns_rotate_zsks` | daily `03:17` | Reuses the operator-driven `RotateZoneKey` helper (PR #213). |
| `dhcp_bundle_rerender` | every 2 min | Renders + caches the Kea config bundle per server; HTTP endpoint short-circuits on the cache (PR #219). |

### Remaining Python — ordered by likely cutover

| Priority | Surface | Why it's still on Python |
|---|---|---|
| **High** | DHCP push to Kea (`api/ipam.py /dhcp/scopes/*/push`, `/dhcp/servers/*/sync`, `/dhcp/scopes/*/diff`) | Stateful Kea Control Agent integration (~5000 lines across `services/dhcp_push.py` + `dhcp_drift_summary.py` + `dhcp_reconcile.py`). The bundle work (PRs #216–#220) ported the **read-side**; the **write-side** to Kea is the heavier port. Also pulls in five DHCP arq cron entries (`dhcp_sync`, `dhcp_age_out`, `dhcp_drift_check`, plus the existing `dhcp_scope_tombstone_purge` already on Go, plus `ipam_utilization_sweep`). ~20-25 PRs. |
| **Medium** | Region-deploy lifecycle (6 of 9 routes) | POST create/start/abort/preflight, kubeconfig callback, SSE event stream. Now unblocked because the Go scheduler exists; the SSE endpoint needs a Go-native streaming shape. ~5-10 PRs. |
| **Medium** | DNS module beyond `/dns/bgp-peers` | Zones, records, keys, health-checks, anycast-bindings — large surface but all CRUD. Most handlers are dark code on Go already; mostly an ingress + Python deletion exercise once parity is audited. ~3-5 PRs. |
| **Medium** | `notify_bridge` arq cron | Drains notification events the magpie alerts service pushes onto `dcim:notify:bridge`. Runs every 5s on Python today; would slot into otter-go-scheduler alongside the existing six cron jobs. ~1 PR. |
| **Lower** | IPAM mutation parity | Most ipam mutations are on Go; remaining gaps are FK-validation messages + the `bulk` paths. ~2-3 PRs. |
| **Infrastructure** | Alembic → Go migration tool | 68 versions to migrate or co-own. Atlas, Goose, or hand-rolled. Decision deferred until the Python deletion endgame. |

**Not in this table because they're already shipped on Go**, despite older docs
suggesting otherwise: the alert-evaluation loop and the collector-down sweep
both run in the standalone `packages/magpie/` binary (referenced as
`services/go-alerts` in older READMEs); the DNS health-check probes run in
`packages/beagle/` (`services/go-dns-probe`); telemetry ingest runs in the Go
ingest path. The Python `worker.py` cron registrations for `evaluate_alerts`,
`sweep_collectors`, and `dns_health_checks` are commented out as `# RETIRED`
in lines 558-561. If you're auditing whether a Python `services/*.py` module
is dead code, check the cron list AND the standalone Go binaries before
assuming work is needed.

**Definition of done for the migration:** `packages/otter/` is deleted from
`main`; the umbrella chart no longer mounts the otter container; CI loses the
`otter (ruff + pytest)` job.

---

## Phase 3 — Cabling & physical layer (~3 weeks)

Goal: model the wiring as well as the gear. The `Cable` model and `patch_panel` asset kind landed earlier; this phase finishes the surface.

| Item | Notes |
|---|---|
| **Cable plant UI** | `Cable` model is live and `cable-panel.tsx` exists but is a stub. Build front + rear connection editor. Cable list per rack (front face cables vs rear face cables), with A-end / B-end / port / color / length. |
| **Patch panel port lists** | Patch panels are an asset kind with a `port_count` integer today — no per-port structure. Add a `Port` model (or first-class JSON ports column) keyed by panel + port number; render a port grid in the asset detail. |
| **Network port inventory** | Generic ports on switches/routers; mark which device port each cable connects. Same `Port` model as patch panels. |
| **Cable color conventions** | `color` field exists on Cable; add per-org configurable color → purpose mapping (e.g. blue=mgmt, yellow=production, red=storage). Render as a legend on the cable view. |
| **Cable export** | Generate a per-rack patch list PDF for cabling crews. |

**Definition of done for Phase 3:** for any device you can answer "what's plugged into port X?" without leaving the UI; patch panels show a port grid; cabling crews can print a per-rack patch list.

---

## Phase 4 — Scale & multi-tenancy (~3-4 weeks)

Goal: prove this actually scales to 184 sites and runs cleanly in a multi-tenant deployment.

| Item | Notes |
|---|---|
| **Telemetry retention tuning** | Defaults in migration 0046: monthly chunks, compression after 7d, drop after 24 months. Wire these to Helm values and per-site overrides so air-gapped sites with bigger disks can retain longer. Add a daily continuous aggregate alongside the hourly one for multi-year dashboards. |
| **Alert evaluation at scale** | The current loop is O(rules × assets) per cycle. Move to per-rule scheduled jobs with bounded fan-out, or query the `telemetry_hourly` continuous aggregate for rules with duration_seconds >= 1h to skip the raw hypertable entirely. |
| **Bulk operations at scale** | Pagination is already enforced. Add server-side streaming exports (NDJSON) and chunked imports for 100k+ asset enterprises. |
| **Performance test harness** | A `loadgen` script that simulates 184 collectors × 50 assets × 4 metrics × 30s polls. Runs against the compose stack, reports p95 ingest latency, alert eval lag, dashboard response time. |
| **Per-org tenancy** | An `Organization` model exists but `Site.organization` is a string tag, not a FK. Promote it to a real FK, add an org dimension to ABAC, and gate cross-org reads by default. Default org for single-tenant. |
| **Classification boundary enforcement** | `Site.classification` is a string field today — not enforced anywhere. Wire it through ABAC so a user with an unclassified scope cannot read classified sites' inventory or telemetry. Banner the UI when viewing a classified rack. |
| **Audit log immutability** | Move `audit_log` to an append-only configuration (revoke UPDATE/DELETE for the app role; ship to external WORM store on a schedule). |
| **Postgres HA verification** | Validate Patroni or RDS-style read replica + failover with the chart. Document RPO/RTO. |

**Definition of done for Phase 4:** a synthetic 184-site load test passes p95 budgets; multi-tenant boundaries are enforced; classification banner shows on classified racks.

---

## Phase 5 — Operations & integrations (~3-4 weeks)

Goal: fit into existing enterprise IT workflows.

| Item | Notes |
|---|---|
| **ServiceNow / Jira integration** | Outbound: open a ticket from an alert. Inbound: link a CMDB CI ID to an asset. |
| **Slack / Teams ChatOps** | `/dcim find 4u in CONUS` → bot replies with rack candidates. `/dcim ack 1234` → acknowledges an alert. (Notifications can already *post* to Slack; this is the inbound slash-command direction.) |
| **Reservations** | Tag U-slots as "reserved for X migration through 2026-08-15" without putting a real device there. Surfaces in the rack viz as ghost blocks. |
| **Two-person approval for power-control** | The `power:approve` capability exists but is not enforced. Wire it into `api/power.py` so power-control actions on critical racks require approval by a second user; audit captures both actors. |
| **Scheduled reports** | PDF rack elevations on demand and on schedule. Capacity reports per region. Email or S3 delivery. |
| **Webhook generic outbound** | Generic outbound webhooks keyed off any audit event for downstream automation. (Notifications has webhooks for *alerts* today; this generalizes to any audit event.) |
| **Helm chart parameterization** | values.yaml for air-gapped image mirror, custom OIDC issuer, custom CA bundle, FIPS-mode container variants. |

**Definition of done for Phase 5:** an alert fires → opens a ServiceNow ticket → posts to Slack → operator acks via slash command → PDF report generated for the change ticket.

---

## Phase 6 — Pilot deployment (1-2 sites) (~3-4 weeks)

Goal: a real site (or two) running with real collectors.

| Item | Notes |
|---|---|
| **Air-gapped image mirror playbook** | Pull all images, push to internal Harbor / ECR mirror. Document in `DEPLOYMENT.md`. |
| **STIG-hardened base images** | Replace the python:3.12-slim and node:20 images with hardened internal variants. |
| **Backup + restore runbook** | Postgres logical backup (pg_dump) + a TimescaleDB-aware physical backup (pgbackrest or pg_basebackup) for the telemetry hypertable. Restore drill documented. |
| **Operator training docs** | Quick-start guide. Common workflows with screenshots. Runbook for failed collector. |
| **Lab site cutover** | Pick a low-stakes site. Stand up a real collector. Discover real devices. Iterate on driver gaps. |
| **Pilot site cutover** | Second site, slightly more complex. Capture operator feedback. |
| **Production readiness review** | Security review, performance review, operations review. Sign-off before broad rollout. |

**Definition of done for Phase 6:** two real sites are operating from this platform, with real telemetry, real alerts, and a documented runbook.

---

## Cross-cutting themes (always-on)

These don't fit neatly in a phase but should accumulate continuously:

- **API stability**: as endpoints grow, version aggressively. `/api/v1` is the contract; breaking changes require `/api/v2`. OpenAPI schema diff in CI.
- **Documentation**: every major feature gets a doc in `docs/`. Operator-facing docs separate from contributor docs.
- **Accessibility**: shadcn primitives are accessible by default; we should keep it that way (color contrast, focus rings, keyboard nav).
- **Internationalization**: not urgent for the DoD use case, but design strings to be extractable so we don't have to retrofit later.
- **Telemetry-of-the-platform**: emit our own SLO metrics (auth login success rate, ingest backlog, query p95) so we can dogfood our own alerts.
- **Code quality**: drive SonarLint warnings to zero; add ruff strict rules incrementally.

---

## Out of scope (intentionally)

These come up but we're not chasing them:

- **3D rack rendering** — eye candy, doesn't help operators.
- **Generic ITSM workflow engine** — we integrate with ServiceNow/Jira, we don't replace them.
- **Provisioning / orchestration** (Terraform-style "make me a server") — this is DCIM, not a CMP.
- **Building information modeling** (BIM/CAD) — we model rack-and-roll, not floorplans-and-CAD.
- **Adding a second time-series backend** — telemetry lives in a TimescaleDB hypertable in the same Postgres database as inventory. Don't add OpenSearch / InfluxDB / Prometheus-as-storage alongside it without a concrete need that the hypertable can't satisfy.

---

## Risks & open questions

1. **Telemetry scale** — 184 sites × 50 racks × 20 devices × 5 metrics × 30s polls ≈ 5 million samples/min. The hypertable at this rate needs careful chunk-interval and compression tuning; we should run the load test in Phase 4 before committing to a single-node vs multi-node Postgres topology.
2. **OIDC variability** — DoD environments use different SSO stacks (CAC, SiteMinder, Azure AD). Keycloak is wired and tested; per-environment lab testing is still required.
3. **Air-gapped vs internet-connected sites** — both exist. Collector update mechanism needs to work in both. Probably means an "update bundle" tarball for air-gapped that the operator deploys via configuration management.
4. **Vendor stencil licensing** — procedural stencils are fine, but if we adopt vendor SVGs we need to verify redistribution rights (most vendors allow it, but it's worth a check).
5. **Classification boundaries** — running unclassified, IL5, and IL6 in the same instance is a non-starter. Multi-instance with optional cross-instance read-only federation is the realistic path; needs design.

---

*Last updated: 2026-06-01. Edit me as plans change.*
