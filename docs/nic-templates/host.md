# Host Template — `NIC-HOST`

Registers a named host (typically a DNS/name server) and its IP address(es).

- **Template Type:** Host
- **Actions:** New (`N`), Modify (`M`), Delete (`D`), Reregister (`R`)
- **Maps to:** `dns_records` (A/AAAA). **No distinct host entity exists yet** — a
  thin host model (or a host view over forward records) is a design decision for the
  capture milestone.

## Fields

| Field | Required | Help | Maps to |
|-------|----------|------|---------|
| Action | ✓ | New / Modify / Delete / Reregister. | (request action) |
| Agency | — | Agency name, e.g. `ARMY`, `NAVY`. | (request metadata) |
| Handle | — | System generated. Not required for New. | host handle map |
| Organization Handle | ✓ | Handle of your preregistered Organization. | `organizations.id` ref |
| Primary POC Handle | ✓ | Preregistered primary POC (gov/mil/contractor). | User handle ref |
| Secondary POC Handle | ✓ | Preregistered secondary POC. | User handle ref |
| Hostname | ✓ | Fully-qualified host name, e.g. `ns1.abc.mil`. | `dns_records.name` (+ zone) |
| Role Mailbox | — | Optional contact email for this host. | (new column) |
| IP Address 1 | ✓ | At least **one** IP address is required. | `dns_records.data` (A/AAAA) |
| IP Address 2–6 | — | Up to 6 IP addresses total. | additional A/AAAA records |
| User Comments | — | Free-form. | (request metadata) |

## Raw layout (for future emit)

```
Template: NIC-HOST
Template Type.............: Host
Action Type..................:
Handle..........................: Not required for New.

ADMINISTRATIVE INFORMATION
Assigned Org Handle.......:
Primary POC Handle........:
Secondary POC Handle....:

HOST INFORMATION
Hostname..................:
Role Mailbox..............:
IP Address..................:
IP Address..................:
IP Address..................:
IP Address..................:
IP Address..................:
IP Address..................:

User Comments (Free Form Section)

END OF TEMPLATE
```
