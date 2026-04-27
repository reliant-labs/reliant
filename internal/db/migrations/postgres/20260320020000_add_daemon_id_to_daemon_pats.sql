-- +goose Up
ALTER TABLE daemon_pats ADD COLUMN IF NOT EXISTS daemon_id TEXT;

-- +goose Down
ALTER TABLE daemon_pats DROP COLUMN IF EXISTS daemon_id;
