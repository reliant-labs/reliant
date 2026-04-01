-- +goose Up
-- Migration: Create main worktrees for existing projects
-- This creates a worktree record for each project's main/default branch
-- and updates existing chats to reference their main worktree

-- First, mark any existing worktrees that match the project's default branch as is_main
-- This handles the case where a "main" worktree was created before is_main existed
UPDATE worktrees
SET is_main = TRUE
WHERE is_main = FALSE
AND branch = (SELECT p.default_branch FROM projects p WHERE p.id = worktrees.project_id)
AND NOT EXISTS (
    -- Only if no main worktree already exists for this project
    SELECT 1 FROM worktrees w2 WHERE w2.project_id = worktrees.project_id AND w2.is_main = TRUE
);

-- Then create main worktree for projects that still don't have one
INSERT INTO worktrees (
    id,
    name,
    path,
    branch,
    base_branch,
    project_id,
    status,
    is_main,
    created_at,
    updated_at,
    last_active
)
SELECT
    lower(hex(randomblob(16))) as id,  -- Generate UUID-like ID
    p.default_branch as name,           -- Use actual branch name (e.g., "main", "master")
    p.path as path,                     -- Use project's original path
    p.default_branch as branch,         -- Current branch
    p.default_branch as base_branch,    -- Base branch is itself
    p.id as project_id,
    'active' as status,
    TRUE as is_main,                    -- Mark as main worktree
    CURRENT_TIMESTAMP as created_at,
    CURRENT_TIMESTAMP as updated_at,
    CURRENT_TIMESTAMP as last_active
FROM projects p
WHERE NOT EXISTS (
    -- Only create if no main worktree exists for this project
    SELECT 1 FROM worktrees w WHERE w.project_id = p.id AND w.is_main = TRUE
);

-- Update chats with NULL worktree_id to reference their project's main worktree
UPDATE chats
SET worktree_id = (
    SELECT w.id
    FROM worktrees w
    WHERE w.project_id = chats.project_id
    AND w.is_main = TRUE
    LIMIT 1
)
WHERE worktree_id IS NULL
AND project_id IS NOT NULL;

-- +goose Down
-- Migration down: Remove main worktrees and restore NULL worktree_ids
-- WARNING: This is destructive and may lose data

-- Set worktree_id back to NULL for chats that reference main worktrees
UPDATE chats
SET worktree_id = NULL
WHERE worktree_id IN (
    SELECT id FROM worktrees WHERE is_main = TRUE
);

-- Delete main worktrees
DELETE FROM worktrees WHERE is_main = TRUE;
