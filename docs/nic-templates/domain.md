# Domain Template — `NIC-DOMAIN`

Registers a `.mil` domain (DNS zone), its forward DNS servers, and mail exchanger.

- **Template Type:** Domain
- **Actions:** New (`N`), Modify (`M`), Delete (`D`), Reregister (`R`)
- **Maps to:** `dns_zones` (+ `dns_records` for NS/MX hostnames)

## Fields

| Field | Required | Help | Maps to |
|-------|----------|------|---------|
| Action | ✓ | New / Modify / Delete / Reregister. | (request action) |
| Agency | — | Agency name, e.g. `ARMY`, `NAVY`. | (request metadata) |
| Handle | — | System generated. | domain handle map |
| Technical POC Handle | ✓ | Preregistered Tech POC (gov/mil/contractor). | User handle ref |
| Administrative POC Handle | ✓ | Preregistered Admin POC (**gov/mil only**). | User handle ref |
| Zone Administrator #1 | — | Optional; approves DNSSEC key templates, notified on DNSSEC events. | User handle ref |
| Zone Administrator #2 | — | Optional; second zone admin. | User handle ref |
| Domain Name | ✓ | Cannot start with `www.`/`www2.`; use vendor name for cloud. | `dns_zones.name` |
| Role Mailbox | — | Optional contact email for this domain. | (new column) |
| DNS Server Hostname 1–6 | ✓ | Preregistered forward DNS servers. **Min two (2) recommended.** | NS `dns_records` |
| MX Server Hostname | — | Mail exchanger hostname. | MX `dns_records` |
| Justification | ✓ (New) | Free-form justification. Required for all **New** domain requests. | (request metadata) |
| User Comments | — | Free-form. | (request metadata) |

## Domain Requirements checklist (must be TRUE)

These are operator attestations the Administrative POC must confirm — model as a set
of required boolean acknowledgements on the request:

- Organizational charter or Provisional Authorization submitted to DoD NIC.
- DNS servers located behind filter routers/firewalls.
- All routers have `IP SOURCE ROUTE` turned **off**.
- DNS service runs **exclusively** on the servers.
- Servers protected by UPS and a backup power source.
- Admin POC acknowledges responsibility for protecting subordinate DNS servers.
- At least two registered DNS servers on **different circuit paths** (no SPOF).
- Both Admin (gov/mil) and Tech POC registered in the DoD NIC/SSC Whois database.

## Raw layout (for future emit)

```
Template: NIC-DOMAIN
Template Type.............: Domain
Action Type..................:
Agency……………………….:
Handle..........................: System generated field.

ADMINISTRATIVE INFORMATION
Assigned Org Handle............:
Technical POC Handle..........:
Administrative POC Handle.:
Zone Administrator.............:

DOMAIN INFORMATION
Domain Name............:
Role Mailbox..............:
DNS Server Hostname.......:
DNS Server Hostname.......:
MX Server Hostname........:

JUSTIFICATION (Free Form Section)

User Comments (Free Form Section)

END OF TEMPLATE
```
