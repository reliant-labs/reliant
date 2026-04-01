-- +goose Up
-- Migration: Add worktree_id column to chats table
-- This enables chats to be associated with specific worktrees (git branches)

-- Add worktree_id column to chats table
ALTER TABLE chats ADD COLUMN worktree_id TEXT REFERENCES worktrees(id) ON DELETE SET NULL;

-- Create index for efficient worktree-based chat queries
CREATE INDEX idx_chats_worktree ON chats(worktree_id);

-- +goose Down
-- Remove the worktree association from chats
DROP INDEX IF EXISTS idx_chats_worktree;
ALTER TABLE chats DROP COLUMN worktree_id;
