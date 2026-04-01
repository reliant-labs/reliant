-- Migration: Make project_id required for chats
-- Chats must belong to a project

-- +goose Up
-- First, set a default project_id for any existing chats that don't have one
-- (This should not happen in production, but handles edge cases)
UPDATE chats
SET project_id = (SELECT id FROM projects LIMIT 1)
WHERE project_id IS NULL;

-- Now make project_id NOT NULL
-- SQLite doesn't support ALTER COLUMN, so we need to recreate the table

-- Create new table with NOT NULL constraint
CREATE TABLE chats_new (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    project_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    model TEXT DEFAULT 'claude-3-5-sonnet',
    temperature REAL DEFAULT 0.7,
    max_tokens INTEGER,
    auto_approve BOOLEAN DEFAULT FALSE,
    is_archived BOOLEAN NOT NULL DEFAULT FALSE,
    branched_from_chat_id TEXT,
    branched_at_ordinal BIGINT,
    agent TEXT,
    workflow_id TEXT,
    run_id TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_active TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    FOREIGN KEY (branched_from_chat_id) REFERENCES chats(id) ON DELETE SET NULL
);

-- Copy data from old table
INSERT INTO chats_new (
    id, title, project_id, user_id, model, temperature, max_tokens,
    auto_approve, is_archived, branched_from_chat_id, branched_at_ordinal,
    agent, workflow_id, run_id, created_at, updated_at, last_active
)
SELECT
    id, title, project_id, user_id, model, temperature, max_tokens,
    auto_approve, is_archived, branched_from_chat_id, branched_at_ordinal,
    agent, workflow_id, run_id, created_at, updated_at, last_active
FROM chats;

-- Drop old table
DROP TABLE chats;

-- Rename new table
ALTER TABLE chats_new RENAME TO chats;

-- Recreate indexes
CREATE INDEX idx_chats_project ON chats(project_id);
CREATE INDEX idx_chats_user_id ON chats(user_id);
CREATE INDEX idx_chats_last_active ON chats(last_active DESC);
CREATE INDEX idx_chats_is_archived ON chats(is_archived);
CREATE INDEX idx_chats_branched_from ON chats(branched_from_chat_id);
CREATE INDEX idx_chats_workflow ON chats(workflow_id);

-- +goose Down
-- This is a destructive migration - no safe way to roll back making project_id nullable again
-- Would need to handle the constraint removal carefully in production
