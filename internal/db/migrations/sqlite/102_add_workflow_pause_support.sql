-- +goose Up
-- Add pause support to workflows
-- This enables indefinite pause/resume via signal-based pause pattern

-- Add 'paused' status to workflows table
-- We use ALTER TABLE instead of recreating because SQLite doesn't support
-- adding to CHECK constraints directly, so we need to recreate the table

-- Step 1: Create new table with updated status constraint
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
        'paused'     -- NEW: Paused state for indefinite pause
    )),
    spawned_by_node_id TEXT,
    loop_iteration INTEGER,
    forked_from_chat_id TEXT,
    forked_from_thread TEXT,
    forked_at_ordinal INTEGER,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME,
    paused_at DATETIME,                         -- NEW: When workflow was paused

    FOREIGN KEY (parent_id) REFERENCES workflows(id) ON DELETE SET NULL,
    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE
);

-- Step 2: Copy data from old table
INSERT INTO workflows_new (
    id, parent_id, chat_id, workflow_name, thread, status,
    spawned_by_node_id, loop_iteration,
    forked_from_chat_id, forked_from_thread, forked_at_ordinal,
    created_at, completed_at
)
SELECT
    id, parent_id, chat_id, workflow_name, thread, status,
    spawned_by_node_id, loop_iteration,
    forked_from_chat_id, forked_from_thread, forked_at_ordinal,
    created_at, completed_at
FROM workflows;

-- Step 3: Drop old table
DROP TABLE workflows;

-- Step 4: Rename new table
ALTER TABLE workflows_new RENAME TO workflows;

-- Step 5: Recreate indexes
CREATE INDEX idx_workflows_parent ON workflows(parent_id);
CREATE INDEX idx_workflows_chat ON workflows(chat_id);
CREATE INDEX idx_workflows_status ON workflows(status);
CREATE INDEX idx_workflows_thread ON workflows(chat_id, thread);
CREATE INDEX idx_workflows_forked_from ON workflows(forked_from_thread) WHERE forked_from_thread IS NOT NULL;
CREATE INDEX idx_workflows_spawned_by_node ON workflows(parent_id, spawned_by_node_id) WHERE spawned_by_node_id IS NOT NULL;
CREATE INDEX idx_workflows_loop_iteration ON workflows(parent_id, spawned_by_node_id, loop_iteration) WHERE loop_iteration IS NOT NULL;
CREATE INDEX idx_workflows_paused ON workflows(status) WHERE status = 'paused';  -- NEW: Index for paused workflows

-- +goose Down
-- Revert pause support changes

CREATE TABLE workflows_old (
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
        'cancelled'
    )),
    spawned_by_node_id TEXT,
    loop_iteration INTEGER,
    forked_from_chat_id TEXT,
    forked_from_thread TEXT,
    forked_at_ordinal INTEGER,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME,

    FOREIGN KEY (parent_id) REFERENCES workflows(id) ON DELETE SET NULL,
    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE
);

INSERT INTO workflows_old (
    id, parent_id, chat_id, workflow_name, thread, status,
    spawned_by_node_id, loop_iteration,
    forked_from_chat_id, forked_from_thread, forked_at_ordinal,
    created_at, completed_at
)
SELECT
    id, parent_id, chat_id, workflow_name, thread,
    CASE WHEN status = 'paused' THEN 'cancelled' ELSE status END,  -- Convert paused to cancelled
    spawned_by_node_id, loop_iteration,
    forked_from_chat_id, forked_from_thread, forked_at_ordinal,
    created_at, completed_at
FROM workflows;

DROP TABLE workflows;
ALTER TABLE workflows_old RENAME TO workflows;

-- Recreate indexes
CREATE INDEX idx_workflows_parent ON workflows(parent_id);
CREATE INDEX idx_workflows_chat ON workflows(chat_id);
CREATE INDEX idx_workflows_status ON workflows(status);
CREATE INDEX idx_workflows_thread ON workflows(chat_id, thread);
CREATE INDEX idx_workflows_forked_from ON workflows(forked_from_thread) WHERE forked_from_thread IS NOT NULL;
CREATE INDEX idx_workflows_spawned_by_node ON workflows(parent_id, spawned_by_node_id) WHERE spawned_by_node_id IS NOT NULL;
CREATE INDEX idx_workflows_loop_iteration ON workflows(parent_id, spawned_by_node_id, loop_iteration) WHERE loop_iteration IS NOT NULL;
