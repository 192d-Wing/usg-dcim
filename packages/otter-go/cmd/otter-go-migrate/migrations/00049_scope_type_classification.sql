-- +goose Up
ALTER TYPE scope_type ADD VALUE IF NOT EXISTS 'classification';

-- +goose Down
-- (no-op downgrade)
