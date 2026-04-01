-- Migration: Add expired_at column to workflows table
-- Purpose: Track when a paused workflow expired (Temporal execution no longer exists).
-- The reconciler sets this when it detects a paused workflow whose Temporal execution
-- has completed/terminated/timed out.

-- +goose Up
ALTER TABLE workflows ADD COLUMN expired_at DATETIME;

-- +goose Down
-- SQLite doesn't support DROP COLUMN directly in older versions,
-- but goose will handle this via table recreation if needed.
-- For simplicity, we create a new table without the column.
CREATE TABLE workflows_temp (
    id TEXT PRIMARY KEY NOT NULL,
    parent_id TEXT,
    chat_id TEXT NOT NULL,
    workflow_name TEXT NOT NULL,
    thread TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    spawned_by_node_id TEXT,
    loop_iteration INTEGER,
    created_at DATETIME NOT NULL,
    completed_at DATETIME,
    paused_at DATETIME,
    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE,
    FOREIGN KEY (parent_id) REFERENCES workflows(id) ON DELETE SET NULL
);

INSERT INTO workflows_temp SELECT
    id, parent_id, chat_id, workflow_name, thread, status,
    spawned_by_node_id, loop_iteration,
    created_at, completed_at, paused_at
FROM workflows;

DROP TABLE workflows;
ALTER TABLE workflows_temp RENAME TO workflows;

CREATE INDEX IF NOT EXISTS idx_workflows_chat_id ON workflows(chat_id);
CREATE INDEX IF NOT EXISTS idx_workflows_parent_id ON workflows(parent_id);
CREATE INDEX IF NOT EXISTS idx_workflows_status ON workflows(status);