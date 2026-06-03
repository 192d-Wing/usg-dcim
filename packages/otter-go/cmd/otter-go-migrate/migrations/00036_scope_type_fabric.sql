-- +goose Up
ALTER TYPE scope_type ADD VALUE IF NOT EXISTS 'fabric';

-- +goose Down
-- (no-op downgrade)
