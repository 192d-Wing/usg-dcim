"""LIR module — pools, requests, allocations, supernet ownership, landing fabric.

Phase 1 of the Local Internet Registry workflow. DoW NIC carves
sub-allocations out of registered ARIN aggregates and hands them to
internal organizations; this migration lays down the storage.

Shape:
  * `lir_pools` — named buckets of supernet space DoW will sub-allocate
    from. Pinned to an IP family + the ARIN parent net handle that
    Reg-RWS reassignments POST to. min/max prefix length bound what
    sizes the pool will issue.
  * `lir_requests` — tenant-submitted request rows with a state machine
    (pending_approval → approved | rejected | cancelled | failed).
    organization_id is the tenant; requester_user_id is the natural
    person.
  * `lir_allocations` — created when a NIC operator approves a request.
    1:1 with the request; tracks the carved tenant Supernet, the
    return lifecycle, and ARIN Reg-RWS state. arin_status starts at
    'none' (no ARIN integration yet) and gets driven by the worker
    once phase 5 lands.
  * Two new nullable columns on `supernets`:
      - `lir_pool_id`  — flags a supernet as a pool source.
      - `owner_organization_id` — flags a supernet as tenant-owned.
    A CHECK constraint forbids both being set on the same row: a
    supernet is either a pool source DoW is carving from, or a tenant
    allocation; never both.
  * `fabrics.is_system` — protects the landing fabric (and any future
    system-managed fabric) from accidental UI deletion.
  * Seed: a system fabric `lir-unassigned` plus its default VRF.
    Approvals land tenant supernets here; the IPAM 'move' endpoint
    relocates them once the tenant picks operational fabric/VRF.

Pool → tenant linkage uses `lir_allocations` rows, NOT
`supernets.parent_supernet_id`. The two Supernet trees (pool side in
the operational fabric, tenant side starting in the landing fabric)
stay independent — keeps the move endpoint trivial since the parent
FK doesn't need rewriting.
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260528_0065"
down_revision: str | None = "20260525_0064"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    # ---- fabrics.is_system ------------------------------------------------
    # Guards the landing fabric below (and any future system-managed
    # fabric) from delete in the UI. Default FALSE so existing fabrics
    # keep their semantics.
    op.execute(
        "ALTER TABLE fabrics ADD COLUMN IF NOT EXISTS is_system BOOLEAN "
        "NOT NULL DEFAULT FALSE"
    )

    # ---- lir_pools --------------------------------------------------------
    op.execute(
        """
        CREATE TABLE IF NOT EXISTS lir_pools (
            id UUID PRIMARY KEY,
            name VARCHAR(128) NOT NULL,
            slug VARCHAR(64) NOT NULL,
            description VARCHAR(512),
            ip_family SMALLINT NOT NULL,
            -- Operational fabric the pool supernets live in. Informational
            -- for the NIC UI; allocation placement always goes through
            -- the landing fabric and then the tenant's move.
            fabric_id UUID REFERENCES fabrics(id),
            classification VARCHAR(32),
            min_prefix_length SMALLINT NOT NULL,
            max_prefix_length SMALLINT NOT NULL,
            -- Optional default purpose stamped on the carved tenant
            -- Supernet (e.g. 'data', 'mgmt'). Tenant can change later.
            default_supernet_purpose VARCHAR(32),
            -- ARIN net handle reassignments POST under. NULL = pool is
            -- LIR-internal only (no upstream feed-up).
            arin_parent_net_handle VARCHAR(64),
            enabled BOOLEAN NOT NULL DEFAULT TRUE,
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            CONSTRAINT uq_lir_pool_name UNIQUE (name),
            CONSTRAINT uq_lir_pool_slug UNIQUE (slug),
            CONSTRAINT ck_lir_pool_family CHECK (ip_family IN (4, 6)),
            CONSTRAINT ck_lir_pool_prefix_bounds CHECK (
                min_prefix_length >= 0
                AND max_prefix_length >= min_prefix_length
                AND (
                    (ip_family = 4 AND max_prefix_length <= 32)
                    OR (ip_family = 6 AND max_prefix_length <= 128)
                )
            )
        )
        """
    )
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_lir_pools_fabric "
        "ON lir_pools (fabric_id) WHERE fabric_id IS NOT NULL"
    )
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_lir_pools_family "
        "ON lir_pools (ip_family)"
    )
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_lir_pools_enabled "
        "ON lir_pools (enabled)"
    )

    # ---- supernets.lir_pool_id + supernets.owner_organization_id ---------
    # Two nullable FKs, mutually exclusive (CHECK below). A pool supernet
    # has lir_pool_id; a tenant supernet has owner_organization_id;
    # neither set is a plain non-LIR supernet (the rest of IPAM).
    op.execute(
        "ALTER TABLE supernets ADD COLUMN IF NOT EXISTS lir_pool_id UUID "
        "REFERENCES lir_pools(id) ON DELETE SET NULL"
    )
    op.execute(
        "ALTER TABLE supernets ADD COLUMN IF NOT EXISTS owner_organization_id UUID "
        "REFERENCES organizations(id) ON DELETE SET NULL"
    )
    op.execute(
        "ALTER TABLE supernets DROP CONSTRAINT IF EXISTS ck_supernet_lir_xor_owner"
    )
    op.execute(
        """
        ALTER TABLE supernets ADD CONSTRAINT ck_supernet_lir_xor_owner CHECK (
            NOT (lir_pool_id IS NOT NULL AND owner_organization_id IS NOT NULL)
        )
        """
    )
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_supernets_lir_pool "
        "ON supernets (lir_pool_id) WHERE lir_pool_id IS NOT NULL"
    )
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_supernets_owner_org "
        "ON supernets (owner_organization_id) "
        "WHERE owner_organization_id IS NOT NULL"
    )

    # ---- lir_requests -----------------------------------------------------
    op.execute(
        """
        CREATE TABLE IF NOT EXISTS lir_requests (
            id UUID PRIMARY KEY,
            organization_id UUID NOT NULL REFERENCES organizations(id),
            requester_user_id UUID NOT NULL REFERENCES users(id),
            -- Tenant's pool preference; NIC may approve into a
            -- different pool (approved_pool_id below).
            pool_id UUID REFERENCES lir_pools(id),
            site_id UUID REFERENCES sites(id),
            ip_family SMALLINT NOT NULL,
            prefix_length SMALLINT NOT NULL,
            -- Optional carve-out hint stamped on the tenant Supernet
            -- (e.g. 'data', 'mgmt'). Falls back to the pool's default.
            purpose VARCHAR(32),
            -- Classification on the request itself, captured here so a
            -- future enforcement pass (matching the move target's
            -- fabric classification) doesn't need a schema change.
            classification VARCHAR(32),
            justification TEXT NOT NULL,
            status VARCHAR(32) NOT NULL DEFAULT 'pending_approval',
            submitted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            decided_at TIMESTAMPTZ,
            decided_by_user_id UUID REFERENCES users(id),
            decision_notes VARCHAR(2048),
            -- Pool the NIC ultimately approved into. Set on approve,
            -- null otherwise. Differs from `pool_id` when the NIC
            -- redirected a request to a different pool.
            approved_pool_id UUID REFERENCES lir_pools(id),
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            CONSTRAINT ck_lir_request_family CHECK (ip_family IN (4, 6)),
            CONSTRAINT ck_lir_request_prefix_bounds CHECK (
                prefix_length >= 0
                AND (
                    (ip_family = 4 AND prefix_length <= 32)
                    OR (ip_family = 6 AND prefix_length <= 128)
                )
            ),
            CONSTRAINT ck_lir_request_status CHECK (
                status IN (
                    'pending_approval',
                    'approved',
                    'rejected',
                    'cancelled',
                    'failed'
                )
            ),
            -- Once a request is decided it stores who/when in decided_*.
            -- pending/cancelled never set them; approved/rejected/failed
            -- always do. Enforced here so a failed transition can't
            -- leave the row in a half-decided state.
            CONSTRAINT ck_lir_request_decision_consistency CHECK (
                (status IN ('pending_approval', 'cancelled'))
                OR (decided_at IS NOT NULL AND decided_by_user_id IS NOT NULL)
            )
        )
        """
    )
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_lir_requests_org "
        "ON lir_requests (organization_id)"
    )
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_lir_requests_requester "
        "ON lir_requests (requester_user_id)"
    )
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_lir_requests_status "
        "ON lir_requests (status)"
    )
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_lir_requests_pool "
        "ON lir_requests (pool_id) WHERE pool_id IS NOT NULL"
    )
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_lir_requests_submitted_at "
        "ON lir_requests (submitted_at DESC)"
    )

    # ---- lir_allocations --------------------------------------------------
    op.execute(
        """
        CREATE TABLE IF NOT EXISTS lir_allocations (
            id UUID PRIMARY KEY,
            request_id UUID NOT NULL REFERENCES lir_requests(id),
            -- Denormalized off the request so ABAC org-scope filters
            -- and per-org listings don't pay the join cost.
            organization_id UUID NOT NULL REFERENCES organizations(id),
            pool_id UUID NOT NULL REFERENCES lir_pools(id),
            -- The pool supernet that was carved. Stays in the
            -- operational fabric; the carved range is recorded here
            -- rather than as a parent FK on the tenant Supernet so
            -- the tenant 'move' endpoint doesn't have to rewrite FKs.
            pool_supernet_id UUID NOT NULL REFERENCES supernets(id),
            -- The tenant-owned Supernet created at approve time. Lives
            -- in the landing fabric until the tenant moves it.
            tenant_supernet_id UUID NOT NULL REFERENCES supernets(id),
            prefix CIDR NOT NULL,
            allocated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            allocated_by_user_id UUID NOT NULL REFERENCES users(id),
            status VARCHAR(32) NOT NULL DEFAULT 'active',
            return_requested_at TIMESTAMPTZ,
            return_requested_by_user_id UUID REFERENCES users(id),
            return_reason VARCHAR(2048),
            returned_at TIMESTAMPTZ,
            returned_by_user_id UUID REFERENCES users(id),
            -- ARIN Reg-RWS state. 'none' = ARIN integration disabled
            -- for this allocation (no parent net handle on the pool,
            -- or feature flag off). 'pending' = queued / in-flight.
            -- 'registered' = ARIN acked the reassignment. 'failed' =
            -- last attempt errored; retryable via the manual endpoint.
            -- 'removing' / 'removed' track the deassignment direction.
            arin_status VARCHAR(32) NOT NULL DEFAULT 'none',
            arin_net_handle VARCHAR(64),
            arin_last_attempt_at TIMESTAMPTZ,
            arin_last_error VARCHAR(2048),
            arin_attempts INTEGER NOT NULL DEFAULT 0,
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            CONSTRAINT uq_lir_allocation_request UNIQUE (request_id),
            CONSTRAINT uq_lir_allocation_tenant_supernet
                UNIQUE (tenant_supernet_id),
            CONSTRAINT ck_lir_allocation_status CHECK (
                status IN ('active', 'return_requested', 'returned')
            ),
            CONSTRAINT ck_lir_allocation_arin_status CHECK (
                arin_status IN (
                    'none', 'pending', 'registered', 'failed',
                    'removing', 'removed'
                )
            )
        )
        """
    )
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_lir_allocations_org "
        "ON lir_allocations (organization_id)"
    )
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_lir_allocations_pool "
        "ON lir_allocations (pool_id)"
    )
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_lir_allocations_pool_supernet "
        "ON lir_allocations (pool_supernet_id)"
    )
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_lir_allocations_status "
        "ON lir_allocations (status)"
    )
    # Worker lookup index — fish out 'pending' and 'failed' rows
    # ordered by last attempt time for backoff scheduling.
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_lir_allocations_arin_worker "
        "ON lir_allocations (arin_status, arin_last_attempt_at) "
        "WHERE arin_status IN ('pending', 'failed', 'removing')"
    )

    # ---- Seed: lir-unassigned landing fabric + default VRF ---------------
    # System fabric. New allocations land here; the IPAM move endpoint
    # relocates them. Looked up by slug at runtime, so the UUID is
    # generated and need not be stable across environments.
    op.execute(
        """
        WITH new_fabric AS (
            INSERT INTO fabrics (
                id, name, slug, description, is_system,
                recursive_engine, created_at, updated_at
            )
            VALUES (
                gen_random_uuid(),
                'LIR Unassigned (landing)',
                'lir-unassigned',
                'System-managed landing fabric for LIR allocations '
                'pending tenant placement. Do not delete.',
                TRUE,
                'coredns',
                NOW(), NOW()
            )
            ON CONFLICT DO NOTHING
            RETURNING id
        )
        INSERT INTO vrfs (
            id, fabric_id, name, description, is_default,
            created_at, updated_at
        )
        SELECT
            gen_random_uuid(), id, 'default',
            'Default VRF for the LIR landing fabric.',
            TRUE, NOW(), NOW()
        FROM new_fabric
        """
    )


def downgrade() -> None:
    # Reverse-order teardown. Tenant Supernet rows referenced by
    # lir_allocations.tenant_supernet_id will be left in place — the
    # owner_organization_id column also goes away, so they revert to
    # ordinary supernets. Operators clean those up out-of-band.
    op.execute("DROP TABLE IF EXISTS lir_allocations")
    op.execute("DROP TABLE IF EXISTS lir_requests")

    op.execute("DROP INDEX IF EXISTS ix_supernets_owner_org")
    op.execute("DROP INDEX IF EXISTS ix_supernets_lir_pool")
    op.execute(
        "ALTER TABLE supernets DROP CONSTRAINT IF EXISTS ck_supernet_lir_xor_owner"
    )
    op.execute("ALTER TABLE supernets DROP COLUMN IF EXISTS owner_organization_id")
    op.execute("ALTER TABLE supernets DROP COLUMN IF EXISTS lir_pool_id")

    op.execute("DROP TABLE IF EXISTS lir_pools")

    # Tear down the landing fabric (and its default VRF via cascade
    # if FK uses it; otherwise the explicit DELETE below).
    op.execute(
        "DELETE FROM vrfs WHERE fabric_id IN ("
        "SELECT id FROM fabrics WHERE slug = 'lir-unassigned'"
        ")"
    )
    op.execute("DELETE FROM fabrics WHERE slug = 'lir-unassigned'")

    op.execute("ALTER TABLE fabrics DROP COLUMN IF EXISTS is_system")
