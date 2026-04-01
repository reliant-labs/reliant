-- +goose Up
-- Add is_planning_mode column to chats table for persisting planning mode state
-- This allows planning mode to be toggled even when no workflow is active

ALTER TABLE chats ADD COLUMN is_planning_mode BOOLEAN NOT NULL DEFAULT FALSE;

-- Index for filtering queries by planning mode (optional optimization)
CREATE INDEX idx_chats_planning_mode ON chats(is_planning_mode);

-- +goose Down
DROP INDEX IF EXISTS idx_chats_planning_mode;
-- SQLite doesn't support DROP COLUMN directly in older versions
-- For modern SQLite (3.35.0+), we can drop the column
-- ALTER TABLE chats DROP COLUMN is_planning_mode;
