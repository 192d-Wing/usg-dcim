-- DEV ONLY. Never apply to a real deployment.
-- Seeds roles + oidc_role_mappings for the usg-ops-console dev realm
-- roles dcim:viewer / dcim:operator / dcim:admin (local E2E). Keycloak
-- expands composites (operator also carries viewer; admin carries all
-- three) and otter-go unions the mapped capabilities, so each role
-- only lists what it adds. Idempotent.
--   psql "$DCIM_POSTGRES_DSN" -f db/dev/console_role_mappings.sql
INSERT INTO roles (id, name, description, permission_codes, is_system, created_at, updated_at) VALUES
  (gen_random_uuid(), 'DevConsoleViewer', 'DEV ONLY: console dcim:viewer',
   '["inventory:*:read","ipam:*:read","dns:*:read","routing:*:read","search:search:read","alerts:alerts:read","telemetry:*:read","dashboards:dashboards:read"]', false, NOW(), NOW()),
  (gen_random_uuid(), 'DevConsoleOperator', 'DEV ONLY: console dcim:operator',
   '["inventory:*:create","inventory:*:update","alerts:alerts:ack"]', false, NOW(), NOW()),
  (gen_random_uuid(), 'DevConsoleAdmin', 'DEV ONLY: console dcim:admin',
   '["*"]', false, NOW(), NOW())
ON CONFLICT (name) DO NOTHING;

INSERT INTO oidc_role_mappings (id, idp_role, dcim_role_id, claim_source, description)
SELECT gen_random_uuid(), m.idp_role, r.id, 'keycloak', 'DEV ONLY console seed'
FROM (VALUES ('dcim:viewer','DevConsoleViewer'),
             ('dcim:operator','DevConsoleOperator'),
             ('dcim:admin','DevConsoleAdmin')) AS m(idp_role, role_name)
JOIN roles r ON r.name = m.role_name
ON CONFLICT (idp_role) DO NOTHING;
