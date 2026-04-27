-- +goose Up
ALTER TABLE daemon_pats ADD COLUMN daemon_id TEXT;

-- +goose Down
-- SQLite doesn't support DROP COLUMN before 3.35.0; this is a best-effort rollback.
-- In production, use a table rebuild if needed.
