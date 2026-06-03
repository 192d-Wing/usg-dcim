-- +goose Up

        CREATE TABLE IF NOT EXISTS dhcp_scope_templates (
            id UUID PRIMARY KEY,
            fabric_id UUID NOT NULL REFERENCES fabrics(id) ON DELETE CASCADE,
            name VARCHAR(128) NOT NULL,
            ip_family SMALLINT NOT NULL,
            options_json JSONB NOT NULL DEFAULT '[]'::jsonb,
            valid_lifetime_seconds INTEGER,
            renew_timer_seconds INTEGER,
            rebind_timer_seconds INTEGER,
            preferred_lifetime_seconds INTEGER,
            description VARCHAR(512),
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            CONSTRAINT ck_dhcp_scope_template_family CHECK (ip_family IN (4, 6)),
            CONSTRAINT ck_dhcp_scope_template_v6_only CHECK (
                ip_family = 6 OR preferred_lifetime_seconds IS NULL
            ),
            CONSTRAINT uq_dhcp_scope_template_fabric_name UNIQUE (fabric_id, name)
        );
CREATE INDEX IF NOT EXISTS ix_dhcp_scope_templates_fabric ON dhcp_scope_templates (fabric_id);
CREATE INDEX IF NOT EXISTS ix_dhcp_scope_templates_family ON dhcp_scope_templates (ip_family);
ALTER TABLE dhcp_scopes ADD COLUMN IF NOT EXISTS template_id UUID REFERENCES dhcp_scope_templates(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS ix_dhcp_scopes_template ON dhcp_scopes (template_id) WHERE template_id IS NOT NULL;
ALTER TABLE dhcp_scopes ALTER COLUMN valid_lifetime_seconds DROP NOT NULL;
ALTER TABLE dhcp_scopes ALTER COLUMN valid_lifetime_seconds DROP DEFAULT;

-- +goose Down
UPDATE dhcp_scopes SET valid_lifetime_seconds = 3600 WHERE valid_lifetime_seconds IS NULL;
ALTER TABLE dhcp_scopes ALTER COLUMN valid_lifetime_seconds SET DEFAULT 3600;
ALTER TABLE dhcp_scopes ALTER COLUMN valid_lifetime_seconds SET NOT NULL;
DROP INDEX IF EXISTS ix_dhcp_scopes_template;
ALTER TABLE dhcp_scopes DROP COLUMN IF EXISTS template_id;
DROP TABLE IF EXISTS dhcp_scope_templates;
