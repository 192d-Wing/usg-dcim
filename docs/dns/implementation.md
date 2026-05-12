# DNS implementation and features guide

For the engineer about to add a feature, debug a regression, or
review a PR. Covers the code layout across backend / frontend /
collector / custom CoreDNS plugin, the major service-level
contracts, and the test posture.

The deployment + secret-management story is in the [admin
guide](admin-guide.md); the day-to-day UI workflows are in the
[operator guide](operator-guide.md); the high-level design
decisions and tradeoffs are in
[../design/dns-integration.md](../design/dns-integration.md).

## Code layout

```text
backend/src/dcim/
├── models/dns.py          SQLAlchemy ORM — DnsZone, DnsRecord, DnsKey,
│                          DnsServer, DnsView, DnsHealthCheck,
│                          DnsBlocklist, DnsForwarder, AnycastGroup,
│                          BgpPeer, AnycastBgpBinding
├── schemas/dns.py         Pydantic surface — *Create / *Update / *Out
│                          per type, plus discriminated unions on
│                          DnsRecord.data per record-type
├── services/dns.py        Render functions (pure) + DB helpers (async)
├── api/dns.py             FastAPI routes — /dns/zones, /records,
│                          /servers, /keys, /enable-dnssec,
│                          /nsec3, /sync-from-ipam, /bundle, etc.
├── worker.py              arq cron functions — dns_sync_from_ipam,
│                          dns_rotate_zsks, dns_collect_metrics,
│                          dns_drop_old_metric_samples
└── migrations/versions/2026*_dns*.py + nsec3_params.py

collector/src/dcim_collector/
├── dns_agent.py           Polls /dns/servers/{id}/bundle, writes
│                          Corefile + zone files + GoBGP yaml,
│                          signals reload
└── config.py              Adds dns: block to the collector schema

frontend/src/components/
└── dns-tab.tsx            Single big file with Zones, Records,
                           Servers, Forwarders, Blocklists,
                           Health-checks sub-panels + Zone detail
                           with DNSSEC / Activity / Preview / Views
                           tabs

infra/coredns-nsec3sign/
└── nsec3sign/             Go module — custom CoreDNS plugin for
                           on-the-fly NSEC3 signing (RFC 5155)
infra/docker/site-dns/      Compose stack for one site
```

## Backend

### Models — `services/dns.py` consumes them, not the API

[`models/dns.py`](../../backend/src/dcim/models/dns.py) mirrors
the conventions in [`models/ipam.py`](../../backend/src/dcim/models/ipam.py):
`UUIDPrimaryKey` + `Timestamped` mixins, str-enum columns with
`values_callable=lambda x: [e.value for e in x]`, denormalized
fabric/site FKs for fast filters. Six top-level tables plus
binding tables for many-to-many edges.

| Model              | Key columns                                                              |
| ------------------ | ------------------------------------------------------------------------ |
| `DnsZone`          | name, kind, fabric_id, site_id, soa_*, default_ttl, signed, zsk_rotation_days, nsec3_salt, nsec3_iterations, nsec3_opt_out |
| `DnsRecord`        | zone_id, name, type, data (JSON), source (manual/ipam/ddns), view_id, health_check_id, ipam_address_id |
| `DnsKey`           | zone_id, role (ksk/zsk), algorithm, private_pem (Fernet at rest), public_key_b64, key_tag, active_from, active_until, retired_at |
| `DnsServer`        | name, site_id, fabric_id, role (auth/recursive), unicast_ip, last_render_at/status/error/etag |
| `DnsView`          | name, fabric_id, match_cidrs, priority, description |
| `DnsHealthCheck`   | name, fabric_id, target_ip, protocol, port, path, interval_seconds, status |
| `DnsBlocklist`     | name, fabric_id, action (nxdomain/nodata/sinkhole), sinkhole_ip |
| `DnsForwarder`     | fabric_id, zone_pattern, upstreams |
| `AnycastGroup`     | service, fabric_id, anycast_ipv4, anycast_ipv6 |
| `BgpPeer`          | site_id, local_asn_id, peer_asn_id, peer_ip, md5_password, tcp_ao_keychain_id |

Composite indexes line up with the access patterns the renderer
needs: `(zone_id, name, type)` on records, `fabric_id`/`site_id`
prefixes on zones/servers/etc.

### Schemas — discriminated unions on `DnsRecord.data`

The Pydantic side enforces type-safety at the API boundary so the
renderer can trust the shape downstream. Each record type gets
its own `_*Data` schema (`_ARData`, `_MxData`, `_SrvData`, …) and
`DnsRecord.data` is the discriminated union over them keyed by
`type`. Malformed payloads fail at the FastAPI dependency layer
with a 422; the renderer never sees them.

The NSEC3 params schema (`DnsZoneNsec3Params`) validates salt is
hex (`pattern=^([0-9a-fA-F]{2})*$`), max-length 64 chars (32
bytes), iterations 0–150. Defaults match RFC 9276.

### Services — pure render functions

[`services/dns.py`](../../backend/src/dcim/services/dns.py) is
the workhorse. Layout: pure render functions at the top
(unit-testable without Postgres), DB-touching helpers below them,
crons at the bottom. The pure functions:

- `render_zone_file(zone, records, *, unhealthy_check_ids)` —
  BIND-format zone with deterministic ordering.
- `render_corefile_auth(zone_names, *, zones_dir, keys_dir,
  dnssec_keys_by_zone, nsec3_params_by_zone, views_by_zone)` —
  Corefile for the authoritative pod. Per-zone block selects
  `dnssec { … }` (NSEC) vs `nsec3sign { … }` (NSEC3) via the new
  `_render_signing_block` helper.
- `render_corefile_recursive(fabric_apexes, auth_unicast_ip,
  upstream_resolvers, forwarders, blocklists)` — Corefile for the
  recursive pod (with stub-zone forwards to local auth + RPZ-lite
  templates for blocklist entries).
- `render_gobgp_config(server, peers, peer_asns, anycast_group)` —
  GoBGP YAML for the anycast announcer.
- `render_dnssec_key_files(zone, keys)` — BIND-format
  `K<zone>+<alg>+<tag>.{key,private}` files. Plain text on disk
  for CoreDNS to read; the in-DB form is Fernet-encrypted when
  `dns_dnssec_secret` is set.
- `render_ds_records(zone, keys)` — DS records (digest type 2 =
  SHA-256) for parent-zone upload.

`render_bundle_for_server(server)` is the integration point that
the API exposes — assembles Corefile + zone files + GoBGP YAML +
key files into a single object with an etag.

The async DB-touching helpers (`_dnssec_artifacts_for_zones`,
`_local_auth_unicast_ip`, `_views_and_zone_files_for_split_horizon`,
`_bgp_for_server`) live below the pure functions, named with a
leading underscore so the test surface is obvious.

### API — `/dns/...` is one router

[`api/dns.py`](../../backend/src/dcim/api/dns.py) is the FastAPI
router for everything DNS. Routes group as:

- **Zones**: `/zones` CRUD + `{id}/sync-from-ipam` +
  `{id}/preview` + `{id}/import` + `{id}/events` + `{id}/keys`
  + `{id}/ds-records` + `{id}/enable-dnssec` + `{id}/disable-dnssec`
  + `{id}/rotate-key/{role}` + `{id}/nsec3` (POST/DELETE).
- **Records**: `/records` CRUD with discriminated-union validation
  on `data`.
- **Servers**: `/servers` CRUD + `{id}/bundle` + `{id}/render-status`
  + `{id}/metrics`.
- **Anycast / BGP**: `/anycast-groups`, `/bgp-peers`,
  `/bgp-peers/{peer_id}/bind/{server_id}` + corresponding DELETE.
- **Views**: `/views` CRUD.
- **Health checks**: `/health-checks` CRUD + `/results`.
- **Forwarders**: `/forwarders` CRUD.
- **Blocklists**: `/blocklists` CRUD + `/blocklists/{id}/entries`
  + bulk import.
- **Keys**: `/keys/{id}` DELETE (purge a retired key).

Every mutation route runs through `audit.record(...)` so the
Activity tab can surface who did what. `_touch_zone(db, zone_id)`
bumps the zone's `updated_at` so the SOA serial advances and the
collector's etag check picks up the change.

### Worker crons

Four DNS-related arq jobs in [`worker.py`](../../backend/src/dcim/worker.py):

| Cron                          | Cadence    | What it does |
| ----------------------------- | ---------- | ------------ |
| `dns_sync_from_ipam`          | every 5 m  | Walks fabrics, projects `IPAddress.dns_name` → A/AAAA/PTR records (`source=ipam`). Idempotent — only touches `source=ipam` rows. |
| `dns_rotate_zsks`             | daily      | Looks for zones with `zsk_rotation_days > 0` whose active ZSK is older than that, rotates them, marks the previous one retired. |
| `dns_collect_metrics`         | every 1 m  | Scrapes the `:9153/metrics` endpoint on each DnsServer, parses CoreDNS counters, writes samples to `dns_server_metrics_samples`. |
| `dns_drop_old_metric_samples` | hourly     | Deletes samples older than `dns_metrics_retention_days`. |

## Collector

[`collector/src/dcim_collector/dns_agent.py`](../../collector/src/dcim_collector/dns_agent.py)
adds a third concurrent loop to the collector (alongside
`_device_loop` and `_drain_loop`):

1. Every `dns_poll_interval` (default 30 s), call `GET
   /dns/servers/{id}/bundle?etag=<last>`.
2. Etag-match → no-op.
3. Otherwise atomically write `Corefile`, each
   `zones/<name>.zone`, `keys/K<zone>+<alg>+<tag>.{key,private}`,
   and `gobgp.yaml` into the shared `dns-state` volume.
4. `kill -SIGUSR1 <coredns-pid>` (auth + recursive) to hot-reload.
   `kill -SIGHUP <gobgp-pid>` (when set) to reload BGP config.
   PIDs come from pidfiles each container drops on the shared
   volume.
5. POST `/dns/servers/{id}/render-status` with success/failure +
   etag.

The collector container does **not** need the docker socket —
render-and-signal works because all four containers share the
host PID namespace via the compose's `pid: "host"` directive.

Auth model unchanged from the rest of the collector: bearer token
at `/etc/dcim/token`, mTLS to the central ingest endpoint.

## coredns-nsec3sign plugin

The custom CoreDNS plugin under
[`infra/coredns-nsec3sign/`](../../infra/coredns-nsec3sign/) adds
on-the-fly NSEC3 signing — the missing-from-upstream feature that
makes NSEC3 zones possible without pre-signing the zone file. Its
own README + SECURITY-REVIEW.md cover the details; this section is
a quick orient.

### File layout

```text
nsec3sign/
├── setup.go        Corefile parser + Caddy registration
├── nsec3sign.go    plugin.Handler — ServeDNS interceptor
├── keys.go         BIND-format DNSKEY pair loader
├── zone.go         BIND zone file → owner-name set (with ENT synthesis)
├── chain.go        Sorted NSEC3 hash chain + matching/covering lookups
├── signer.go       RRSIG generation + signing-key selection
├── sigcache.go     LRU signature cache + 75% expiration janitor
├── denial.go       NXDOMAIN / NODATA / delegation / wildcard proofs
├── metrics.go      Five Prometheus metrics
└── *_test.go       70+ subtests across all files
```

### Request flow

1. **`ServeDNS`** ([`nsec3sign.go`](../../infra/coredns-nsec3sign/nsec3sign/nsec3sign.go))
   short-circuits when (a) QNAME is outside configured zones, (b)
   the EDNS0 DO bit is clear, or (c) no keys loaded.
2. Otherwise intercepts the downstream response via
   `plugin/pkg/nonwriter`.
3. **`attachDenialProof`** ([`denial.go`](../../infra/coredns-nsec3sign/nsec3sign/denial.go))
   classifies via `response.Typify` and attaches NSEC3 records:
   - NXDOMAIN → closest-encloser proof (matching encloser + covering
     next-closer + covering wildcard)
   - NODATA → matching NSEC3 with the type bitmap that omits the
     queried qtype
   - Delegation → matching NSEC3 (with NS/DS bitmap) or covering
     NSEC3 with opt-out flag
4. **`attachWildcardProof`** scans answer RRsets for wildcard
   expansions (via `chain.wildcardSource`) and attaches the
   covering NSEC3 for the next-closer name (RFC 5155 §7.2.4).
5. **`signMessage`** ([`signer.go`](../../infra/coredns-nsec3sign/nsec3sign/signer.go))
   walks Answer + Ns, groups by (name, type, class), and emits
   one RRSIG per applicable key. Wildcard-expanded RRsets are
   signed over a cloned RRset with the wildcard owner so
   `miekg/dns.RRSIG.Sign` computes `Labels` correctly; the
   resulting RRSIG's `Hdr.Name` is patched back to the qname.
6. RRSIGs route through the signature cache
   ([`sigcache.go`](../../infra/coredns-nsec3sign/nsec3sign/sigcache.go))
   on the hot path — fnv-64a keyed by RRset presentation form,
   evicted at 75% of validity.
7. Final `WriteMsg` to the real ResponseWriter.

### Chain population

[`zone.go`](../../infra/coredns-nsec3sign/nsec3sign/zone.go) parses
the same BIND zone file the parent `file` plugin reads (via
`miekg/dns.ZoneParser`, the same parser `file` uses internally —
no coupling to CoreDNS internals). `synthesizeENTs` walks each
explicit owner's ancestor labels and emits ENT nodes with empty
type bitmaps — required for RFC 5155 §7.2.2 NODATA-at-ENT and for
deep-wildcard detection in `chain.wildcardSource`.

The `chain.go` builder hashes every name via
`miekg/dns.HashName(name, 1, iterations, salt)` (lowercased — the
RFC and `dig` show lowercase, miekg/dns returns uppercase
internally), sorts by hash for O(log n) lookups, and stashes
salt/iterations/opt-out for parameter-change detection on reload.

### Wildcard detection

`chain.wildcardSource(owner)`:

1. If `matchingNSEC3(owner) != nil` → concrete, no wildcard.
2. Walk to `findClosestEncloser(owner)` and check whether
   `*.<encloser>` is in the chain.
3. If yes, return that wildcard name. Two consumers: the signer
   uses it to rewrite the RRset's owner so miekg/dns sets
   `RRSIG.Labels` correctly; `attachWildcardProof` uses it to
   decide whether to emit the §7.2.4 covering NSEC3.

The detection is correct only when intermediate-label ENTs are in
the chain — `synthesizeENTs` is the prerequisite. Without it,
`findClosestEncloser` for `printer.dev.example.test.` would
climb past `dev.example.test.` (no records, not in chain) and
land at the apex, then look for `*.example.test.` (the wrong,
less-specific wildcard).

### Custom CoreDNS build

The plugin is compiled INTO CoreDNS, not loaded at runtime.
[`Dockerfile`](../../infra/coredns-nsec3sign/Dockerfile) clones
CoreDNS at the pinned version, splices
`nsec3sign:github.com/192d-wing/coredns-nsec3sign/nsec3sign` into
the plugin chain immediately after `dnssec` (the existing NSEC
plugin's slot), points the module graph at our local source via
a `replace` directive, and runs the upstream's `go generate && go
build` sequence. The resulting binary is a drop-in for
`coredns/coredns:1.14.2` — same entrypoint, same `-conf` flag,
same listening ports.

## Frontend

[`frontend/src/components/dns-tab.tsx`](../../frontend/src/components/dns-tab.tsx)
holds everything. Single file because the panels share a lot of
state (fabric scope, react-query keys, common types). Top-level
flow:

1. **`DnsTab({ canWrite })`** is the entry point — tabs across
   Zones / Records / Servers / Forwarders / Blocklists /
   Health-checks. Fabric scope from `useFabricScope()`.
2. **`ZonesListView`** lists zones for the fabric. Click a zone
   → `ZoneDetailView`.
3. **`ZoneDetailView`** has its own sub-tabs: Records, Activity,
   DNSSEC, Preview, Views, Health-checks.
4. **`ZoneDnssecTab`** owns the DNSSEC + NSEC3 UI:
   `KeyValuePairs` header summary, `ZoneNsec3Panel` (separate
   subcomponent), Keys table, DS records table.
5. **`ZoneNsec3Panel`** — read-only `KeyValuePairs` for
   non-write operators, three-field form (salt / iterations /
   opt-out) for write-capable users, with client-side validation
   matching the backend regex/bounds.

Data layer: `@refinedev/core` for paginated list endpoints,
`@tanstack/react-query` for everything else. Mutations always
end with `qc.invalidateQueries({ queryKey: [...] })` to refetch
the now-stale views.

Cloudscape (AWS Cloudscape Design) is the component library. The
DNSSEC tab uses `Container`, `Header`, `KeyValuePairs`, `Table`,
`Badge`, `Button`, `Input`, `FormField`, `ColumnLayout`,
`Checkbox`, `Modal`, and `Tabs` — typical Cloudscape kit.

## Testing posture

### Backend

```bash
uv run --with pytest python -m pytest backend/tests/test_dns_render.py -v
```

18 subtests covering:

- Zone-file rendering (origin, TTL, SOA, ordering, MX/TXT/SRV
  formats)
- Corefile-auth blocks (basic, NSEC dnssec block, NSEC3
  nsec3sign block, opt-out, mixed NSEC + NSEC3 zones)
- Corefile-recursive (apex stubs, fallback upstreams,
  conditional forwarders, blocklist templates)
- GoBGP config
- Bundle etag stability

Render tests use `SimpleNamespace` zone/record objects — no
Postgres, no fixtures, deterministic. Run them on every PR.

API tests for the DNS routes are NOT in the suite today —
they'd require a test DB. Run end-to-end via the dcim-bundle-smoke
harness instead (below).

### Plugin

```bash
cd infra/coredns-nsec3sign
go test ./nsec3sign/... -count=1
```

70+ subtests across parser, key loader, chain (including all RFC
5155 Appendix B vectors), signer (RRSIG round-trip with
`RRSIG.Verify` across ECDSA-P256 + Ed25519 + RSA), denial proofs
(NXDOMAIN closest-encloser, NODATA, delegation secure/insecure/
opt-out, wildcard expansion), cache (hit/miss/eviction/janitor),
zone-file ingestion (apex detection, ENT synthesis, opt-out
detection, $INCLUDE rejection).

Coverage 87.4% — the uncovered statements are in `setup()` (the
Caddy entry point, hard to unit-test without spinning the whole
framework).

### Wire-level smoke

Three harnesses under
[`infra/coredns-nsec3sign/examples/`](../../infra/coredns-nsec3sign/examples/):

- **`quick-smoke`** — hand-rolled Corefile + zone + keys, useful
  for validating plugin changes in isolation against a locally-
  built image.
- **`dcim-bundle-smoke`** — drives the DCIM Python renderer end-
  to-end (zone + key + Corefile all generated by
  `backend/src/dcim/services/dns.py`) and runs the locally-built
  CoreDNS image against the output. Tests the renderer-plugin
  interface.
- **`comprehensive-test`** — eight-scenario harness exercising
  every code path the plugin handles (positive, NXDOMAIN, NODATA,
  wildcard, ENT NODATA, deep ENT, secure delegation, insecure
  delegation) against the **GHCR-published image** (not a local
  build). Use after every plugin tag bump as the deploy-validation
  step. README has a coverage-to-fix map linking each scenario to
  the S-0X finding it exercises.

Each harness has its own README walking through the bring-up.
All three produce `dig +dnssec` output operators can verify
visually.

## Extension points

### Adding a new record type

1. Add the type to `DnsRecordType` enum in
   [`models/dns.py`](../../backend/src/dcim/models/dns.py).
2. Add a `_<Type>Data` Pydantic schema in
   [`schemas/dns.py`](../../backend/src/dcim/schemas/dns.py) and
   wire it into the discriminated union.
3. Add a `_format_record_line` branch in
   [`services/dns.py`](../../backend/src/dcim/services/dns.py)
   that emits the BIND presentation form.
4. Add a render test in `backend/tests/test_dns_render.py` that
   pins the line format.
5. Add the type to the frontend record form in
   [`dns-tab.tsx`](../../frontend/src/components/dns-tab.tsx)
   under the type-specific form switch.

### Adding a new DNSSEC algorithm

1. Add the algorithm to `DnsKeyAlgorithm` enum in
   [`models/dns.py`](../../backend/src/dcim/models/dns.py).
2. Add the algorithm number to `_DNSSEC_ALG_NUMBER` in
   [`services/dns.py`](../../backend/src/dcim/services/dns.py).
3. Implement the key-generation branch in
   `generate_dnssec_keypair`.
4. Implement the BIND-private rendering in
   `_bind_private_key_file`.
5. The plugin side is algorithm-agnostic — it routes through
   `miekg/dns` which handles ECDSA / Ed25519 / RSA + variants.

### Adding a Corefile directive

1. Add the keyword to `directiveParsers` map in
   [`infra/coredns-nsec3sign/nsec3sign/setup.go`](../../infra/coredns-nsec3sign/nsec3sign/setup.go).
2. Write the handler function (small, validates the arity + type
   + bounds, stores on `Nsec3Sign`).
3. Add a test case to `TestParseValid` (or `TestParseInvalid`
   for bad-input paths).
4. Plumb the field through to wherever it's consumed at runtime.

### Adding a new cron

1. Define the function in
   [`worker.py`](../../backend/src/dcim/worker.py) with `@arq.cron`.
2. Add a settings flag if the operator should be able to disable
   or retune the cadence.
3. Add a sanity test in `backend/tests/test_dns_render.py`-style
   that drives the function with a synthetic DB-free fixture.

### Bumping CoreDNS

The plugin links against CoreDNS at build time, so bumping the
upstream version means touching the Go module + Dockerfile +
docs in one commit. The mechanical steps:

1. `git ls-remote --tags https://github.com/coredns/coredns` —
   confirm the target tag actually exists.
2. Bump `ARG COREDNS_VERSION` in
   [`Dockerfile`](../../infra/coredns-nsec3sign/Dockerfile).
3. Bump `COREDNS_VERSION` in
   [`Makefile`](../../infra/coredns-nsec3sign/Makefile).
4. Bump `github.com/coredns/coredns` in
   [`go.mod`](../../infra/coredns-nsec3sign/go.mod).
5. `go mod tidy` — this resolves the indirect deps and may force
   a Go-toolchain bump if the new CoreDNS pulled in modules
   requiring a newer minimum. Mirror that in `ARG GO_VERSION` on
   the Dockerfile.
6. `go test ./nsec3sign/... -count=1` — surfaces any API breaks
   immediately. CoreDNS has historically broken plugin-facing APIs
   across minor versions; expect to do real adaptation work.

**Known API change to watch for**: CoreDNS made
`plugin/pkg/cache.Cache` generic (`Cache[T any]`) between v1.11.x
and v1.14.x. Our cache stores `[]dns.RR`; the type parameter
flows through five call sites (struct field, `cache.New`, two
function signatures in `sigcache.go`, the `Walk` callback's map
type, and the `.([]dns.RR)` assertion on `Get` which becomes
unnecessary). Look at commit `46330b0` for the full diff.

After the build is green, rebuild the image, retag as
`<new-version>-1`, push to GHCR, and re-run the
`comprehensive-test` harness against the pulled image to confirm
all eight wire-level scenarios still pass.

## Where the lines are

- **Pure render** in `services/dns.py` — no DB, no FastAPI, no
  IO. Test without spinning anything up.
- **DB helpers** in `services/dns.py` below the pure functions —
  named `_*`. Take an `AsyncSession`.
- **API surface** in `api/dns.py` — every mutation audited, every
  handler small enough to read in one screen.
- **Plugin chain** in `infra/coredns-nsec3sign/nsec3sign/` —
  separate Go module, vendors against CoreDNS via a `replace`
  directive at build time. No Python.
- **Frontend** is single-file by design (`dns-tab.tsx`) — the
  panels share enough state that splitting them across files adds
  more friction than it saves. If it grows past ~4000 lines,
  reconsider.

## Pointers

- Architecture decisions: [../design/dns-integration.md](../design/dns-integration.md)
- Deployment + secrets: [admin-guide.md](admin-guide.md)
- UI workflows: [operator-guide.md](operator-guide.md)
- Plugin internals: [../../infra/coredns-nsec3sign/README.md](../../infra/coredns-nsec3sign/README.md)
- Plugin security review: [../../infra/coredns-nsec3sign/SECURITY-REVIEW.md](../../infra/coredns-nsec3sign/SECURITY-REVIEW.md)
- API reference (live): <http://localhost:8000/docs#tag/dns>
