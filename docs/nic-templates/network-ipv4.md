# Network IPv4 Template — `NIC-Network-IPv4`

Registers an IPv4 network allocation. Maps directly onto the LIR request →
allocation flow.

- **Template Type:** Network
- **Actions:** New (`N`), Modify (`M`), Delete (`D`), Reregister (`R`)
- **Maps to:** `lir_requests` (intake) → `lir_allocations` + `supernets` (granted)

## Fields

### Administrative

| Field | Required | Help | Maps to |
|-------|----------|------|---------|
| Action | ✓ | New / Modify / Delete / Reregister. | (request action) |
| Agency | — | Agency name. | (request metadata) |
| Handle | — | System generated. | network handle map |
| Assigned Org Handle | ✓ | Preregistered Organization. | `organizations.id` ref |
| Technical POC Handle | ✓ | Tech POC (gov/mil/contractor). | User handle ref |
| Administrative POC Handle | ✓ | Admin POC (**gov/mil only**). | User handle ref |
| Zone Administrator | — | Optional; DNSSEC approver. | User handle ref |

### Network information

| Field | Required | Help | Maps to |
|-------|----------|------|---------|
| IP Version | ✓ | `IPv4`. | `lir_requests.ip_family` |
| Network Aggregator | ✓ | `NIPRNET` / `Cloud Service Offering` / `DREN` / `INTERNET` / `DNI-U` / `OTHER`. Non-changeable when maintaining. | (new column) |
| Network Classification | ✓ | e.g. `NIPRNET: Unclassified`. | (new column) |
| Customer Network Name | ✓ | Begins alphanumeric; `[A-Z0-9-]`. Auto-generated for Cloud: `CCSNET-<Agency>-<Type>-<Provider>`. | (new column) |
| Tactical Network | — | `Yes` / `No`. | (new column) |
| CCSD | — | If known. | (new column) |
| NIPRNET Hub Identifier | — | If known. | (new column) |
| CCS Platform | Cloud only | Data type hosted. | (new column) |
| CCS Provider/Platform | Cloud only | Cloud provider. | (new column) |
| CCS Region | Cloud only | Internet routing location. | (new column) |

### New IPv4 registration data

| Field | Required | Help | Maps to |
|-------|----------|------|---------|
| Network Number | Modify/Rereg/Reassign | Required for modify, reregistration, or reassignment. | `supernets.prefix` (network part) |
| CIDR | ✓ | CIDR being requested. **`/20` and shorter require an addressing plan + topology diagram.** | `lir_requests.prefix_length` |
| No. of Hosts (Initially) | ✓ | Initial host count. | (request metadata) |
| No. of Hosts (6 months) | ✓ | Host count at 6 months. | (request metadata) |
| Max No. of Hosts | ✓ | Expected maximum host count. | (request metadata) |

### IN-ADDR DNS server information

| Field | Required | Help | Maps to |
|-------|----------|------|---------|
| Hostname 1 + IP | recommended | Preregistered reverse-DNS server. **Min two (2) recommended.** | PTR/reverse zone refs |
| Hostname 2 + IP | recommended | Second reverse-DNS server. | PTR/reverse zone refs |

| Field | Required | Help | Maps to |
|-------|----------|------|---------|
| Justification | ✓ (New) | Required for all New network requests. | `lir_requests.justification` |
| User Comments | — | Free-form. | (request metadata) |

> `/20`-or-shorter requests require an **addressing plan and topology/diagram**
> attachment — model as a conditional required upload when CIDR ≤ /20.

## Raw layout (for future emit)

```
Template: NIC-Network-IPv4
Template Type.............: Network
Action Type..................:
Agency……………………….:
Handle..........................: System generated field.

ADMINISTRATIVE INFORMATION
Assigned Org Handle............:
Technical POC Handle..........:
Administrative POC Handle.:
Zone Administrator.............:

NETWORK INFORMATION
IP Version…………………………..:
Network Aggregator…………..:
Network Classification………..:
Customer Network Name……:
Tactical Network………………...:
CCSD……………………………………:
NIPRNET Hub Identifier……….:
CCS Platform……………………….:
CCS Provider/Platform………..:
CCS Region………………………….:

NEW IPv4 REGISTRATION DATA
Network Number……………..…:
CIDR…………………………………….:
No. of Hosts (Initially):…………:
No. of Hosts (6 months)…..….:
Max No. of Hosts…………………:

IN-ADDR DNS SERVER INFORMATION
Hostname 1......……………:
Hostname 1 IP Address..:
Hostname 2………………….:
Hostname 2 IP Address…:

JUSTIFICATION (Free Form Section)

User Comments (Free Form Section)

END OF TEMPLATE
```
