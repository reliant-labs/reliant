-- Migration: Drop worker_started_at and worker_stopped_at columns from workflows table
-- Purpose: Worker lifecycle tracking is no longer needed with shared worker pool.
-- Pause/resume is now handled via signals, not by stopping/starting per-workflow workers.

-- +goose Up
-- SQLite doesn't support DROP COLUMN directly, so we recreate the table
CREATE TABLE workflows_new (
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

INSERT INTO workflows_new (
    id, parent_id, chat_id, workflow_name, thread, status,
    spawned_by_node_id, loop_iteration,
    created_at, completed_at, paused_at
)
SELECT 
    id, parent_id, chat_id, workflow_name, thread, status,
    spawned_by_node_id, loop_iteration,
    created_at, completed_at, paused_at
FROM workflows;

DROP TABLE workflows;
ALTER TABLE workflows_new RENAME TO workflows;

-- Recreate indexes
CREATE INDEX IF NOT EXISTS idx_workflows_chat_id ON workflows(chat_id);
CREATE INDEX IF NOT EXISTS idx_workflows_parent_id ON workflows(parent_id);
CREATE INDEX IF NOT EXISTS idx_workflows_status ON workflows(status);

-- +goose Down
ALTER TABLE workflows ADD COLUMN worker_started_at DATETIME;
ALTER TABLE workflows ADD COLUMN worker_stopped_at DATETIME;