# DNS integration: CoreDNS deployment + IPAM-driven records + anycast

> Design doc for the DNS subsystem. Captures the architecture and the
> reasoning behind the major choices so future contributors don't have
> to reverse-engineer the trade-offs from the code.

## Context

The DCIM platform already owns IPAM (Fabric → VRF → Supernet → Subnet → IPAddress) and runs a Python collector at every site with mTLS to the central API. Today there's nothing answering DNS queries for those hosts — operators either manually maintain external zone files or skip internal DNS entirely.

This design makes DCIM the source-of-truth for internal DNS:

- Two CoreDNS containers at each site — one **authoritative** for the per-site zone (delegated from a per-fabric apex), one **recursive** for everything else.
- The recursive CoreDNS at each site is reachable on a **per-fabric anycast IP** announced via a GoBGP sidecar (one anycast per fabric, IPv4 + IPv6).
- DCIM auto-generates A/AAAA/PTR from `IPAddress.dns_name` and lets operators manage CNAME/MX/TXT/SRV/NS/CAA as first-class `DnsRecord` rows.
- The existing site collector grows a new agent loop that polls the central API for rendered Corefile + zone files + GoBGP config, drops them into a shared volume, and signals each container to reload.

DNSSEC, split-horizon views, and AXFR-based zone transfers are explicitly deferred — the central push/pull replaces AXFR.

## Major decisions

| Decision | Choice | Why |
|---|---|---|
| Where CoreDNS runs | Co-located with the site collector as sibling containers in a `docker compose` stack | The collector already exists at every site with mTLS to central. Reusing it as the config-render-and-reload agent gives us one auth path, one model in inventory, and avoids the docker-socket-in-container anti-pattern. |
| Auth vs recursive split | Two CoreDNS containers per site — auth on the management IP, recursive on the anycast IP | Role separation keeps the recursive's failure modes (cache poisoning, recursion bombs) from taking out the authoritative pod. Clients only ever talk to the recursive. |
| Anycast plane | GoBGP sidecar advertising a per-fabric `/32` (and `/128` if v6) | GoBGP is mature, container-friendly, and Go-native (no GPL licensing concerns). Per-fabric anycast lines up with the existing fabric scoping; routing convergence handles failover automatically. |
| Source of truth direction | One-way IPAM → DNS for A/AAAA/PTR; manual `DnsRecord` rows for CNAME/MX/TXT/SRV/NS/CAA | Bidirectional sync against external DNS (Infoblox/BlueCat) is a fundamentally different product. Owning the data outright and exporting BIND-format zone files keeps the contract simple. |
| Zone scoping | Per-site subdomain delegation under a per-fabric apex (`site42.prod.dcim.mil` delegated from `prod.dcim.mil`) | Aligns with the site hierarchy, lets each site CoreDNS own its own data, keeps blast radius small. The fabric apex lives in DCIM and is loaded by every site auth pod for resilience. |
| BGP config modeling | First-class `BgpPeer` + `AnycastGroup` entities, reusable across services | DNS is the first anycast service; NTP and log aggregators are the obvious next ones. Inlining peer config on `DnsServer` would force a refactor on the second consumer. |
| DNSSEC | Deferred to v2 | KSK/ZSK rotation, NSEC3, and DS-record uploads to the parent zone is a project of comparable size to this whole document. Get the basics solid first. |

## Architecture

```
                   ┌─────────────── DCIM central ───────────────┐
                   │  /api/v1/dns/...  (CRUD + render)          │
                   │  zone-renderer service (BIND-format)       │
                   │  bgp-config-renderer service (GoBGP YAML)  │
                   └────────────────────┬───────────────────────┘
                                        │ mTLS / bearer (existing)
                       ┌────────────────┴────────────────┐
                       │                                 │
            ┌──── site 42 ────┐                ┌──── site 99 ────┐
            │ collector       │                │ collector       │
            │  └─ dns-agent   │                │  └─ dns-agent   │
            │ coredns-auth ◄──┤  shared vol    │ coredns-auth    │
            │ coredns-recur ◄─┤  (Corefiles)   │ coredns-recur   │
            │ gobgp ◄─────────┤                │ gobgp           │
            │   ↕ BGP                          │   ↕ BGP         │
            │  underlay leaf                   │  underlay leaf  │
            └─────────────────┘                └─────────────────┘
                       ↓ anycast 10.99.0.53                     ↓
                   clients                                  clients
```

### Query flow

1. Client (configured via DHCP option 6 → anycast IP) sends a query to the recursive CoreDNS.
2. Recursive CoreDNS at the closest site (BGP best-path) answers:
   - For `*.<fabric_apex>` → forward to the local **authoritative** pod on the management IP.
   - For everything else → `forward .` to the operator's configured upstream resolvers.
3. The authoritative pod has the **fabric-wide bundle**: the apex zone (with NS-delegations to every site) and every site subdomain. So most internal lookups never leave the box; cross-site lookups follow the apex referral and hit another site's auth pod directly.

### Push/pull model (replaces AXFR)

Every 30 s, the collector's `dns-agent` loop polls `GET /dns/servers/{id}/bundle?etag=<last>`. If the etag matches, no-op. Otherwise it atomically writes the new Corefile + zone files + GoBGP config into a shared volume, then sends `SIGUSR1` to CoreDNS (hot-reload) and `SIGHUP` to GoBGP. Status reports flow back via `POST /dns/servers/{id}/render-status`.

This keeps the central system as the single source of truth and avoids running zone-transfer infrastructure (TSIG, slave config, IXFR journaling).

## Backend

### Models — [`backend/src/dcim/models/dns.py`](../../backend/src/dcim/models/dns.py)

Mirrors the conventions of [`models/ipam.py`](../../backend/src/dcim/models/ipam.py): `UUIDPrimaryKey` + `Timestamped` mixins, str enums via `values_callable`, `INET`/`CIDR` Postgres types, denormalized fabric/site IDs for fast filter.

| Table | Purpose |
|---|---|
| `dns_servers` | One row per CoreDNS deployment (auth or recursive) at a site. Mirrors `DhcpServer` shape. |
| `dns_zones` | Apex (per-fabric) or site (per-site) zones DCIM is authoritative for. Holds SOA fields + default TTL. |
| `dns_records` | Every record-type as a row. `source=ipam` rows are auto-generated; `source=manual` rows are operator-managed. JSON `data` column for type-specific fields. |
| `anycast_groups` | Per-fabric anycast IP for a service (`dns_recursive` initially; reserved for `ntp`, `log`, etc.). |
| `bgp_peers` | Reusable BGP neighbor definition (per-site: local AS, peer AS, peer IP, MD5 password). |
| `anycast_bgp_bindings` | M:M between recursive `DnsServer` and the BGP peers it advertises its anycast IP to. |

### Migration — [`backend/src/dcim/migrations/versions/20260510_0009_dns.py`](../../backend/src/dcim/migrations/versions/20260510_0009_dns.py)

One DDL statement per `op.execute()` (asyncpg refuses multi-statement SQL — see the existing 0007/0008 migrations for the pattern). Creates the six tables and five enums (`dns_server_role`, `dns_zone_kind`, `dns_record_type`, `dns_record_source`, `anycast_service`).

### Schemas — [`backend/src/dcim/schemas/dns.py`](../../backend/src/dcim/schemas/dns.py)

Reuses `CidrStr` / `InetStrOpt` from [`schemas/ipam.py`](../../backend/src/dcim/schemas/ipam.py) so we don't duplicate the `_to_str` BeforeValidator.

`DnsRecord.data` is validated by a discriminated union — one schema per record type. Backend rejects malformed payloads (e.g. `priority` missing on MX, `target` not an FQDN on CNAME).

### Services — [`backend/src/dcim/services/dns.py`](../../backend/src/dcim/services/dns.py)

Pure render functions, kept separate from DB concerns so they're trivially unit-testable.

- `render_zone_file(zone, records) -> str` — BIND-format zone (CoreDNS `file` plugin reads BIND). Deterministic ordering for diffability.
- `auto_records_for_subnet(subnet, ip_addresses, zone) -> list[DnsRecord]` — projects `IPAddress.dns_name` into A/AAAA + reverse PTR. All `source=ipam`.
- `render_corefile(server, zones, upstreams) -> str` — Corefile shape depends on `server.role`:
  - `auth`: `file` plugin per zone, `health`, `prometheus`, `errors`, `log`.
  - `recursive`: `forward .` to operator upstreams + stub-zone `forward <fabric_apex> <local_auth_ip>:53` so internal lookups don't hit the public root.
- `render_gobgp_config(server, peers, anycast_group) -> dict` — GoBGP YAML: local AS, neighbors, network statements for the anycast `/32` (v4) and `/128` (v6).
- `render_bundle_for_server(server) -> dict` — single call returning Corefile + zones + GoBGP config + etag. Polled by the collector.

Reverse-zone math reuses [`services/ipam.py:parse_network`](../../backend/src/dcim/services/ipam.py).

### API — [`backend/src/dcim/api/dns.py`](../../backend/src/dcim/api/dns.py)

Same patterns as `api/ipam.py` (`Page`/`PageParams`, audit on every write, capability-gated):

- `GET/POST/PATCH/DELETE /dns/zones` (filter by fabric/site/kind)
- `GET/POST/PATCH/DELETE /dns/records` (filter by zone/type/source)
- `GET/POST/PATCH/DELETE /dns/servers` (filter by site/fabric/role)
- `GET/POST/PATCH/DELETE /dns/anycast-groups`
- `GET/POST/PATCH/DELETE /dns/bgp-peers`
- `POST /dns/bgp-peers/{peer_id}/bind/{server_id}` + corresponding `DELETE`
- `GET /dns/servers/{id}/bundle` — the rendered Corefile + zones + GoBGP, with an `etag` for short-circuiting
- `POST /dns/zones/{id}/sync-from-ipam` — re-projects IPAM rows. Idempotent; replaces only `source=ipam` rows.

### Worker cron — [`backend/src/dcim/worker.py`](../../backend/src/dcim/worker.py)

`dns_sync_from_ipam` every 5 minutes, walking fabrics and rebuilding `source=ipam` records for each site zone. Catches new IP allocations / DHCP leases since the last cycle.

## Collector

[`collector/src/dcim_collector/main.py`](../../collector/src/dcim_collector/main.py) gains a third concurrent loop alongside `_device_loop` and `_drain_loop`:

- **`_dns_agent_loop`** in new [`collector/src/dcim_collector/dns_agent.py`](../../collector/src/dcim_collector/dns_agent.py):
  1. Every `dns_poll_interval` (default 30 s), call `GET /dns/servers/{id}/bundle?etag=<last>`. Etag-match → no-op.
  2. Atomically write `Corefile`, each `zones/<name>.zone`, and `gobgp.yaml` to the shared volume.
  3. `kill -SIGUSR1 <coredns-pid>` to hot-reload CoreDNS, `kill -SIGHUP <gobgp-pid>` for GoBGP. PIDs from a shared pidfile each container drops.
  4. `POST /dns/servers/{id}/render-status` with success/failure.

Auth model unchanged — reuses the existing `MtlsConfig` from [`collector/src/dcim_collector/config.py`](../../collector/src/dcim_collector/config.py).

### Site bundle — [`infra/docker/site-dns/docker-compose.yml`](../../infra/docker/site-dns/docker-compose.yml)

The operator brings this up at each site. Services:

- `dcim-collector` (existing image, with `dns:` block in config)
- `coredns-auth` (`coredns/coredns:1.11`, mounts `/var/lib/dcim-dns/auth/`, listens 5353/53 on management IP)
- `coredns-recursive` (same image, mounts `/var/lib/dcim-dns/recursive/`, listens 53 on the anycast IP via host networking)
- `gobgp` (`jauderho/gobgp:latest`, host networking, peers with the leaf the operator configures)

The collector container does **not** need the docker socket — render-and-signal is enough because all containers share the host PID namespace via the compose pid-namespace setting and a shared bind-mounted volume.

## Frontend

New `dns` tab in [`frontend/src/pages/ipam.tsx`](../../frontend/src/pages/ipam.tsx). The IPAM page is already the home for things-with-IPs; DNS belongs there.

Three sub-views inside the tab (matching the existing `OverlaysTab` pattern — fabric selector at top, panels below):

1. **Zones** — apex zone + per-site zones, with a "render preview" that fetches the current BIND text.
2. **Records** — drill from a zone; CRUD with type-specific forms. Auto-generated rows show a "from IPAM" badge and are read-only.
3. **Servers + anycast** — register CoreDNS deployments, assign role + site, bind recursive servers to BGP peers + an anycast group. "Last render" status badge per server.

A small **BGP Peers** management dialog inside the Servers panel for now — the model is reusable but DNS is its only consumer in v1.

## Verification

End-to-end smoke against the existing compose stack:

1. `POST /dns/zones {kind:apex, name:"prod.dcim.mil", fabric_id:...}`.
2. `POST /dns/anycast-groups {service:dns_recursive, anycast_ipv4:"10.255.0.53"}`.
3. Register two `DnsServer` rows (auth + recursive) at site 42.
4. `GET /dns/servers/{recursive_id}/bundle` and assert: Corefile with `forward . <upstream>`, stub for `prod.dcim.mil → <auth_unicast>:53`, GoBGP advertising 10.255.0.53/32.
5. `POST /dns/zones/{site_zone_id}/sync-from-ipam` and verify A/AAAA + reverse PTR appear.
6. `pytest backend/tests/services/test_dns_render.py` for the pure render functions.
7. UI: open IPAM → DNS tab → add a zone → add a manual CNAME → verify the zone preview reflects it.

Local site-bundle smoke (deferred follow-up): bring up `infra/docker/site-dns/docker-compose.yml` against the local stack with a fake leaf peer (another GoBGP container as the BGP peer); `dig @<anycast-ip> leaf-01.site42.prod.dcim.mil` should return the expected A record.

## Out of scope (defer)

- DNSSEC signing (KSK/ZSK rotation, NSEC3, DS upload to parent)
- Split-horizon views (different answers for internal vs external clients)
- AXFR/IXFR zone transfers (replaced by central push/pull)
- DNS-based service discovery integrations (Consul, etcd)
- Encrypted-at-rest BGP MD5 passwords (column exists; encryption deferred to the same pass that hardens `DhcpServer.auth_password`)
- Lifecycle management of the CoreDNS container itself (start/stop/upgrade) — operator runs `docker compose up`; collector only renders configs and signals reloads
