# DNS operator guide

For the day-to-day operator: how to create zones, manage records,
enable DNSSEC, flip to NSEC3, register CoreDNS servers, watch
health, and resolve common problems from the UI and API. Platform
deployment + secret management is in the [admin
guide](admin-guide.md); the code internals are in the
[implementation guide](implementation.md).

## Where DNS lives in the UI

The DNS tab sits inside the IPAM page (`IPAM → DNS`). Everything
DNS-related scopes to a **fabric** — pick one from the fabric
selector at the top before drilling in. Sub-views inside the tab:

| Sub-view              | Manages                                          |
| --------------------- | ------------------------------------------------ |
| Zones                 | Apex (per-fabric) + site (per-site) + reverse zones |
| Records               | A/AAAA/PTR (auto from IPAM) and CNAME/MX/TXT/SRV/NS/CAA (manual) |
| Servers + anycast     | CoreDNS deployments, BGP peers, anycast groups   |
| Forwarders            | Per-fabric conditional + catch-all upstreams     |
| Blocklists (RPZ-lite) | Sinkholes for known-bad names                    |
| Health checks         | TCP/HTTP/HTTPS probes that gate records          |

Each zone's detail view has its own tabbed layout: Records,
Activity, DNSSEC, Preview, Views (split-horizon), Health-checks.

## Zone management

### Creating a fabric apex zone

The apex zone is the per-fabric "top" — it holds the NS
delegations to every site zone and (if signed) the DS records
that anchor the trust chain.

1. `IPAM → DNS → Zones → New zone`.
2. **Type**: Primary apex.
3. **Name**: the fully-qualified apex (`prod.dcim.mil`,
   `internal.example.test`, etc.).
4. **Fabric**: the fabric this apex belongs to.
5. Leave SOA defaults as-is unless you need shorter timers; the
   defaults are tuned for push/pull rather than AXFR.
6. Save. The renderer will pick it up on the next collector
   poll.

You generally have **one apex per fabric**. If you need a second
apex (e.g. a tenant-scoped namespace inside the same fabric),
create it as another `apex`-kind zone — the renderer can carry
multiple.

### Creating a site zone

Site zones are mostly **auto-projected** from IPAM. When you
delegate from the apex (`site42.prod.dcim.mil` NS records pointing
at the site's auth pod), DCIM creates the corresponding site zone
the first time a record needs to land in it.

To create one explicitly:

1. `IPAM → DNS → Zones → New zone`.
2. **Type**: Primary site.
3. **Name**: the fully-qualified site name
   (`site42.prod.dcim.mil`).
4. **Fabric** + **Site**: both required for site zones.
5. Save.

Reverse zones (`<subnet>.in-addr.arpa` / `ip6.arpa`) are
**always** auto-projected from `Subnet` rows in IPAM — don't
create them by hand. They appear in the Zones list once IPAM has
subnets in this fabric.

### Adding manual records

Inside a zone's detail view, the **Records** tab has a `New
record` button. Type-specific forms cover:

- **CNAME** — single target FQDN. The form rejects targets
  pointing at the same name (no loops).
- **MX** — priority + target. The renderer emits in priority
  order.
- **TXT** — raw text, quoted on the wire. Multi-line + special
  chars supported via the quoting logic the renderer handles for
  you.
- **SRV** — priority, weight, port, target. All four required.
- **NS** — sub-delegation only. Don't use this to point a
  fabric apex at itself; the apex's own NS records are
  auto-emitted from `DnsServer` rows.
- **CAA** — flags, tag (`issue` / `issuewild` / `iodef`), value.

**A / AAAA / PTR** records *can* be created manually but the
normal path is to set `dns_name` on the `IPAddress` row in IPAM
and let the projector fill in A/AAAA/PTR for free. Records the
projector created carry a **"from IPAM"** badge and are read-only
in the UI; flip them to manual only if you need a hand-managed
override.

### IPAM-projected records

The worker cron `dns_sync_from_ipam` runs every 5 minutes (and on
explicit refresh from the zone detail page). It walks each
fabric's subnets, projects `IPAddress.dns_name` into A/AAAA + the
matching PTR in the reverse zone, and replaces only `source=ipam`
rows. Manual records survive across cycles untouched.

To preview what would change without committing, hit `POST
/api/v1/dns/zones/{id}/sync-from-ipam?dry_run=true`.

### DDNS records (from DHCP leases)

When the central DHCP service writes leases with hostnames into
DCIM, those records get `source=ddns` and are managed by the
lease lifecycle (created on lease, removed on release). Flip
`DCIM_DNS_DDNS_ENABLED=false` if your DHCP fleet is noisy and
you'd rather not have churn in zone files.

### Importing existing BIND zones

`Zones → <zone> → Import`. Accepts a `.zone` file in BIND format;
records the importer creates land as `source=manual`. DNSSEC
records (RRSIG, NSEC, NSEC3*, DS, CDNSKEY, CDS, TLSA, SSHFP) are
**skipped** by the importer — DCIM owns those via its own DNSSEC
flow.

## DNSSEC

### Enabling DNSSEC on a zone

1. Open the zone detail view → **DNSSEC** tab.
2. Click **Enable DNSSEC**. A confirmation prompt names the
   algorithm (default ECDSAP256SHA256, configurable system-wide by
   admin).
3. DCIM generates a KSK + ZSK, marks the zone `signed=true`, and
   the renderer starts emitting the `dnssec { key file … }` block
   on the next collector poll.
4. Copy the **DS records** from the DSSEC tab and give them to
   the parent zone's operator (your registrar, your TLD's DNSSEC
   contact, your enterprise parent zone team). Most operators
   ship two DS records — one SHA-1, one SHA-256 — DCIM emits
   SHA-256 (digest type 2) per RFC 6840 §5.2.
5. Once the parent's DS shows up in `dig DS <zone>`, your zone
   is part of the validation chain. Verify with `dig +dnssec @8.8.8.8
   <zone>` and look for the `ad` flag in the response.

### Switching to NSEC3

The default DNSSEC profile uses NSEC — efficient, but it lets
chain-walking validators enumerate your zone contents. NSEC3
hashes owner names so the chain isn't directly walkable. The
**Denial of existence** section on the DNSSEC tab lets you flip:

1. Confirm the auth pod is running the
   `ghcr.io/192d-wing/coredns-nsec3sign:v1.14.2-N` image. NSEC3
   responses from the stock `coredns/coredns` image won't work
   — there's no NSEC3 support in the upstream `dnssec` plugin.
2. Set **Salt** to `""` (empty) — RFC 9276 §3.1 recommends this
   for new deployments. Non-empty salts are accepted for
   compatibility with legacy NSEC3 chains being migrated in
   (must be hex, max 32 bytes).
3. Set **Iterations** to `0` — also RFC 9276 recommended. The UI
   accepts up to 150 (the historic BIND cap) for legacy
   compatibility.
4. **Opt-out** elides insecure delegations (NS-without-DS) from
   the chain — useful only on delegation-heavy zones (apex zones
   that delegate to many unsigned child zones). Leave off for
   site zones.
5. Click **Enable NSEC3**. The renderer flips the per-zone block
   from `dnssec` to `nsec3sign` on the next bundle poll.

To revert: click **Revert to NSEC** on the same section. Trust
chain stays intact; only the denial-of-existence profile
changes.

### Key rotation

ZSK rotation can be automatic or manual:

- **Automatic**: the DNSSEC tab has a **Rotation policy** button.
  Set days > 0 and the worker cron `dns_rotate_zsks` rotates the
  ZSK on that cadence, retiring the previous one.
- **Manual**: click **Rotate ZSK** on the DNSSEC tab header. New
  ZSK becomes active immediately, the previous one is marked
  retired. Retired keys stay in the DNSKEY RRset until you delete
  them (recommended: wait at least the SOA expire window so
  cached validators don't break).

**KSK rotation is always manual** because it requires uploading a
new DS to the parent. The UI flow:

1. Click **Rotate KSK** on the DNSSEC tab header.
2. Confirm the prompt — it warns that you must upload the new DS
   to the parent before the old DS can be retired.
3. DCIM generates the new KSK, marks the old one retired but
   keeps it in the DNSKEY RRset.
4. Copy the new DS records from the DS records section and ship
   them to the parent operator.
5. Wait for the parent to publish (a day to a week depending on
   the registrar's cadence).
6. Once `dig DS <zone>` shows only the new DS, **purge the
   retired KSK** by clicking the trash icon on its row in the
   Keys table. Don't purge sooner — caching validators verifying
   against the old DS will fail.

### Unsigning a zone

Hit **Unsign zone** on the DNSSEC tab. Type the zone name in the
confirmation modal (typed-confirmation to prevent fat-finger).
Withdraw the DS from the parent *first* — unsigning while a DS
still points at the zone produces sustained validation failures
for the duration of the negative-cache TTL.

## DNS server registration

Each site runs two CoreDNS pods: one **auth** (per-site
authoritative), one **recursive** (forwards to the local auth +
operator upstreams via anycast). Both need a `DnsServer` row in
DCIM:

1. `IPAM → DNS → Servers → New server`.
2. **Role**: `auth` or `recursive`.
3. **Site**: the site this pod lives at.
4. **Unicast IP**: the management IP the pod listens on
   (`172.30.42.10` and `172.30.42.20` in the example compose).
5. For `recursive` pods, scroll to **Anycast announce**:
   - **Anycast group**: pick the fabric's group; this drives the
     `/32` GoBGP announces.
   - **Announced to peer**: multi-select the BGP peers
     announcement targets. Usually the leaf switch + a redundancy
     pair.
6. Save.

The next collector poll renders the Corefile + (for recursives)
the GoBGP YAML, and the on-site stack picks them up
automatically. The Servers list shows **Last render** status
per server; a red badge there is your first signal of trouble.

### BGP peers + anycast groups

Both have their own management screens reachable from the Servers
panel. BGP peer creation flow:

1. `Servers → BGP peers → New peer`.
2. **Site**: where the peer is.
3. **Local AS / Peer AS / Peer IP**: standard BGP knobs.
4. **MD5 password** (optional but recommended) or **TCP-AO
   keychain** reference.
5. Save and bind to one or more `recursive` DnsServer rows from
   the Servers panel.

Anycast group creation:

1. `Servers → Anycast groups → New group`.
2. **Service**: `dns_recursive`.
3. **Anycast IPv4** (required) and **IPv6** (optional).
4. **Fabric**: which fabric this group belongs to.
5. Save.

## Health-checked records

Records can be gated by a TCP, HTTP, or HTTPS probe. When the
probe fails, the renderer drops the record from the rendered zone
file until it recovers. Useful for failover patterns (point a
single CNAME at two A records, gate each on its own probe).

1. `Zones → <zone> → Health-checks → New check`.
2. **Protocol** + **target IP** + **port** + **interval/timeout**.
3. Save. The probe starts immediately from the worker (or from a
   collector when site-local probing is configured).
4. On the record edit form, pick the check from the **Health
   check** dropdown.

The Health-checks tab shows live status; the per-zone Activity
tab logs probe transitions.

## Split-horizon views

Different answers for different client subnets. Useful for
internal/external value splits or fabric-scoped overrides.

1. `Zones → <zone> → Views → New view`.
2. **Name**: short label (`internal`, `dmz`, `default`).
3. **Match CIDRs**: client source IPs that select this view.
4. **Priority**: lower = higher precedence in the Corefile
   `view` plugin's first-match-wins order.
5. Save, then on each record set the view from the dropdown. A
   record with no view goes into the **default** view (clients
   that match no other view's CIDR list).

## Forwarders

Per-fabric stub-zone forwards + catch-all upstreams. Lives at
`Servers → Forwarders`.

- **Conditional forwarder** (specific zone name → specific
  upstream): for "send queries for `corp.example.com` to
  10.0.0.53" patterns.
- **Catch-all upstream** (fabric-wide): the `forward .` block in
  the recursive Corefile. Override the system default
  (`DCIM_DNS_RECURSIVE_UPSTREAMS`) per-fabric if some fabrics
  need different upstreams.

## Blocklists (RPZ-lite)

`Servers → Blocklists → New blocklist`. Operate on the recursive
pod's response path:

- **Pattern**: literal name or `*.suffix` wildcard.
- **Action**: `nxdomain` (deny with NXDOMAIN), `nodata` (NODATA),
  `sinkhole` (rewrite to a sinkhole IP).

Bulk import is supported — paste a list of patterns into the
**Bulk add** modal.

## Verifying a signed zone end-to-end

Before pointing production traffic at a freshly-signed zone, run
the [comprehensive-test
harness](../../infra/coredns-nsec3sign/examples/comprehensive-test/)
against your auth pod. It boots the deployed image against a
hand-rolled zone exercising every code path the plugin handles —
positive answers, NXDOMAIN, NODATA, wildcard expansion, empty
non-terminals (deep hierarchies), and secure / insecure
delegation referrals — and produces `dig +dnssec` output an
operator can read in 30 seconds.

Use it as the sanity check after:

- **Enabling NSEC3** on a zone for the first time. Confirms the
  custom image is actually running and the signing path works on
  the wire.
- **Bumping the plugin image** to a new tag. Confirms the new
  build still produces validating responses across all the
  shapes a real zone might hit.
- **Rotating a key** (KSK or ZSK). The harness's RRSIG inception
  field shows whether the new key is the one signing.

The harness uses its own throwaway zone (`example.test.`) so it
doesn't touch production data. Walk-through is in the
[harness README](../../infra/coredns-nsec3sign/examples/comprehensive-test/README.md).

## Common troubleshooting

### "My zone changes aren't propagating"

- Open `Servers`. The relevant `auth` row should show
  `last_render_status = ok` and `last_render_at` < 1 minute.
- If render is older than a couple minutes, the collector is
  stuck or unreachable. Look at the collector's logs (the
  Activity tab on the site shows recent events).
- If render is recent but the change isn't visible:
  - For a new record: confirm it's saved (refresh the records
    list) and check it doesn't have a health check that's
    failing.
  - For a DNSSEC change: signature cache may be serving stale
    sigs. Wait ~30 seconds for cache expiry or restart the auth
    pod.

### "I can't enable NSEC3 — the button is disabled"

The zone must be signed first. Enable DNSSEC, then come back and
flip NSEC3.

### "DNSSEC validators reject my responses"

Most common causes, in order:

1. **DS at parent doesn't match KSK** — copy the DS records from
   the DNSSEC tab again, compare to `dig DS <zone>`. Re-upload
   to parent if needed.
2. **NSEC3 enabled on a stock CoreDNS image** — admin needs to
   bump the auth pod to the custom `coredns-nsec3sign` image.
3. **Old NSEC3 cached** — wait one negative-cache TTL (default 5
   minutes from the SOA Minttl) for downstream validators to
   refetch.

### "BGP session won't come up"

- Confirm the peer's local + remote AS match what's in the
  `BgpPeer` row.
- MD5/TCP-AO secrets are case-sensitive. The leaf-side and
  DCIM-side must match exactly.
- Look at `gobgp` logs in the site stack: `docker compose -p
  siteN logs gobgp`.

### "Anycast IP works from some clients but not others"

Routing issue, not DNS. The leaf is announcing the route to only
a subset of clients. Talk to whoever owns the underlay network.

## Pointers

- Admin / deployment: [admin-guide.md](admin-guide.md)
- Code internals: [implementation.md](implementation.md)
- Design context: [../design/dns-integration.md](../design/dns-integration.md)
- API reference: <http://localhost:8000/docs#tag/dns> (OpenAPI)
