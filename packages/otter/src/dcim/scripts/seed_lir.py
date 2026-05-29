"""Seed a minimal LIR module dataset for local dev.

What this loads (idempotently — re-runs are no-ops):

  * Organization "DoW NIC" — the LIR operator org.
  * Organization "Example Tenant" — the customer org that submits
    the sample request below.
  * Fabric "dow-nipr" + default VRF — where the pool's source
    supernet lives operationally. Distinct from the seeded landing
    fabric "lir-unassigned" (migration 0065).
  * Supernet 10.99.0.0/16 in dow-nipr, flagged as a pool source.
  * LirPool "DoW IPv4 NIPR" — slug `dow-v4-nipr`, family v4, prefix
    range /20-/29, ARIN parent handle blank (LIR-internal).
  * LirRequest from admin@dcim.local on behalf of "Example Tenant"
    asking for an IPv4 /28 — sits in `pending_approval` so the
    NIC UI's Approval queue surfaces it on first sign-in.

LIR tables (lir_pools, lir_requests) are written via raw SQL because
the Python side intentionally doesn't carry SQLAlchemy models for
them — the module's API lives in otter-go and reads via sqlc. Shared
tables (organizations, fabrics, vrfs, supernets, users) still go
through their existing ORM models.

Run via:  make seed-lir-local
"""

from __future__ import annotations

import asyncio

from sqlalchemy import select, text

from ..db import async_session
from ..models.auth import User
from ..models.ipam import Fabric, Supernet, Vrf
from ..models.organization import Organization

# Dev-only sample range. RFC 1918 private space chosen deliberately so
# nothing the seed creates can leak into a production routing table if
# this script is mistakenly run elsewhere. Change here if 10.99.0.0/16
# clashes with a lab network you actually use.
SAMPLE_POOL_SUPERNET = "10.99.0.0/16"  # NOSONAR S1313 — RFC1918, dev-only seed


# ---- helpers ---------------------------------------------------------

async def _get_or_create_org(db, name: str) -> Organization:
    existing = (
        await db.execute(select(Organization).where(Organization.name == name))
    ).scalar_one_or_none()
    if existing is not None:
        return existing
    org = Organization(
        name=name,
        arin_org_id=None,
        address_line1="1 Example Way",
        city="Arlington",
        state_province="VA",
        postal_code="22202",
        country="US",
        admin_poc_name="Sample Admin",
        admin_poc_email=f"admin@{name.lower().replace(' ', '')}.example",
        tech_poc_name="Sample Tech",
        tech_poc_email=f"tech@{name.lower().replace(' ', '')}.example",
        abuse_poc_name="Sample Abuse",
        abuse_poc_email=f"abuse@{name.lower().replace(' ', '')}.example",
    )
    db.add(org)
    await db.flush()
    return org


async def _get_or_create_fabric(db, name: str, slug: str) -> tuple[Fabric, Vrf]:
    fabric = (
        await db.execute(select(Fabric).where(Fabric.slug == slug))
    ).scalar_one_or_none()
    if fabric is None:
        fabric = Fabric(name=name, slug=slug, classification="NIPR")
        db.add(fabric)
        await db.flush()
    # Ensure a default VRF exists. Production code auto-creates one on
    # Fabric insert; the seed mirrors that explicitly so a re-run after
    # a partial failure backfills cleanly.
    default_vrf = (
        await db.execute(
            select(Vrf).where(
                Vrf.fabric_id == fabric.id,
                Vrf.is_default == True,  # noqa: E712
            ),
        )
    ).scalar_one_or_none()
    if default_vrf is None:
        default_vrf = Vrf(fabric_id=fabric.id, name="default", is_default=True)
        db.add(default_vrf)
        await db.flush()
    return fabric, default_vrf


# ---- LIR-table writes (raw SQL — no Python ORM models) --------------

# Idempotency keys for the LIR rows the seed plants. Each helper does
# the SELECT-then-INSERT-if-missing dance directly via SQL so re-runs
# stay no-ops without needing an ON CONFLICT clause (which would need
# unique indexes we haven't added).

_GET_POOL = text(
    "SELECT id FROM lir_pools WHERE slug = :slug",
)
_INSERT_POOL = text(
    """
    INSERT INTO lir_pools (
        id, name, slug, description, ip_family,
        classification, min_prefix_length, max_prefix_length,
        default_supernet_purpose, arin_parent_net_handle, enabled,
        created_at, updated_at
    )
    VALUES (
        gen_random_uuid(), :name, :slug, :description, 4,
        'NIPR', 20, 29,
        'data', NULL, TRUE,
        NOW(), NOW()
    )
    RETURNING id
    """,
)

_GET_REQUEST = text(
    """
    SELECT id FROM lir_requests
    WHERE organization_id = :org_id
      AND requester_user_id = :user_id
      AND justification LIKE 'Seeded sample request%'
    """,
)
_INSERT_REQUEST = text(
    """
    INSERT INTO lir_requests (
        id, organization_id, requester_user_id,
        ip_family, prefix_length, purpose, justification,
        status, submitted_at, created_at, updated_at
    )
    VALUES (
        gen_random_uuid(), :org_id, :user_id,
        4, 28, 'data', :justification,
        'pending_approval', NOW(), NOW(), NOW()
    )
    RETURNING id
    """,
)


async def _get_or_create_pool(db) -> str:
    pool_id = (await db.execute(_GET_POOL, {"slug": "dow-v4-nipr"})).scalar_one_or_none()
    if pool_id is not None:
        return str(pool_id)
    pool_id = (
        await db.execute(
            _INSERT_POOL,
            {
                "name": "DoW IPv4 NIPR",
                "slug": "dow-v4-nipr",
                "description": (
                    "Seeded sample pool. Carves /28-/20 IPv4 blocks for NIPR tenants."
                ),
            },
        )
    ).scalar_one()
    return str(pool_id)


async def _get_or_create_pool_supernet(
    db, fabric: Fabric, vrf: Vrf, prefix: str, pool_id: str,
) -> Supernet:
    existing = (
        await db.execute(
            select(Supernet).where(
                Supernet.fabric_id == fabric.id,
                Supernet.prefix == prefix,
            ),
        )
    ).scalar_one_or_none()
    if existing is not None:
        if str(existing.lir_pool_id) != pool_id:
            # Re-link in case the pool got recreated.
            await db.execute(
                text(
                    "UPDATE supernets SET lir_pool_id = :pid WHERE id = :sid",
                ),
                {"pid": pool_id, "sid": existing.id},
            )
        return existing
    supernet = Supernet(
        fabric_id=fabric.id,
        vrf_id=vrf.id,
        prefix=prefix,
        name=f"{fabric.slug} aggregate",
        description="Seeded by seed_lir.",
        purpose="data",
    )
    db.add(supernet)
    await db.flush()
    await db.execute(
        text("UPDATE supernets SET lir_pool_id = :pid WHERE id = :sid"),
        {"pid": pool_id, "sid": supernet.id},
    )
    return supernet


async def _get_or_create_sample_request(
    db, tenant: Organization, requester: User,
) -> str:
    existing = (
        await db.execute(
            _GET_REQUEST, {"org_id": tenant.id, "user_id": requester.id},
        )
    ).scalar_one_or_none()
    if existing is not None:
        return str(existing)
    req_id = (
        await db.execute(
            _INSERT_REQUEST,
            {
                "org_id": tenant.id,
                "user_id": requester.id,
                "justification": (
                    "Seeded sample request. Need a /28 for lab segment at the "
                    "example tenant's primary site."
                ),
            },
        )
    ).scalar_one()
    return str(req_id)


# ---- entrypoint ------------------------------------------------------

async def seed() -> None:
    async with async_session() as db:
        # The admin user is created by seed_demo. Look it up; bail
        # politely if the dev hasn't run that yet, since the request
        # row needs a real requester_user_id.
        admin = (
            await db.execute(
                select(User).where(User.email == "admin@dcim.local"),
            )
        ).scalar_one_or_none()
        if admin is None:
            print(
                "seed_lir: admin@dcim.local not found. "
                "Run `make seed-local` first (or sign in via OIDC and re-run).",
            )
            return

        _ = await _get_or_create_org(db, "DoW NIC")
        tenant = await _get_or_create_org(db, "Example Tenant")
        await db.commit()

        fabric, vrf = await _get_or_create_fabric(db, "DoW NIPR", "dow-nipr")
        pool_id = await _get_or_create_pool(db)
        await _get_or_create_pool_supernet(
            db, fabric, vrf, SAMPLE_POOL_SUPERNET, pool_id,
        )
        await _get_or_create_sample_request(db, tenant, admin)
        await db.commit()

        print("seed_lir: OK")
        print("  organizations: DoW NIC + Example Tenant")
        print("  fabric:        dow-nipr (operational)")
        print("  pool:          dow-v4-nipr (v4, /20-/29)")
        print("  supernet:      10.99.0.0/16 attached to pool")
        print("  request:       pending /28 from Example Tenant — approve in /lir")


if __name__ == "__main__":
    asyncio.run(seed())
