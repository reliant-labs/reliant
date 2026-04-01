-- +goose Up
-- Fix old worktrees with zero-value last_active timestamps
-- This sets last_active to created_at for worktrees where last_active is not set
UPDATE worktrees 
SET last_active = created_at,
    updated_at = datetime('now', 'utc')
WHERE last_active IS NULL 
   OR last_active < '1970-01-01 00:00:00';

-- +goose Down
-- No rollback needed - this is a data fix migration
