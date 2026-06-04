# Organization Template — `NIC-ORGANIZATION`

The Organization must be registered **before** it can be referenced by any other
template (User, Host, Domain, Network, ASN). Its handle is system-generated.

- **Template Type:** Organization
- **Actions:** New (`N`), Modify (`M`), Delete (`D`), Reregister (`R`)
- **Maps to:** `organizations` table

## Fields

| Field | Required | Help | Maps to |
|-------|----------|------|---------|
| Action | ✓ | New / Modify / Delete / Reregister. | (request action) |
| Agency | ✓ | Service/agency, e.g. `ARMY`, `NAVY`. | `organizations` (new column `agency`) |
| Handle | — | System generated. Not required for New. | `organizations.id` ↔ NIC handle map |
| Primary Org POC | — | Handle of primary point of contact. | POC ref (User handle) |
| Secondary/Alternate Org POC | — | Handle of alternate point of contact. | POC ref (User handle) |
| Organization Name | ✓ | Name of your organization. | `organizations.name` |
| Address Line 1 | ✓ | — | `organizations.address_line1` |
| Address Line 2 | — | — | `organizations.address_line2` |
| Address Line 3 | — | — | (new column) |
| Address Line 4 | — | — | (new column) |
| City or APO/FPO | ✓ | City, or APO (Army/Air Force) / FPO (Navy/Marine) post office. | `organizations.city` |
| State or APO/FPO Code | ✓ | State, or MPSA code: `AA` (Americas), `AE` (Europe), `AP` (Pacific). | `organizations.state_province` |
| Zip Code | — | — | `organizations.postal_code` |
| Organizational Mailbox | — | Organization email address. | `organizations.email` |
| User Comments | — | Free-form; notes to Approving Official / NIC. | (request metadata) |

> **Note:** The NIC field-help marks Zip as not-required, but the field table
> implies a complete mailing address. Treat Zip as **required for US addresses**,
> optional for APO/FPO where the code carries it.

## Raw layout (for future emit)

```
Template: NIC-ORGANIZATION
Template Type.............: Organization
Action Type..................:
Handle..........................: System generated field.

ADMINISTRATIVE INFORMATION
Service/Agency.....................:
Primary Org POC Handle…….:
Secondary Org POC Handle..:

ORGANIZATION INFORMATION
Organization Name...............:
Address Line 1.......................:
Address Line 2.......................:
Address Line 3.......................:
Address Line 4.......................:
City or APO/FPO....................:
State or APO/FPO Code……...:
Zip Code................................:
Organizational Mailbox.........:

User Comments

END OF TEMPLATE
```
