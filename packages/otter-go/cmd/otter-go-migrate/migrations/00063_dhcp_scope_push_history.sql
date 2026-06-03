-- +goose Up

        CREATE TABLE IF NOT EXISTS dhcp_scope_push_history (
            id UUID PRIMARY KEY,
            scope_id UUID NOT NULL REFERENCES dhcp_scopes(id) ON DELETE CASCADE,
            server_id UUID NOT NULL REFERENCES dhcp_servers(id) ON DELETE CASCADE,
            operation VARCHAR(16) NOT NULL,
            kea_subnet_id INTEGER,
            status VARCHAR(16) NOT NULL,
            error VARCHAR(2048),
            duration_ms INTEGER,
            attempted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            CONSTRAINT ck_dhcp_push_history_status CHECK (
                status IN ('ok', 'error', 'unsupported')
            ),
            CONSTRAINT ck_dhcp_push_history_operation CHECK (
                operation IN ('add', 'update', 'delete')
            )
        );
CREATE INDEX IF NOT EXISTS ix_dhcp_scope_push_history_scope ON dhcp_scope_push_history (scope_id, attempted_at DESC);
CREATE INDEX IF NOT EXISTS ix_dhcp_scope_push_history_server ON dhcp_scope_push_history (server_id, attempted_at DESC);

-- +goose Down
DROP TABLE IF EXISTS dhcp_scope_push_history;
