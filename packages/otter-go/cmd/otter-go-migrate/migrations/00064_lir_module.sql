-- +goose Up
ALTER TABLE fabrics ADD COLUMN IF NOT EXISTS is_system BOOLEAN NOT NULL DEFAULT FALSE;

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
        );
CREATE INDEX IF NOT EXISTS ix_lir_pools_fabric ON lir_pools (fabric_id) WHERE fabric_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS ix_lir_pools_family ON lir_pools (ip_family);
CREATE INDEX IF NOT EXISTS ix_lir_pools_enabled ON lir_pools (enabled);
ALTER TABLE supernets ADD COLUMN IF NOT EXISTS lir_pool_id UUID REFERENCES lir_pools(id) ON DELETE SET NULL;
ALTER TABLE supernets ADD COLUMN IF NOT EXISTS owner_organization_id UUID REFERENCES organizations(id) ON DELETE SET NULL;
ALTER TABLE supernets DROP CONSTRAINT IF EXISTS ck_supernet_lir_xor_owner;

        ALTER TABLE supernets ADD CONSTRAINT ck_supernet_lir_xor_owner CHECK (
            NOT (lir_pool_id IS NOT NULL AND owner_organization_id IS NOT NULL)
        );
CREATE INDEX IF NOT EXISTS ix_supernets_lir_pool ON supernets (lir_pool_id) WHERE lir_pool_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS ix_supernets_owner_org ON supernets (owner_organization_id) WHERE owner_organization_id IS NOT NULL;

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
        );
CREATE INDEX IF NOT EXISTS ix_lir_requests_org ON lir_requests (organization_id);
CREATE INDEX IF NOT EXISTS ix_lir_requests_requester ON lir_requests (requester_user_id);
CREATE INDEX IF NOT EXISTS ix_lir_requests_status ON lir_requests (status);
CREATE INDEX IF NOT EXISTS ix_lir_requests_pool ON lir_requests (pool_id) WHERE pool_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS ix_lir_requests_submitted_at ON lir_requests (submitted_at DESC);

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
        );
CREATE INDEX IF NOT EXISTS ix_lir_allocations_org ON lir_allocations (organization_id);
CREATE INDEX IF NOT EXISTS ix_lir_allocations_pool ON lir_allocations (pool_id);
CREATE INDEX IF NOT EXISTS ix_lir_allocations_pool_supernet ON lir_allocations (pool_supernet_id);
CREATE INDEX IF NOT EXISTS ix_lir_allocations_status ON lir_allocations (status);
CREATE INDEX IF NOT EXISTS ix_lir_allocations_arin_worker ON lir_allocations (arin_status, arin_last_attempt_at) WHERE arin_status IN ('pending', 'failed', 'removing');

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
        FROM new_fabric;

-- +goose Down
DROP TABLE IF EXISTS lir_allocations;
DROP TABLE IF EXISTS lir_requests;
DROP INDEX IF EXISTS ix_supernets_owner_org;
DROP INDEX IF EXISTS ix_supernets_lir_pool;
ALTER TABLE supernets DROP CONSTRAINT IF EXISTS ck_supernet_lir_xor_owner;
ALTER TABLE supernets DROP COLUMN IF EXISTS owner_organization_id;
ALTER TABLE supernets DROP COLUMN IF EXISTS lir_pool_id;
DROP TABLE IF EXISTS lir_pools;
DELETE FROM vrfs WHERE fabric_id IN (SELECT id FROM fabrics WHERE slug = 'lir-unassigned');
DELETE FROM fabrics WHERE slug = 'lir-unassigned';
ALTER TABLE fabrics DROP COLUMN IF EXISTS is_system;
