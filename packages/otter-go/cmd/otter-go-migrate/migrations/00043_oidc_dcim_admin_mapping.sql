-- +goose Up

        INSERT INTO oidc_role_mappings
            (id, idp_role, dcim_role_id,
             scope_dimension, scope_target, claim_source)
        SELECT gen_random_uuid(), 'dcim-admin', id,
               NULL, NULL, 'keycloak'
        FROM roles WHERE name = 'EnterpriseAdmin'
        ON CONFLICT (idp_role) DO NOTHING;

-- +goose Down
DELETE FROM oidc_role_mappings WHERE idp_role = 'dcim-admin';
