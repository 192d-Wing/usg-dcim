-- +goose Up
ALTER TABLE oidc_role_mappings ADD COLUMN scope_dimension scope_type;
ALTER TABLE oidc_role_mappings ADD COLUMN scope_target VARCHAR(255);

-- +goose Down
ALTER TABLE oidc_role_mappings DROP COLUMN IF EXISTS scope_target;
ALTER TABLE oidc_role_mappings DROP COLUMN IF EXISTS scope_dimension;
