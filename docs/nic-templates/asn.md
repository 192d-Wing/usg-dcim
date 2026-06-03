# ASN Template — `NIC-ASN`

Registers an Autonomous System Number and its composition (routing protocols,
routers, networks).

- **Template Type:** ASN
- **Actions:** New (`N`), Modify (`M`), Delete (`D`), Reregister (`R`)
- **Maps to:** `bgp_asns` table (+ `organizations` ref)

## Fields

### Administrative

| Field | Required | Help | Maps to |
|-------|----------|------|---------|
| Action | ✓ | New / Modify / Delete / Reregister. | (request action) |
| Agency | — | Agency name. | (request metadata) |
| Handle | — | System generated. Not required for New. | ASN handle map |
| Assigned Org Handle | ✓ | Preregistered Organization. | `bgp_asns.organization_id` |
| Technical POC Handle | ✓ | Tech contact (gov/mil/contractor). | User handle ref |
| Administrative POC Handle | ✓ | Admin contact (**gov/mil only**). | User handle ref |

### Autonomous system information

| Field | Required | Help | Maps to |
|-------|----------|------|---------|
| AS Number | Modify/etc. | Not required for New (assigned on grant). | `bgp_asns.asn` |
| Network Aggregator | ✓ | `NIPRNET` / `DREN` / `INTERNET` / `DNI-U` / `OTHER` / **`SIPRNET`**. | (new column) |
| Network Classification | ✓ | `NIPRNET: Unclassified` or `SIPRNET: Secret`. | (new column) |
| Customer ASN Name | ✓ | Begins alphanumeric; `[A-Z0-9-]`. | `bgp_asns.description` or new column |

### Protocol & network information (AS composition, for New)

| Field | Required | Help | Maps to |
|-------|----------|------|---------|
| IGP used in AS | — | Interior gateway protocol. | (new column) |
| EGP used in AS | — | Exterior gateway protocol. | (new column) |
| Site Premise Router Address | — | — | (new column) |
| Hub Router Address | — | — | (new column) |
| Number of Routers | — | — | (new column) |
| IP Addresses of Routers | — | — | (new column) |
| Number of Networks | — | — | (new column) |
| IP Addresses of Networks | — | — | (new column) |

| Field | Required | Help | Maps to |
|-------|----------|------|---------|
| Justification | ✓ | Free-form; required. | (request metadata) |
| User Comments | ✓ | Free-form; required. Also asks: existing registered ASN(s) on NIPRNET/SIPRNET? | (request metadata) |

## Raw layout (for future emit)

```
Template: NIC-ASN
Template Type.............: ASN
Action Type..................:
Agency……………………….:
Handle..........................: System generated field.

Administrative Information - Required
Assigned Org Handle............:
Technical POC Handle..........:
Administrative POC Handle..:

Autonomous System Information - Required
AS Number............................: Not required for new.
Network Aggregator..............:
Network Classification...........:
Customer ASN Name.............:

Protocol Information
IGP used in AS........................:
EGP used in AS.......................:

Network Information
Site Premise Router Address..................:
Hub Router Address...............................:
Number of Routers................................:
IP Addresses of Routers.........................:
Number of Networks.............................:
IP Addresses of Networks......................:

Justification Section
Justification (Free Form Text) – Required
User Comments (Free Form Section) – Required
Does this Organization have any existing registered ASN(s) on NIPRNET or SIPRNET? If yes, what is the ASN number(s)?

END OF TEMPLATE
```
