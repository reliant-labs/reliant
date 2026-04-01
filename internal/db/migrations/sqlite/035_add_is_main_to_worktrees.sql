-- +goose Up
-- Migration: Add is_main column to worktrees table
-- This allows us to identify the main/default project worktree

-- Add is_main column with default FALSE
ALTER TABLE worktrees ADD COLUMN is_main BOOLEAN NOT NULL DEFAULT FALSE;

-- Create index for efficient querying of main worktrees
CREATE INDEX idx_worktrees_is_main ON worktrees(project_id, is_main) WHERE is_main = TRUE;

-- Add constraint to ensure only one main worktree per project
CREATE UNIQUE INDEX idx_one_main_per_project ON worktrees(project_id) WHERE is_main = TRUE;

-- +goose Down
-- Remove the indexes and column
DROP INDEX IF EXISTS idx_one_main_per_project;
DROP INDEX IF EXISTS idx_worktrees_is_main;
ALTER TABLE worktrees DROP COLUMN is_main;
