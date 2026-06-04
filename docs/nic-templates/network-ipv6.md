# Network IPv6 Template — `NIC-Network-IPv6`

Registers an IPv6 network allocation. Same administrative + network-information
blocks as [IPv4](network-ipv4.md); the **registration data** block differs (IPv6 is
sized in `/48`s and is location-anchored).

- **Template Type:** Network
- **Actions:** New (`N`), Modify (`M`), Delete (`D`), Reregister (`R`)
- **Maps to:** `lir_requests` (intake) → `lir_allocations` + `supernets` (granted)

## Fields

### Administrative + Network information

Identical to [network-ipv4.md](network-ipv4.md) (Assigned Org / Tech POC / Admin POC
/ Zone Administrator; IP Version = `IPv6`; Network Aggregator; Classification;
Customer Network Name; Tactical Network; CCSD; Hub Identifier; CCS Platform /
Provider / Region).

### New IPv6 registration data

| Field | Required | Help | Maps to |
|-------|----------|------|---------|
| DISN Transport | ✓ | `Yes`/`No` — do you use DISN transport to reach the internet? | (new column) |
| Geophysical Location | ✓ | US state or world country; **all addresses must be within** this location. | (new column) |
| No. of /48 Requested | ✓ | If using DISN: only **one** /48. Own backbone: `1, 2, 4, 8, 16` → `/48, /47, /46, /45, /44`. Requires addressing plan + topology diagram + supporting docs. | `lir_requests.prefix_length` (derived) |

### IN-ADDR DNS server information

| Field | Required | Help | Maps to |
|-------|----------|------|---------|
| Hostname 1 + IP | recommended | Preregistered reverse-DNS server. **Min two (2) recommended.** | PTR/reverse zone refs |
| Hostname 2 + IP | recommended | Second reverse-DNS server. | PTR/reverse zone refs |

| Field | Required | Help | Maps to |
|-------|----------|------|---------|
| Justification | ✓ (New) | Required for all New network requests. | `lir_requests.justification` |
| User Comments | — | Free-form. | (request metadata) |

> **Sizing rule:** DISN-connected ⇒ exactly one `/48`. Own-backbone ⇒ count ∈
> {1,2,4,8,16}, mapping to prefix lengths /48…/44. Always requires an addressing
> plan + topology diagram attachment.

## Raw layout (for future emit)

```
Template: NIC-Network-IPv6
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

NEW IPv6 REGISTRATION DATA
DISN Transport……………..…:
Geophysical Location……….:
No. of /48 Requested:…………:

IN-ADDR DNS SERVER INFORMATION
Hostname 1......……………:
Hostname 1 IP Address..:
Hostname 2………………….:
Hostname 2 IP Address…:

JUSTIFICATION (Free Form Section)

User Comments (Free Form Section)

END OF TEMPLATE
```
