# CDS / CDNSKEY auto-propagation (RFC 7344)

Admin reference for the RFC 7344 / RFC 8078 mechanism DCIM uses to
let parent zone scanners auto-update DS records on KSK rotation,
removing the manual portal-upload step.

The day-to-day record-management lives in the
[operator guide](operator-guide.md); the rest of the platform
ownership lives in the [admin guide](admin-guide.md). This doc
covers only the CDS/CDNSKEY surface.

---

## What it does

When a DCIM zone is **signed AND** has `publish_cds=true` (the
default), the renderer emits two extra records at the zone apex:

```dns
@   IN  CDNSKEY  257 3 13 <base64-public-key>
@   IN  CDS      <key-tag> 13 2 <sha-256-digest>
```

One pair per **active KSK**. ZSKs and retired KSKs are
deliberately excluded — see [§ Why ZSKs and retired KSKs are
skipped](#why-zsks-and-retired-ksks-are-skipped).

A parent zone operator running an RFC 8078 §3 scanner picks these
up on its scan interval, validates the signature chain (the
records are signed by the current ZSK), confirms the new KSK is
introduced **before** any DS handoff, and replaces the parent's
DS RRset to match `CDS`. Without a scanner, the records are
harmless padding in the zone file — about 200 bytes per signed
zone.

## When to leave it on (the default)

- Parent zone runs a CDS scanner (`BIND named-checkzone -S`,
  Knot's `keymgr ds-check`, PowerDNS Lightning Stream, etc.) and
  you want hands-off KSK rotation.
- You're inside the DoD-internal hierarchy under a parent zone
  that you also control through DCIM — the future "central
  consumes DCIM's CDS for its own children" loop benefits from
  having the records always present.
- You don't care: the records are 200 bytes/zone and don't
  affect resolver behavior.

## When to turn it off

Set `publish_cds=false` on a per-zone basis when:

- **Cross-org coordinated rotation.** You're rotating a KSK whose
  DS is held by an external operator who requires a written
  hand-off (signed ticket, JIRA approval, etc.). Suppressing CDS
  prevents an automated scanner at the parent from picking up
  the new key before the human process completes.
- **Compliance attestation period.** A regulatory window requires
  you to attest that no automated DS update happened. Flip it off
  for the attestation, flip back on after.
- **Parent operator explicitly forbids it.** Some parents (Verisign
  for some TLDs, for example) require RFC 8078 §4 explicit
  bootstrapping before they'll honor CDS — pushing the records
  before they've enabled scanning produces noise without effect.
  Defensible to leave them off until the bootstrap.
- **Mid-incident.** You're investigating a key compromise. The
  active KSK is being retired *out of band*; you don't want a
  parent scanner to lock the parent's DS to a key you're about
  to revoke.

Flipping `publish_cds=false` removes both records from the next
collector poll (typically within ~30 s of the API write). Flipping
back to `true` re-publishes on the same cycle.

## How to flip it

### API

```http
PATCH /api/v1/dns/zones/{zone_id}
Content-Type: application/json
Authorization: Bearer <token>

{ "publish_cds": false }
```

Returns the updated zone. The next bundle render will omit the
records; the next collector poll picks the change up.

### Direct DB (emergency only)

```sql
UPDATE dns_zones SET publish_cds = false WHERE name = 'example.org';
```

Bypasses the audit log and the freeze check; only useful when the
API is down.

### UI

Surfaces under the zone detail's DNSSEC panel (planned — until
the UI ships, use the API path above).

## Interplay with KSK rotation

During a KSK rotation the active key list contains **both** the
incoming and the outgoing key for the overlap window. The
renderer emits a `CDNSKEY` + `CDS` pair for **each** active KSK.
A correctly-implemented parent scanner (RFC 8078 §3) is supposed
to add the new key's DS *without* removing the old key's DS until
the old key is fully gone from the child's DNSKEY RRset.

Concretely:

1. **t=0**: rotate KSK. Old KSK is marked retired (`retired_at`
   set); new KSK is generated, active. Renderer now emits CDS for
   the new KSK only — the retired one is excluded.
2. **Parent scanner at t≤scan_interval**: sees one CDS for the
   new key, updates parent DS to match.
3. **t≥48h** (or whatever the parent's DS TTL × safety margin):
   old KSK is fully out of the validation path; operator deletes
   the retired DnsKey row via the API.

If your parent's scanner is more conservative (some refuse to
*remove* a DS RR without an RFC 8078 §4 delete-DS marker), keep
the retired key for one TTL beyond rotation, but the CDS *still*
won't reference it — that's deliberate. Manually upload a DS
delete to the parent if their scanner needs the explicit signal.

## RFC 8078 "delete DS" form

DCIM does **not** currently emit the special "delete DS" record
form RFC 8078 §4 defines for unsigning a zone:

```dns
@   IN  CDNSKEY  0 3 0 AA==
@   IN  CDS      0 0 0 00
```

Use case: signed zone → unsigned, you want the parent scanner to
remove all DS RRs automatically. Today the workflow is:

1. Manually request DS removal at the parent (portal, ticket).
2. Wait for the parent's DS TTL to expire.
3. `POST /dns/zones/{id}/disable-dnssec` to remove keys + clear
   `signed`.

A future change will add an `unsigning` lifecycle state that
emits the delete-DS form for the rotation window before
[step 3]. Track in the [DNS follow-ups](../../ROADMAP.md#dns-follow-ups).

## Why ZSKs and retired KSKs are skipped

**ZSKs**: never carry DS. The parent's DS chain is rooted in the
**KSK**, not the ZSK. Publishing a `CDS` for a ZSK would tell the
parent to validate the child's DNSKEY RRset against a key that
isn't used to sign that RRset — instant SERVFAIL on every signed
response. The renderer enforces this with a hard skip on
`key.role != ksk`.

**Retired KSKs**: the operator has already started removing them
from rotation. Emitting their CDS would keep them alive in the
parent's DS even after they've left the child's DNSKEY RRset, so
validators that have cached the old DS would fail to chain to a
KSK no longer present. The renderer enforces this with a hard
skip on `key.retired_at is not None`.

These two rules together mean **the published CDS RRset always
matches the currently-active set of DS-relevant keys** — no stale
state, no premature state.

## Validation

Three checks the admin should be able to run end-to-end:

### 1. The records are in the rendered zone file

```bash
docker run --rm -v <site>_dns-state:/state alpine:3 \
    grep -E 'CDNSKEY|CDS' /state/auth/zones/<zone>.zone
```

Expect: one `CDNSKEY` and one `CDS` line per active KSK, anchored
at `@` (the apex). No DS-of-children records mixed in (those have
the child name as owner, not `@`).

### 2. CoreDNS serves them

```bash
dig +short @<auth-server-ip> CDS  example.org
dig +short @<auth-server-ip> CDNSKEY example.org
```

Expect both queries to return records. If they return empty,
either the bundle hasn't pushed yet (wait a poll cycle, check
`/api/v1/dns/servers/{id}` `last_render_at`) or `publish_cds`
is off / `signed` is off.

### 3. The CDS digest matches `render_ds_records`

```bash
dig +short @<auth-server-ip> CDS example.org
# 45561 13 2 306E873C20CE3ED16621606EFEF8B439FC3DFEEC914906B236DD953AFC186DBA

curl -fsS -H "Authorization: Bearer $TOKEN" \
    /api/v1/dns/zones/<zone_id>/ds-records | jq '.[0]'
# { "key_tag": 45561, "algorithm": 13, "digest_type": 2,
#   "digest": "306E873C20CE…" }
```

Tags and digests must match exactly — RFC 7344 §3.1 is explicit
that the CDS is "what would be produced as DS." DCIM enforces this
in the renderer (same SHA-256 computation in both code paths) and
the `test_cdnskey_cds_rdata_fields_consistent_with_ds` unit test
locks it in.

## Troubleshooting

### "Parent isn't picking up the rotation"

Walk it backwards from the parent:

1. **Can the parent's scanner reach `auth` over UDP/53?** Check
   the per-fabric ACLs (`fabric.dns_allow_networks`,
   `dns_deny_networks`). The scanner runs from the parent
   operator's network, which may not be in DCIM's allow-list
   until you explicitly add it.
2. **Does the parent see a non-zero CDS RRset?** They should be
   able to `dig CDS <zone> @<auth-ip>` and see one record per
   active KSK. Empty answer → check `publish_cds` flag (see
   validation step 2 above).
3. **Does the CDS match the current DS the parent holds?** If
   the parent holds an *older* DS than the CDS publishes, that's
   the rotation in flight — they should update on their next
   scan. If the parent holds a *newer* DS than the CDS publishes,
   DCIM is stale — re-render the bundle, check the collector
   apply timestamp.
4. **Is the parent's scanner running at all?** Some parents
   scan daily, some hourly; the operator-facing assumption that
   "rotation propagates in minutes" only holds for fast scanners.

### "CDS shows the old retired KSK"

Shouldn't happen — the renderer hard-skips retired KSKs. If you
see this:

- Check the live render with the inspector at validation step 1.
- Confirm `retired_at` is actually set on the old key (a
  rotation-key API call sets it server-side; a hand-`UPDATE` to
  the row needs to set this column explicitly).
- Check the collector hasn't been off-line for longer than the
  zone's SOA serial bump — a stale bundle holds stale records.

### "Same record published twice"

The render is idempotent, but the underlying zone file is
appended to. If you see duplicate apex records, check that no
operator has manually added a CDS as a `DnsRecord` row — they
shouldn't, but the API doesn't (yet) reject the type at the
record-create layer. The fix: delete the duplicate `DnsRecord`,
re-render the bundle.

## Pointers

- RFC 7344 (CDS / CDNSKEY for auto-update of DS):
  <https://datatracker.ietf.org/doc/html/rfc7344>
- RFC 8078 (parent-side processing, including delete-DS):
  <https://datatracker.ietf.org/doc/html/rfc8078>
- Implementation: `render_cdnskey_cds_lines` in
  [`backend/src/dcim/services/dns.py`](../../backend/src/dcim/services/dns.py)
- Tests: `test_cdnskey_*` in
  [`backend/tests/test_dns_render.py`](../../backend/tests/test_dns_render.py)
- Migration: `20260513_0045_dns_zone_publish_cds`
