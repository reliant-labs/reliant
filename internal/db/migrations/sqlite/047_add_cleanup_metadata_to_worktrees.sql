-- +goose Up
-- Add cleanup_metadata column to worktrees table
-- This tracks whether directory and/or branch were deleted during archive
-- JSON field storing CleanupMetadata struct

ALTER TABLE worktrees ADD COLUMN cleanup_metadata TEXT;

-- +goose Down
-- SQLite doesn't support DROP COLUMN directly
-- The column will remain but be unused during rollback
