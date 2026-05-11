# DNS feature roadmap — 10-feature plan

Suggested ship order: light wins first (build velocity, reduce muscle
memory), schema changes mid-roadmap, DNSSEC last (it touches every
render path and has cryptographic state to get right).

## Recommended sequence

1. Audit-log filter for DNS (#9)
2. Conditional forwarders per zone (#10)
3. BIND zone file import (#5)
4. Reverse-zone management UI (#6)
5. Response Policy Zones / RPZ (#3)
6. DDNS from DHCP leases (#4)
7. Query metrics dashboard (#7)
8. Split-horizon views (#2)
9. Health-checked records (#8)
10. DNSSEC signing (#1) — last; depends on stable render pipeline

---

## #9 — Audit-log filter for DNS  *(0.5 day)*

**Scope:** the global audit log already records DNS writes (every API
route calls `audit(...)`); we just don't surface them DNS-scoped.

- **Backend:** none — audit table already includes `resource_type` /
  `resource_id`. Add filter params to the audit list endpoint if not
  already there.
- **Frontend:** new "Activity" tab inside the zone detail view that
  calls `GET /audit/?resource_type=dns_zone&resource_id=…` and renders
  the existing audit-log table component. Same for record-level on a
  record's row-expand.
- **Risk:** very low.

---

## #10 — Conditional forwarders per zone  *(1 day)*

**Scope:** per-zone "forward queries for X to upstream Y" overrides
for the recursive's Corefile, in addition to the global upstreams.

- **Model:** `DnsForwarder { id, fabric_id (FK), zone_pattern (str,
  e.g. "aws.internal."), upstreams (str[] of IP:port), description }`.
  Unique on `(fabric_id, zone_pattern)`.
- **Migration:** create `dns_forwarders`.
- **Service:** `render_corefile_recursive` grows a
  `forwarders: Iterable[Forwarder]` arg; renders one `{pattern}:53 {
  forward . upstream… }` block before the catch-all `.:53`. The
  existing apex-stub blocks remain; conditional forwarders are
  additive.
- **API:** `/dns/forwarders` CRUD.
- **Frontend:** a "Forwarders" section in the Servers tab (fabric-
  scoped).
- **Risk:** low — pure config-render additions.

---

## #5 — BIND zone file import  *(1.5 days)*

**Scope:** operators upload a `.zone` file; backend parses, validates,
and bulk-inserts records into an existing zone (or creates the zone
from `$ORIGIN`).

- **Dependency:** add `dnspython` to backend deps. It can parse a
  BIND zone into structured records with SOA + per-record data.
- **Service:** `parse_bind_zone(text, default_zone=None) -> (zone_meta,
  list[DnsRecord])`. Maps BIND rdata → our JSON `data` shape. Refuses
  unsupported types (DNSKEY, RRSIG until #1 lands).
- **API:** `POST /dns/zones/{id}/import` (multipart text upload).
  Returns `{added, replaced, errors[]}`. Replaces only `source=manual`
  rows by default; `?replace_ipam=true` opts into clearing IPAM rows
  too.
- **Frontend:** "Import zone file" button on the records toolbar opens
  a modal with a textarea + file-drop, shows a diff preview before
  committing.
- **Risk:** medium — BIND syntax has edge cases (line-continuations,
  $INCLUDE, $GENERATE). Skip $INCLUDE / $GENERATE in v1; flag them in
  the diff.

---

## #6 — Reverse-zone management UI  *(1 day)*

**Scope:** reverse zones (`x.y.z.in-addr.arpa`, `n.ip6.arpa`) are
auto-derived today from subnet boundaries but invisible. Surface them
as first-class.

- **Model:** add `DnsZoneKind.reverse`. Per-subnet (vs per-site for
  forward zones). New optional column `subnet_id` on `DnsZone` (FK →
  `Subnet`).
- **Migration:** add enum value + nullable column + sync existing
  reverse-zone records into rows.
- **Service:** the IPAM projector creates a `kind=reverse` zone per
  subnet on first sync. PTR records belong to that zone.
- **API:** zones list already lists everything; add a `kind=reverse`
  filter in the UI.
- **Frontend:** Hosted zones list grows a "Type" facet (apex / site /
  reverse); reverse zones get the same detail view but only allow PTR
  records.
- **Risk:** low-medium — the projector logic needs care so we don't
  double-create.

---

## #3 — Response Policy Zones (RPZ)  *(2 days)*

**Scope:** per-fabric block/redirect lists at the recursive layer.
CoreDNS doesn't ship native RPZ, but its `dnsredir` / `rewrite` /
`template` plugins cover the common patterns; for true RPZ semantics
we'd point an external resolver (Unbound / BIND) — out of scope. Build
the simpler "rewrite" version first.

- **Model:** `DnsBlocklist { id, fabric_id, name, action (block|
  sinkhole|allow), description }` + `DnsBlocklistEntry { id,
  blocklist_id, pattern (FQDN or wildcard), sink_target (IP, nullable)
  }`.
- **Migration:** two new tables.
- **Service:** recursive Corefile picks up blocked patterns and emits
  one of:
  - `template ANY ANY name (pattern) { rcode NXDOMAIN }` for block.
  - `template ANY A name (pattern) { answer "... 60 IN A {sink}" }`
    for sinkhole.
- **API:** `/dns/blocklists` + `/dns/blocklists/{id}/entries` CRUD;
  bulk-add endpoint for threat-feed imports.
- **Frontend:** new "Blocklists" sub-tab; entries table with bulk-add
  textarea.
- **Risk:** medium — pattern format quirks across feeds (raw FQDN vs
  hosts-file format).

---

## #4 — DDNS from DHCP leases  *(1.5 days)*

**Scope:** when DHCP module activates a lease with a hostname, project
an A + PTR record into the matching forward + reverse zones
automatically.

- **Dependency:** the DHCP module emits lease events / has a lease
  table — verify before scoping.
- **Service:** new `dhcp_dns_sync` worker job runs alongside
  `dns_sync_from_ipam`. For every lease with a non-empty `hostname`,
  project A (or AAAA) + PTR with `source=ddns` and a back-pointer to
  `lease_id`. Stale leases delete their projected rows.
- **Model:** add `DnsRecordSource.ddns`; new optional column `lease_id`
  on `DnsRecord`.
- **Migration:** enum value + column.
- **API:** none new; reuses zones / records endpoints.
- **Frontend:** records table grows a "DDNS" source chip; the detail
  view explains how to clear (release the lease).
- **Risk:** medium — depends on DHCP module surface; race conditions
  between lease churn and zone reload.

---

## #7 — Query metrics dashboard  *(1.5 days)*

**Scope:** CoreDNS already exposes Prometheus on `:9153` in both auth
and recursive Corefiles. Pull and chart per-server: QPS, NXDOMAIN
rate, top queried names, response-time histogram.

- **Architecture decision:** scrape from central (each server
  publishes via the GoBGP underlay only — central has no path to
  `:9153`), or scrape from the collector and POST back. Picking
  **collector-scrape + push** keeps central → site connectivity
  asymmetric (collector-initiated, matching every other agent).
- **Backend:** new endpoint `POST /dns/servers/{id}/metrics` for
  collector pushes; metrics stored in TimescaleDB hypertable (if
  installed) or a plain time-series table.
- **Collector:** new `_dns_metrics_loop` polls
  `http://localhost:9153/metrics` per server, parses, posts to central.
- **Frontend:** add a "Metrics" panel below the bundle status on the
  Servers tab — recharts line + a top-names table.
- **Risk:** medium — depends on whether we want full PromQL or just a
  few hard-coded queries.

---

## #2 — Split-horizon views  *(2 days)*

**Scope:** same FQDN, different answers for "internal" vs "external"
clients. CoreDNS supports this via the `view` plugin (CoreDNS 1.10+).

- **Model:** add `DnsView { id, fabric_id, name, match_cidrs (CIDR[]),
  priority }`. Records get an optional `view_id` (nullable = all
  views).
- **Migration:** new table + column.
- **Service:** Corefile rendering emits one `view` block per
  `DnsView`, each scoped by `client_ip { in_range … }`, with its own
  copy of the zones (filtered to records matching that view + null-
  view fallback).
- **API:** `/dns/views` CRUD; record create/edit gets a view picker.
- **Frontend:** zone records list shows a "View" column; create form
  picks the view.
- **Risk:** medium-high — increases rendered Corefile size and zone-
  file proliferation per view.

---

## #8 — Health-checked records / failover routing  *(2.5 days)*

**Scope:** an A record can have multiple targets, each with a health
check; CoreDNS responds only with healthy targets. Subset of Route 53
routing policies.

- **Architecture decision:** CoreDNS doesn't ship native health-
  checked file-plugin records. Two paths:
  - (a) Use CoreDNS's `loadbalance` + `forward health_check` —
    limited to upstream health, not record-level.
  - (b) Move dynamic records into the `redis` plugin and have central
    drive Redis with current-healthy targets. **This is the real
    path.**
- **Model:** `DnsHealthCheck { id, target (IP), protocol (tcp|http|
  https|icmp), port, path, interval_s, timeout_s, healthy_threshold,
  unhealthy_threshold }`. `DnsRecord.health_check_ids: UUID[]` for
  routed records.
- **Worker:** new `dns_healthcheck` cron probes every check; updates
  last-pass timestamp + status; pushes "current view" to Redis.
- **Service:** for records with health checks, render skips the file
  plugin and writes to Redis; CoreDNS reads via the `redis` plugin.
- **API:** `/dns/health-checks` CRUD; record form gets a "Routing
  policy" dropdown.
- **Frontend:** per-record routing config UI.
- **Risk:** high — introduces Redis as a render target alongside the
  file plugin; collector + central both write.

---

## #1 — DNSSEC signing  *(4 days)*

**Scope:** sign each zone with KSK + ZSK; emit signed `.zone` files;
provide DS records for upload to the parent.

- **Model:**
  - `DnsKey { id, zone_id, role (ksk|zsk), algorithm
    (ECDSAP256SHA256|ED25519|RSASHA256), private_key (encrypted-at-
    rest), public_key, tag, active_from, active_until, retired_at }`.
  - `DnsZone.signed: bool`, `DnsZone.nsec3_salt: str | null`,
    `DnsZone.nsec3_iterations: int`.
- **Migration:** new table + columns.
- **Service:**
  - Key generation via `dnspython` (CSR-less; we control both halves).
  - Sign rendered zone file via `dnspython.dnssec.sign_zone()` to
    produce `RRSIG`, `NSEC3PARAM`, `DNSKEY` records.
  - `GET /dns/zones/{id}/ds-records` returns the DS records the parent
    zone's operator uploads.
- **Worker:** key-rotation cron: ZSK pre-publishes 7 days before
  takeover, retires the old ZSK 14 days after the new one is active;
  KSK rotation is operator-triggered.
- **CoreDNS:** auth Corefile uses the `dnssec` plugin pointing at the
  zone-specific key files written into the dns-state volume.
- **API:** `/dns/zones/{id}/enable-dnssec`, `/dns/keys`,
  `/dns/zones/{id}/ds-records`.
- **Frontend:** zone detail gets a "DNSSEC" section — enable toggle,
  key roster with timestamps, DS-record copy block, manual KSK-rotate
  button.
- **Risk:** high — cryptographic state, parent-side coordination,
  render-pipeline changes to *every* zone. Save for after the simpler
  items have hardened the render path.

---

## Cross-cutting concerns

- **Settings:** add `dns_dnssec_default_algorithm`, `dns_ddns_enabled`,
  `dns_metrics_retention_days`. Centralized so operators can pre-set
  per environment.
- **Audit:** every new mutation route calls the existing `audit(...)`
  helper — feature #9 then surfaces them for free.
- **Bundle etag:** today's etag covers Corefile + zone files +
  gobgp.yaml. As features add fields (RPZ blocklists, views, health
  checks → Redis state), include them in the hash so the collector
  doesn't no-op past a real change.
