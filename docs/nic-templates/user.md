# User Template — `NIC-USER`

Registers a person as a point-of-contact (POC). A User handle is referenced by
Organization, Host, Domain, and Network templates as their Technical /
Administrative POC or Zone Administrator.

- **Template Type:** User
- **Actions:** New (`N`), Modify (`M`), Delete (`D`), Reregister (`R`)
- **Maps to:** `users` table (currently minimal; POC contact fields are today
  embedded as string columns on `organizations`)

## Fields

| Field | Required | Help | Maps to |
|-------|----------|------|---------|
| Action | ✓ | New / Modify / Delete / Reregister. | (request action) |
| User Handle | — | System generated. Not required for New. | `users.id` ↔ NIC handle map |
| Last Name | ✓ | — | `users.family_name` |
| First Name | ✓ | — | `users.given_name` |
| Middle Initial | — | Middle initial, or `NMI` (no middle initial). | (new column) |
| Name Suffix | — | — | (new column) |
| Title/Rank | — | Title and/or rank. | (new column) |
| Address Line 1 | ✓ | — | (new column / contact table) |
| Address Line 2 | — | — | (new column) |
| Address Line 3 | — | — | (new column) |
| Address Line 4 | — | — | (new column) |
| City or APO/FPO | ✓ | City, or APO/FPO post office. | (new column) |
| State or APO/FPO Code | ✓ | State, or MPSA code `AA`/`AE`/`AP`. | (new column) |
| Zip Code | ✓ | — | (new column) |
| E-mail Address (primary) | ✓ | — | `users.email` |
| E-mail Address (secondary) | — | Secondary email, if applicable. | (new column) |
| Commercial Phone | ✓ | Phone by which you can be reached. | (new column) |
| Commercial Phone Ext | — | If applicable. | (new column) |
| DSN Phone | — | DSN phone. | (new column) |
| DSN Phone Ext | — | If applicable. | (new column) |
| Fax | — | Commercial or DSN fax. | (new column) |
| TLD (Top Level Domain) | — | Top-level `.mil` domain of your chain of command, e.g. `ARMY`, `NAVY`. | (new column) |
| User Comments | — | Free-form. | (request metadata) |

## Raw layout (for future emit)

```
Template: NIC-USER
Template Type.............: USER
Action Type..................:
Handle..........................: System generated field.

NAME INFORMATION
Last Name...................:
First Name..................:
Middle Initial..............:
Name Suffix................:
Title/Rank...................:

ADDRESS INFORMATION
Address Line 1............:
Address Line 2............:
Address Line 3............:
Address Line 4............:
City or APO/FPO.........:
State or APO/FPO Code.....:
Zip Code.....................:

CONTACT INFORMATION
E-mail Address..................:
Commercial Phone...........:
Commercial Phone Ext.....:
DSN Phone........................:
DSN Phone Ext..................:
Fax....................................:
TLD....................................:

User Comments

END OF TEMPLATE
```
