# DoD NIC Registration Templates

This directory captures the **DoD Network Information Center (NIC)** registration
templates as machine-usable specifications. They are the source-of-truth field
schemas for the **end-user (internal DoD customer) registration** workflow in the
LIR module.

> **Status (first cut): form capture only.** These specs define the structured
> data we collect against each template. Rendering the captured data back into the
> NIC's dotted-line template text (for email submission) and any auto-submission
> are explicitly **out of scope** for the first cut — see
> [Rendering / emit (deferred)](#rendering--emit-deferred).

## Why these exist

The DCIM acts as a **Local Internet Registry (LIR)** for its tenants. Internal DoD
customers submit registration requests (for IP space, ASNs, hosts, domains, DNSSEC
keys, and the org/user handles that back them). The DCIM:

1. **Tracks internal DoD customers** and their registration requests using the
   field schemas defined here, and
2. **still pushes upstream to ARIN** via the existing Reg-RWS integration
   (`packages/otter-go/internal/lir/arin/`).

The NIC templates are therefore an **intake data model**, not a replacement for the
ARIN path.

## Submission target (for reference)

All completed NIC templates are emailed to the DoD NIC at:

```
disa.columbus.ns.mbx.hostmaster-dod-nic@mail.mil
```

Phone: 1-844-347-2457, option 2.

## The eight templates

| Template | NIC ID | Spec | Maps to existing DCIM entity |
|----------|--------|------|------------------------------|
| Organization | `NIC-ORGANIZATION` | [organization.md](organization.md) | `organizations` table (POCs, address, `arin_org_id`) |
| User | `NIC-USER` | [user.md](user.md) | `users` table (+ contact fields, currently embedded as org POCs) |
| Host | `NIC-HOST` | [host.md](host.md) | `dns_records` (A/AAAA) — no distinct host entity yet |
| Domain | `NIC-DOMAIN` | [domain.md](domain.md) | `dns_zones` |
| Network IPv4 | `NIC-Network-IPv4` | [network-ipv4.md](network-ipv4.md) | `supernets` / `lir_requests` / `lir_allocations` |
| Network IPv6 | `NIC-Network-IPv6` | [network-ipv6.md](network-ipv6.md) | `supernets` / `lir_requests` / `lir_allocations` |
| ASN | `NIC-ASN` | [asn.md](asn.md) | `bgp_asns` table |
| DNSKEY | `NIC-DNSKEY` | [dnskey.md](dnskey.md) | DNSSEC lifecycle (`internal/dns/dnssec*.go`) |

## Common concepts

### Action types

Every template carries an **Action Type**. Valid actions (not all templates support
all four — DNSKEY omits Reregister):

| Action | Code | Meaning |
|--------|------|---------|
| New | `N` | Create a new registration. |
| Modify | `M` | Change an existing registration. |
| Delete | `D` | Remove / unregister. |
| Reregister | `R` | Re-affirm an existing registration. |

### Handles

Most templates reference **preregistered handles** — opaque NIC-assigned IDs for an
Organization, a User (POC), a Host, a Domain, or an ASN. A handle is *system
generated* by the NIC on a successful `New` registration. Templates that reference
another entity (e.g. a Network referencing its Org and POCs) expect that entity's
handle, or — if not yet known — enough identifying info (org name, POC name + email)
for the NIC to resolve it.

**Dependency order:** `Organization` and `User` must be registered first; their
handles are then referenced by `Host`, `Domain`, `Network`, and `ASN`.

### POC roles

- **Technical POC** — responsible party; may be government, military, **or contractor**.
- **Administrative POC** — approving party; **must be government or military**.
- **Zone Administrator (#1, #2)** — optional; approve/reject **DNSSEC key** templates
  only, and are notified on DNSSEC events. The Tech/Admin POCs approve all *other*
  template types.

### Network classification & aggregator

Shared by the Network and ASN templates:

- **Network Aggregator** (the network family): `NIPRNET`, `Cloud Service Offering`,
  `DREN`, `INTERNET`, `DNI-U`, `OTHER`, and (ASN only) `SIPRNET`.
- **Network Classification**: e.g. `NIPRNET: Unclassified`, `SIPRNET: Secret`.

### Cloud Service Offering (CCS) fields

When the aggregator is **Cloud Service Offering**, several fields become required and
the Customer Network Name is auto-generated as
`CCSNET-<Agency>-<CCS Data Type>-<CCS Provider>` (e.g. `CCSNET-AF-IAAS-AWS`):

- **CCS Platform** — type of data hosted.
- **CCS Provider/Product** — the cloud provider.
- **CCS Region** — routing location to the internet.

## Spec file format

Each `*.md` spec captures, per template:

1. **Identity** — NIC template ID, supported actions.
2. **Field table** — every field with: requiredness, NIC field-help text, and the
   target DCIM column/entity (`maps to`).
3. **Raw layout** — the verbatim dotted-line template body, preserved as a fenced
   block so a future renderer can reproduce the exact submission format.

## Rendering / emit (deferred)

The raw layout block in each spec is retained specifically so a later milestone can
implement template-text rendering and (optionally) auto-email to the NIC mailbox.
The first cut does **not** implement this — it only captures and validates the
structured data.
