-- +goose Up
ALTER TABLE fabrics ADD COLUMN catalog_transfer_acl JSON;

-- +goose Down
ALTER TABLE fabrics DROP COLUMN IF EXISTS catalog_transfer_acl;
