-- +goose Up
-- Migration: Add fork metadata to workflows table
-- This enables context inheritance between threads for handoff patterns.
--
-- forked_from_thread: Which thread to inherit context from (thread of parent)
-- forked_at_ordinal: Up to which message ordinal in parent thread to inherit
--
-- These are separate from parent_id which tracks execution hierarchy (who spawned whom).
-- A workflow can be spawned by workflow A but fork context from workflow B.

-- Add fork metadata columns
ALTER TABLE workflows ADD COLUMN forked_from_thread TEXT;
ALTER TABLE workflows ADD COLUMN forked_at_ordinal INTEGER;

-- Index for efficient fork resolution lookups
-- Partial index only includes rows where forked_from_thread is not null
CREATE INDEX idx_workflows_forked_from ON workflows(forked_from_thread) WHERE forked_from_thread IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_workflows_forked_from;
-- Note: SQLite doesn't support DROP COLUMN, so we'd need to recreate the table
-- For simplicity, we'll leave the columns in place during rollback
