-- +goose Up
-- Original alembic revision 20260512_0030 added a snapshot column AND
-- ran a Python-side data migration that rewrote every role's
-- permission_codes from the new BUILT_IN_ROLES bundles + a
-- LEGACY_CODE_EXPANSION map. The data-migration body was a one-shot
-- historical artifact that the alembic source said "on a fresh install
-- runs against an empty roles table and is a no-op" — so for goose we
-- only carry the schema change. Cutover databases have the data
-- migration in their history already (alembic ran it); fresh databases
-- start with an empty roles table and seed_demo / the operator's role
-- bootstrap fills it from current BUILT_IN_ROLES directly.
ALTER TABLE roles ADD COLUMN legacy_permission_codes JSON;

-- +goose Down
ALTER TABLE roles DROP COLUMN IF EXISTS legacy_permission_codes;
