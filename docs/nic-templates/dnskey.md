# DNSKEY Template — `NIC-DNSKEY`

Registers a DNSSEC Key-Signing Key (KSK) for inclusion in the `.MIL` zone file.
Approved/rejected by the domain's **Zone Administrators** (not the Tech/Admin POCs).

- **Actions:** New (`N`), Modify (`M`), Delete (`D`) — **no Reregister**
- **Maps to:** DNSSEC lifecycle in `internal/dns/dnssec*.go`

## Fields

| Field | Required | Help | Maps to |
|-------|----------|------|---------|
| Action | ✓ | New / Modify / Delete. | (request action) |
| Handle | ✓ | Domain handle or domain name. | `dns_zones` ref |
| Key Type | display-only | Default `KSK Key`. | (fixed) |
| Start Date | ✓ | `yyyymmdd`. Effective date NIC includes the key in the `.MIL` zone. NIC builds the zone 1900–2000 GMT, Mon–Fri (not weekends/holidays). | DNSSEC key activation |
| End Date | ✓ | Last day NIC includes this KSK; auto-removed the following day. **Validity cannot exceed 2 years.** | DNSSEC key expiry |
| KSK Id | display-only | Computed KSK key tag for the entered value. | DNSSEC key tag |
| KSK Value | ✓ (New) | Record contents from the KSK public key file. Non-updatable on Modify/Delete. | DNSSEC public key |
| User Comments | — | Free-form. | (request metadata) |

## Operational notes (capture as guidance, not fields)

- **Recommended dating:** set each KSK's End Date to the day *before* the next key's
  Start Date, so the current/next KSK is always the one published — this cleanses
  resolver caches across a rollover.
- A DNS server using a KSK that is **not** published (old removed, or next not yet
  published) makes the domain **inaccessible** — all traffic stops.
- Strongly recommended: keep signing your zone with the **old** KSK for **7 days
  after** NIC removal.

## Raw layout (for future emit)

```
Template: NIC-DNSKEY
Action Type..................:
Handle..........................:

KEY INFORMATION
Key Type..................: KSK Key
Start Date................: yyyymmdd
End Date..................:
KSK Id.......................:
KSK Value.................:

User Comments

END OF TEMPLATE
```
