-- +goose Up
-- usg-ops-console embed (OTTER_AUTH_MODE=console|both): the console
-- assertion forwards Keycloak realm roles with the "dcim:" prefix.
-- Map dcim:admin to the built-in EnterpriseAdmin role, mirroring
-- 00043 (dcim-admin). No-op when the role has not been bootstrapped.
-- dcim:viewer / dcim:operator map to deployment-defined roles via the
-- admin OIDC-mappings API; a DEV ONLY seed lives in
-- db/dev/console_role_mappings.sql. Keycloak expands composites, so an
-- admin carries all three roles and capabilities are the union.
        INSERT INTO oidc_role_mappings
            (id, idp_role, dcim_role_id,
             scope_dimension, scope_target, claim_source)
        SELECT gen_random_uuid(), 'dcim:admin', id,
               NULL, NULL, 'keycloak'
        FROM roles WHERE name = 'EnterpriseAdmin'
        ON CONFLICT (idp_role) DO NOTHING;

-- +goose Down
DELETE FROM oidc_role_mappings WHERE idp_role = 'dcim:admin';
