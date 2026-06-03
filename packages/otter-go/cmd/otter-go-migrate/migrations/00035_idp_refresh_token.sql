-- +goose Up
ALTER TABLE users ADD COLUMN idp_refresh_token TEXT;
ALTER TABLE users ADD COLUMN idp_refresh_token_iat TIMESTAMPTZ;

-- +goose Down
ALTER TABLE users DROP COLUMN IF EXISTS idp_refresh_token_iat;
ALTER TABLE users DROP COLUMN IF EXISTS idp_refresh_token;
