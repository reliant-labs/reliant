-- Migration: Add worker_started_at and worker_stopped_at columns to workflows table
-- Purpose: Track worker lifecycle for race condition fix between pause/resume and reconciler
--
-- Problem: When a workflow is paused, activities sit in Temporal's "Scheduled" state.
-- When resumed, the reconciler may run before the worker picks up the queued activities,
-- seeing them as "stuck" (scheduled >30s) and incorrectly terminating the workflow.
--
-- Solution: Track when the worker was started so the reconciler can skip the stuck check
-- for workflows whose workers were recently started.

-- +goose Up
ALTER TABLE workflows ADD COLUMN worker_started_at DATETIME;
ALTER TABLE workflows ADD COLUMN worker_stopped_at DATETIME;

-- +goose Down
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
    forked_from_chat_id TEXT,
    forked_from_thread TEXT,
    forked_at_ordinal INTEGER,
    created_at DATETIME NOT NULL,
    completed_at DATETIME,
    paused_at DATETIME,
    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE,
    FOREIGN KEY (parent_id) REFERENCES workflows(id) ON DELETE SET NULL
);

INSERT INTO workflows_new (
    id, parent_id, chat_id, workflow_name, thread, status,
    spawned_by_node_id, loop_iteration,
    forked_from_chat_id, forked_from_thread, forked_at_ordinal,
    created_at, completed_at, paused_at
)
SELECT 
    id, parent_id, chat_id, workflow_name, thread, status,
    spawned_by_node_id, loop_iteration,
    forked_from_chat_id, forked_from_thread, forked_at_ordinal,
    created_at, completed_at, paused_at
FROM workflows;

DROP TABLE workflows;
ALTER TABLE workflows_new RENAME TO workflows;

-- Recreate indexes
CREATE INDEX IF NOT EXISTS idx_workflows_chat_id ON workflows(chat_id);
CREATE INDEX IF NOT EXISTS idx_workflows_parent_id ON workflows(parent_id);
CREATE INDEX IF NOT EXISTS idx_workflows_status ON workflows(status);
