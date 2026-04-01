-- +goose Up
-- Remove dead cancelled_at column from chats table.
-- The old DB-polling cancellation mechanism has been replaced with
-- Temporal signal-based activity cancellation.

ALTER TABLE chats
    DROP COLUMN IF EXISTS cancelled_at;

DROP INDEX IF EXISTS idx_chats_cancelled_at;

-- +goose Down
ALTER TABLE chats
    ADD COLUMN cancelled_at TIMESTAMP;

CREATE INDEX IF NOT EXISTS idx_chats_cancelled_at ON chats(id, cancelled_at);
