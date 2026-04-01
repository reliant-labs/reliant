-- +goose Up
-- Migration: Add lineage columns to context_windows for direct CW chain traversal
--
-- This migration introduces:
-- 1. parent_context_window_id: Explicit link to parent context window
-- 2. fork_at_ordinal: For branches, the max ordinal to inherit from parent
--
-- Key insight: The context window chain becomes the "logical thread" - what the LLM
-- sees as context. This eliminates the need to derive the logical thread from
-- Thread fork relationships during message resolution.

-- Add new columns
ALTER TABLE context_windows ADD COLUMN parent_context_window_id TEXT REFERENCES context_windows(id) ON DELETE SET NULL;
ALTER TABLE context_windows ADD COLUMN fork_at_ordinal INTEGER;

-- Index for efficient parent traversal
CREATE INDEX idx_context_windows_parent ON context_windows(parent_context_window_id) 
    WHERE parent_context_window_id IS NOT NULL;

-- =============================================================================
-- BACKFILL EXISTING DATA
-- =============================================================================

-- Step 1: Link compaction chains (CWs with sequence > 0 link to previous sequence)
-- For each CW with sequence > 0, set parent_context_window_id to the CW with sequence-1 in same thread
UPDATE context_windows
SET parent_context_window_id = (
    SELECT prev.id
    FROM context_windows prev
    WHERE prev.thread_id = context_windows.thread_id
      AND prev.sequence = context_windows.sequence - 1
)
WHERE context_windows.sequence > 0;

-- Step 2: Link forked context windows
-- For threads that were forked (have fork_at_context_window_id), link their first CW
-- to the fork source CW and copy the fork_at_ordinal from the thread
UPDATE context_windows
SET 
    parent_context_window_id = (
        SELECT t.fork_at_context_window_id
        FROM threads t
        WHERE t.id = context_windows.thread_id
          AND t.fork_at_context_window_id IS NOT NULL
    ),
    fork_at_ordinal = (
        SELECT t.fork_at_ordinal
        FROM threads t
        WHERE t.id = context_windows.thread_id
          AND t.fork_at_context_window_id IS NOT NULL
    )
WHERE context_windows.sequence = 0
  AND context_windows.parent_context_window_id IS NULL
  AND EXISTS (
      SELECT 1 FROM threads t
      WHERE t.id = context_windows.thread_id
        AND t.fork_at_context_window_id IS NOT NULL
  );

-- +goose Down
DROP INDEX IF EXISTS idx_context_windows_parent;

-- SQLite doesn't support DROP COLUMN directly, so we need to recreate the table
-- For down migration, we just note that the columns will be removed
-- In practice, the down migration is rarely used and SQLite 3.35+ supports DROP COLUMN
ALTER TABLE context_windows DROP COLUMN fork_at_ordinal;
ALTER TABLE context_windows DROP COLUMN parent_context_window_id;
