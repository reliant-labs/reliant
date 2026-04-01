-- +goose Up
-- Migration: Add forked_at_context_seq to workflows table
-- This captures the parent's context_sequence at fork time.
--
-- Problem: When a child chat branches from a parent that has compacted,
-- the child's context_sequence (0) doesn't match the parent's (e.g., 1).
-- GetMessagesForLLMContext was using the child's context_sequence to query
-- parent messages, resulting in 0 messages being returned.
--
-- Solution: Store the parent's context_sequence at fork time and use it
-- when resolving inherited messages.

-- Add forked_at_context_seq column
ALTER TABLE workflows ADD COLUMN forked_at_context_seq INTEGER;

-- +goose Down
-- Note: SQLite doesn't support DROP COLUMN, so we'd need to recreate the table
-- For simplicity, we'll leave the column in place during rollback
