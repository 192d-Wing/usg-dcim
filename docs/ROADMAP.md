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

Shipped through commit `5478494`:

- Multi-site inventory hierarchy: Region → Site → Building → Room → Row → Rack → Asset, with orthogonal SiteGroup tags (MAJCOM/mission/enclave).
- Full Asset model with face (front/rear), mount (rack / 0U vertical), PDU side, PSU count.
- Rack visualization with vendor stencil + colored-block view modes, drag-and-drop reposition, U-grid + vertical PDU side rails, face toggle, redundancy outlines.
- Power chain: Outlet + PowerConnection tables, redundancy classifier (redundant / single / unpowered / n/a), connect/disconnect editor.
- Capacity rollups (U %, kW %, contiguous free runs) per rack + free-space finder page.
- Variable rack heights with orphan-protected shrinks.
- Vendor stencil catalog (procedural SVG by manufacturer + kind, optional `image_url` for real vendor SVGs).
- Telemetry ingest plane (TimescaleDB hypertable with monthly chunks, columnar compression after 7 days, 24-month retention, hourly continuous aggregate, idempotent batches, freshness tracking).
- Alert engine: threshold rules with duration, dedup, suppression, maintenance windows (model only), collector-down sweep.
- arq worker with cron jobs (alert eval, collector sweep, freshness).
- Site collector agent: SNMP / Redfish / Modbus TCP / REST / IPMI, store-and-forward SQLite buffer, mTLS or bearer-token auth, retry/backoff.
- RBAC + ABAC: capability strings, scope dimensions (region/site/group/enclave/org), 9 built-in roles, scoped API tokens.
- Refine + shadcn/ui + Tailwind frontend with real URL routing, react-hook-form + zod, recharts, sonner, lucide.
- Audit log on every write.
- docker compose for local dev. Helm chart skeleton for k8s (untested on a real cluster).
- Public repo at <https://github.com/192d-Wing/usg-dcim>.
- IPAM: Fabric → VRF → Supernet (nestable) → Subnet → IPAddress with per-VRF
  uniqueness, purpose inheritance, IPv6, free-space finder, IP grid, global
  IP search. Kea DHCP lease ingest. VXLAN/GENEVE overlay tracking
  (Overlay → VNI → VTEP), subnet→L2-VNI binding. Drag-and-drop subnet
  reparenting in the supernet tree.
- DNS: authoritative + recursive split, Hickory migration on the recursive,
  DoT/DoH end-to-end, NSEC3, apex DNSSEC delegation, per-fabric CIDR ACLs
  (with `allow_networks_strict` for Hickory), zone freeze/unfreeze write
  lock, runtime-editable system upstream forwarders, top-names in query
  metrics. RFC 9432 catalog zones fully shipped (model + renderer +
  per-fabric AXFR ACL + catalog DNSSEC + UI sub-panel + BIND 9.20
  consumer smoke test + RFC 9432 §4.2.3 `primaries` property records).
  Per-neighbor `afi-safis` in the GoBGP config so IPv6 anycast `/128`s
  actually get advertised. RFC 7344 CDS/CDNSKEY auto-propagation
  (per-zone `publish_cds` opt-out, emit at apex for active KSKs).
  ICMP health-check probes (RFC 792 echo, unprivileged ICMP +
  CAP_NET_RAW fallback, helm chart wires the cap by default).

---

## DNS follow-ups

Items that didn't make the current DNS work but are worth tracking. The
shipped pieces (Hickory migration, apex DNSSEC delegation, DoH/DoT,
NSEC3, top-names in query metrics, per-fabric CIDR ACLs) are already
captured in `docs/dns/` and recent commits.

| Item | Why it matters | Notes |
|---|---|---|
| **Per-second QPS rate limiting on the recursive** | Hickory 0.26 has no native QPS limiter. The 0037 CIDR ACLs only gate *who* can ask, not *how fast*. | Real options today are out-of-band: nftables hashlimit on the recursive host or a dnsdist sidecar. Revisit when upstream lands a token-bucket. |
| **Hickory `allow_networks_strict` upstream PR merge** | Live pilot on `hickory-prom:v0.26.0-2` accepts the ACL config but the carve-out semantics aren't what operators expect when both allow + deny are non-empty. DCIM-side wiring already passes the strict flag through. | PR drafted as commit `0bdf8cd61` on branch `fix/access-allowlist-bypassed-when-deny-nonempty` and submitted upstream. When merged + released, bump `infra/hickory-prom/` to the new tag and drop the `Dockerfile.local` workaround. |
| **BIND `primaries` catalog property support** | DCIM emits RFC 9432 §4.2.3 `primaries.<member_id>.zones A/AAAA` records, but BIND 9.20.22 only honors `coo` / `ext` properties — member zones provision as stubs without primaries. Knot DNS 3.4+ and PowerDNS 4.7+ already honor the records. | Wait for BIND to add `primaries`-property support. Until then operators using BIND must declare member zones manually in named.conf; the smoke test `bind9-smoke/named.conf` documents the workaround. |

---

## Near-term IPAM polish

Small follow-ups to the IPAM/overlay work that's already shipped. None of
these unblock anything else; they close obvious operator gaps.

| Item | Why it matters | Notes |
|---|---|---|
| **VTEP ↔ VNI memberships UI** | The backend `/ipam/vtep-memberships` endpoint exists, but the Overlays tab lists VTEPs and VNIs as separate tables with no way to wire them together. An operator can't currently say "leaf-01 advertises VNI 10010" from the UI. | Add a "VNIs advertised" column to the VTEP table and an "Advertised by" column to the VNI table, both with add/remove. Server-side already enforces same-overlay. |
| **Supernet drag-and-drop reparent** | We ship subnet drag-and-drop but not supernet → supernet moves. Symmetric with the existing UX. | PATCH already supports `parent_supernet_id`; mostly a frontend wiring + cycle-prevention check. |
| **CSV bulk import for subnets / IPs** | Mirrors the existing asset CSV importer. Easiest path to bootstrap from a spreadsheet of existing allocations. | Backend bulk endpoints + a drop-CSV-then-preview UI in the IPAM tree. |

---

## Phase 1 — Operational completeness (~2-3 weeks)

Goal: an operator can do every routine job without dropping to the API.

| Item | Why it matters | Notes |
|---|---|---|
| **Site detail page** | `/sites/:id` currently falls back to the list. Sites are the natural drill-down from search and dashboards. | Show site KPIs (rack count, kW, alert count, collector health), hierarchy tree (buildings → rooms → rows → racks), capacity rollup at site level. |
| **Cross-rack asset moves** | Today drag-and-drop only repositions within a rack. Real moves cross racks (and sometimes sites). | Add a "Move asset" dialog with site→rack→U picker; extend drag-and-drop to drag an asset out of one rack and onto another rack card. |
| **Maintenance window editor** | Model exists, no CRUD UI. Suppresses alerts during planned work — table-stakes. | List + create + edit maintenance windows, scoped to site or asset filter. |
| **Alert rule editor** | Rules can only be created via API. | Form: metric, operator, threshold, duration, severity, scope (enterprise default vs site override), runbook URL. |
| **User / role / scope management UI** | Today users + role assignments are seed-only. | Three small CRUD pages: Users, Roles, ScopeAssignments. |
| **API token issuance UI** | `/auth/tokens` works via API; needs a Settings → Tokens page. | List, issue (returns plaintext once), revoke. Capability-gated. |
| **Bulk import (CSV)** | Backend `POST /inventory/assets/bulk` exists; no UI. Needed for onboarding 184 sites of inventory. | Drag-drop CSV → parse → preview → confirm. Same for sites, racks. |
| **Decommission workflow** | Multi-step retire with sanitization checklist + audit. | Lifecycle state already in the model; add a guided dialog (mark decommissioned → drop power connections → archive). |
| **Audit log viewer** | We record everything. Compliance needs to read it. | Table + filters (actor, action, target, date range). Per-rack and per-asset filtered views. |
| **Collector enrollment UX** | Only API; operators need a "show me the bootstrap command" page. | A page that issues a one-time enrollment token and shows the systemd / docker run command to paste on the site jump host. |

**Definition of done for Phase 1:** an operator can stand up a new site, import its racks, place devices, set power chains, define alert rules, and enroll a collector — all from the UI.

---

## Phase 2 — Production hardening (~2-3 weeks)

Goal: deploy to a real k8s cluster, real auth, real monitoring, real CI.

| Item | Notes |
|---|---|
| **Tests + CI** | We have 2 smoke tests. Add coverage of: RBAC scope evaluation, telemetry ingest pipeline, alert engine fire/suppress/dedup, power-chain redundancy classifier, capacity helper, frontend critical paths via Vitest + @testing-library. GitHub Actions workflow for: ruff + tsc + pytest + vitest + docker build + helm lint. |
| **Real OIDC** | Wire Authlib against Keycloak (or Azure AD) and validate against a Keycloak container in compose. SAML stub second. |
| **Real collector loop** | Drop a `snmpsim-lextudio` container into compose, point one collector at it, prove SNMP polling → buffer → forwarder → ingest → freshness → dashboards. |
| **Notifications** | Outbound webhook on alert.fire / alert.resolve. Slack + email adapters. Per-rule routing. |
| **Helm chart on k3d** | Stand up a k3d cluster locally, install the chart, verify migrations job runs, ingress works, ServiceMonitor scrapes. Document gotchas in `DEPLOYMENT.md`. |
| **Observability** | Add `prometheus-client` middleware (already in deps) for HTTP histograms + business metrics (telemetry samples/sec, alerts fired, ingest batch size). Add OpenTelemetry traces to the API and worker. |
| **Bundle optimization** | Frontend bundle is 1.2 MB. Add route-level `React.lazy` per page; manual chunks for recharts and Refine. |
| **Lint cleanup** | The codebase has accumulated ~100 SonarLint warnings (S6759 readonly props, S3358 nested ternary, S1854 unused, etc.). Knock these down so future warnings stand out. |
| **Dependabot + security scanning** | Enable Dependabot for npm + uv, GitHub CodeQL on push. |
| **Database migrations CI gate** | A workflow that spins up Postgres, runs `alembic upgrade head` from a clean DB, then `alembic downgrade -1` + `upgrade head` to ensure reversibility. |

**Definition of done for Phase 2:** push to main triggers a CI run that lints, tests, builds images, and pushes to ghcr; you can `helm install` to a fresh k3d cluster and end-to-end smoke passes.

---

## Phase 3 — Cabling & physical layer (~3-4 weeks)

Goal: model the wiring as well as the gear. This is the most "DCIM-flavored" feature still missing.

| Item | Notes |
|---|---|
| **Cable plant UI** | `Cable` model is already in the DB. Build front + rear connection editor. Cable list per rack (front face cables vs rear face cables), with A-end / B-end / port / color / length. |
| **Patch panel mapping** | Patch panels as a special asset kind with port lists. Connect device port → patch panel port → uplink. |
| **Network port inventory** | Generic ports on switches/routers; mark which device port each cable connects. |
| **IP address inventory** | A simple IPAM: subnet, IP, allocation to asset. Foundation for DNS integration. |
| **DNS/IPAM integration** | Optional adapter against Infoblox / BlueCat / NetBox for IP truth-of-record. |
| **Cable color conventions** | Per-org configurable color → purpose mapping (e.g. blue=mgmt, yellow=production, red=storage). |
| **Cable export** | Generate a per-rack patch list PDF for cabling crews. |

**Definition of done for Phase 3:** for any device you can answer "what's plugged into port X?" without leaving the UI.

---

## Phase 4 — Scale & multi-tenancy (~3-4 weeks)

Goal: prove this actually scales to 184 sites and runs cleanly in a multi-tenant deployment.

| Item | Notes |
|---|---|
| **Telemetry retention tuning** | Defaults in migration 0046: monthly chunks, compression after 7d, drop after 24 months. Wire these to Helm values and per-site overrides so air-gapped sites with bigger disks can retain longer. Add a daily continuous aggregate alongside the hourly one for multi-year dashboards. |
| **Alert evaluation at scale** | The current loop is O(rules × assets) per cycle. Move to per-rule scheduled jobs with bounded fan-out, or query the `telemetry_hourly` continuous aggregate for rules with duration_seconds >= 1h to skip the raw hypertable entirely. |
| **Bulk operations at scale** | Pagination is already enforced. Add server-side streaming exports (NDJSON) and chunked imports for 100k+ asset enterprises. |
| **Performance test harness** | A `loadgen` script that simulates 184 collectors × 50 assets × 4 metrics × 30s polls. Runs against the compose stack, reports p95 ingest latency, alert eval lag, dashboard response time. |
| **Per-org tenancy** | An `Org` entity that owns Sites + Users + Tokens. Default org for single-tenant. Adds an org dimension to ABAC. |
| **Classification boundary enforcement** | Site.classification is a field today. Enforce that a user with an unclassified scope cannot read classified sites' inventory or telemetry. Banner the UI when viewing a classified rack. |
| **Audit log immutability** | Move `audit_log` to an append-only configuration (revoke UPDATE/DELETE for the app role; ship to external WORM store on a schedule). |
| **Postgres HA verification** | Validate Patroni or RDS-style read replica + failover with the chart. Document RPO/RTO. |

**Definition of done for Phase 4:** a synthetic 184-site load test passes p95 budgets; multi-tenant boundaries are enforced; classification banner shows on classified racks.

---

## Phase 5 — Operations & integrations (~3-4 weeks)

Goal: fit into existing enterprise IT workflows.

| Item | Notes |
|---|---|
| **ServiceNow / Jira integration** | Outbound: open a ticket from an alert. Inbound: link a CMDB CI ID to an asset. |
| **Slack / Teams ChatOps** | `/dcim find 4u in CONUS` → bot replies with rack candidates. `/dcim ack 1234` → acknowledges an alert. |
| **Reservations** | Tag U-slots as "reserved for X migration through 2026-08-15" without putting a real device there. Surfaces in the rack viz as ghost blocks. |
| **Two-person approval for power-control** | Power-control actions on critical racks require approval by a second user with `power:approve`. Audit captures both actors. |
| **Scheduled reports** | PDF rack elevations on demand and on schedule. Capacity reports per region. Email or S3 delivery. |
| **Webhook generic outbound** | Generic outbound webhooks keyed off any audit event for downstream automation. |
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
- **Adding another time-series database** — telemetry already lives in the TimescaleDB hypertable shipped in migration 0046. No need for InfluxDB / Victoria Metrics / Prometheus-as-storage alongside it.

---

## Risks & open questions

1. **Telemetry scale** — 184 sites × 50 racks × 20 devices × 5 metrics × 30s polls ≈ 5 million samples/min. TimescaleDB at this rate needs careful tuning of chunk_time_interval, compression policy timing, and the continuous-aggregate refresh window; we should run the load test in Phase 4 before committing to a single-node vs multi-node topology.
2. **OIDC variability** — DoD environments use different SSO stacks (CAC, SiteMinder, Azure AD). Wiring multiple should be straightforward via Authlib but needs lab testing per environment.
3. **Air-gapped vs internet-connected sites** — both exist. Collector update mechanism needs to work in both. Probably means an "update bundle" tarball for air-gapped that the operator deploys via configuration management.
4. **Vendor stencil licensing** — procedural stencils are fine, but if we adopt vendor SVGs we need to verify redistribution rights (most vendors allow it, but it's worth a check).
5. **Classification boundaries** — running unclassified, IL5, and IL6 in the same instance is a non-starter. Multi-instance with optional cross-instance read-only federation is the realistic path; needs design.

---

*Last updated: 2026-05-12. Edit me as plans change.*
