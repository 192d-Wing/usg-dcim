-- +goose Up

        CREATE TABLE IF NOT EXISTS dhcp_scopes (
            id UUID PRIMARY KEY,
            dhcp_server_id UUID NOT NULL REFERENCES dhcp_servers(id) ON DELETE CASCADE,
            subnet_id UUID REFERENCES subnets(id) ON DELETE SET NULL,
            name VARCHAR(128) NOT NULL,
            ip_family SMALLINT NOT NULL,
            prefix CIDR NOT NULL,
            pools_json JSONB NOT NULL DEFAULT '[]'::jsonb,
            pd_pools_json JSONB,
            options_json JSONB NOT NULL DEFAULT '[]'::jsonb,
            reservations_json JSONB NOT NULL DEFAULT '[]'::jsonb,
            valid_lifetime_seconds INTEGER NOT NULL DEFAULT 3600,
            renew_timer_seconds INTEGER,
            rebind_timer_seconds INTEGER,
            preferred_lifetime_seconds INTEGER,
            enabled BOOLEAN NOT NULL DEFAULT TRUE,
            description VARCHAR(512),
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            CONSTRAINT ck_dhcp_scope_family CHECK (ip_family IN (4, 6)),
            -- v6-only timers must be NULL on v4 scopes; the API enforces
            -- the inverse too but the DB-level guard catches direct
            -- INSERTs and bad migrations.
            CONSTRAINT ck_dhcp_scope_v6_only CHECK (
                ip_family = 6
                OR (pd_pools_json IS NULL AND preferred_lifetime_seconds IS NULL)
            ),
            CONSTRAINT uq_dhcp_scope_server_prefix UNIQUE (dhcp_server_id, prefix)
        );
CREATE INDEX IF NOT EXISTS ix_dhcp_scopes_server ON dhcp_scopes (dhcp_server_id);
CREATE INDEX IF NOT EXISTS ix_dhcp_scopes_subnet ON dhcp_scopes (subnet_id) WHERE subnet_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS ix_dhcp_scopes_family ON dhcp_scopes (ip_family);

-- +goose Down
DROP TABLE IF EXISTS dhcp_scopes;
