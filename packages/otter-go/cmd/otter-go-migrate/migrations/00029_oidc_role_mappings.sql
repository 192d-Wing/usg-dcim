-- +goose Up

        CREATE TABLE oidc_role_mappings (
            id UUID PRIMARY KEY,
            idp_role VARCHAR(255) NOT NULL,
            claim_source VARCHAR(64) NOT NULL DEFAULT 'keycloak',
            dcim_role_id UUID NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
            description VARCHAR(255),
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            CONSTRAINT uq_oidc_role_mapping_idp_role UNIQUE (idp_role)
        );
CREATE INDEX ix_oidc_role_mappings_dcim_role ON oidc_role_mappings (dcim_role_id);

-- +goose Down
DROP INDEX IF EXISTS ix_oidc_role_mappings_dcim_role;
DROP TABLE IF EXISTS oidc_role_mappings;
