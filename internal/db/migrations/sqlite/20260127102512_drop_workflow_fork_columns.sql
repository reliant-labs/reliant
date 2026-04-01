-- +goose Up
-- +goose StatementBegin

-- Migration: Remove workflow fork columns
-- The fork-based context inheritance is no longer needed.
-- SQLite doesn't support DROP COLUMN, so we recreate the table.

-- Step 1: Drop the fork-related index
DROP INDEX IF EXISTS idx_workflows_forked_from;

-- Step 2: Create new table without fork columns
CREATE TABLE workflows_new (
    id TEXT PRIMARY KEY,
    parent_id TEXT,
    chat_id TEXT NOT NULL,
    workflow_name TEXT NOT NULL,
    thread TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'running' CHECK (status IN (
        'pending',
        'running',
        'completed',
        'failed',
        'cancelled',
        'paused'
    )),
    spawned_by_node_id TEXT,
    loop_iteration INTEGER,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME,
    paused_at DATETIME,
    worker_started_at DATETIME,
    worker_stopped_at DATETIME,

    FOREIGN KEY (parent_id) REFERENCES workflows_new(id) ON DELETE SET NULL,
    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE
);

-- Step 3: Copy data from old table (excluding fork columns)
INSERT INTO workflows_new (
    id, parent_id, chat_id, workflow_name, thread, status,
    spawned_by_node_id, loop_iteration,
    created_at, completed_at, paused_at,
    worker_started_at, worker_stopped_at
)
SELECT 
    id, parent_id, chat_id, workflow_name, thread, status,
    spawned_by_node_id, loop_iteration,
    created_at, completed_at, paused_at,
    worker_started_at, worker_stopped_at
FROM workflows;

-- Step 4: Drop old table
DROP TABLE workflows;

-- Step 5: Rename new table
ALTER TABLE workflows_new RENAME TO workflows;

-- Step 6: Recreate indexes (except idx_workflows_forked_from)
CREATE INDEX idx_workflows_parent ON workflows(parent_id);
CREATE INDEX idx_workflows_chat ON workflows(chat_id);
CREATE INDEX idx_workflows_status ON workflows(status);
CREATE INDEX idx_workflows_thread ON workflows(chat_id, thread);
CREATE INDEX idx_workflows_spawned_by_node ON workflows(parent_id, spawned_by_node_id) WHERE spawned_by_node_id IS NOT NULL;
CREATE INDEX idx_workflows_loop_iteration ON workflows(parent_id, spawned_by_node_id, loop_iteration) WHERE loop_iteration IS NOT NULL;
CREATE INDEX idx_workflows_paused ON workflows(status) WHERE status = 'paused';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Add back the fork columns
ALTER TABLE workflows ADD COLUMN forked_from_chat_id TEXT;
ALTER TABLE workflows ADD COLUMN forked_from_thread TEXT;
ALTER TABLE workflows ADD COLUMN forked_at_ordinal INTEGER;
ALTER TABLE workflows ADD COLUMN forked_at_context_seq INTEGER;

-- Recreate the fork index
CREATE INDEX idx_workflows_forked_from ON workflows(forked_from_thread) WHERE forked_from_thread IS NOT NULL;

-- +goose StatementEnd
