-- Fix projects unique constraint to be per user
-- Multiple users should be able to work on the same project path

-- +goose Up
-- SQLite doesn't support modifying constraints, so we need to recreate the table

-- Create new table with correct unique constraint
CREATE TABLE projects_new (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    path TEXT NOT NULL,
    user_id TEXT NOT NULL,
    description TEXT,
    is_git_repo BOOLEAN NOT NULL DEFAULT TRUE,
    default_branch TEXT DEFAULT 'main',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_active TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(user_id, path)
);

-- Copy data from old table
INSERT INTO projects_new (
    id, name, path, user_id, description, is_git_repo, default_branch,
    created_at, updated_at, last_active
)
SELECT
    id, name, path, user_id, description, is_git_repo, default_branch,
    created_at, updated_at, last_active
FROM projects;

-- Drop old table
DROP TABLE projects;

-- Rename new table
ALTER TABLE projects_new RENAME TO projects;

-- Recreate indexes
CREATE INDEX idx_projects_user_id ON projects(user_id);
CREATE INDEX idx_projects_last_active ON projects(last_active DESC);

-- +goose Down
-- This is a destructive migration - no safe way to roll back
-- Would need to handle the constraint changes carefully in production
