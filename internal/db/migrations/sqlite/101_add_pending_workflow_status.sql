-- +goose Up
-- Add 'pending' status to workflows table for branched chats that haven't started execution yet
-- This replaces the hacky use of 'completed' status for placeholder workflows

-- SQLite doesn't support ALTER TABLE to modify CHECK constraints, so we recreate the table
CREATE TABLE workflows_new (
    id TEXT PRIMARY KEY,
    parent_id TEXT,
    chat_id TEXT NOT NULL,
    workflow_name TEXT NOT NULL,
    thread TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'running' CHECK (status IN (
        'pending',    -- Created but not started (branched chat awaiting first message)
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

-- Copy existing data
INSERT INTO workflows_new (id, parent_id, chat_id, workflow_name, thread, status, spawned_by_node_id, loop_iteration, forked_from_chat_id, forked_from_thread, forked_at_ordinal, created_at, completed_at)
SELECT id, parent_id, chat_id, workflow_name, thread, status, spawned_by_node_id, loop_iteration, forked_from_chat_id, forked_from_thread, forked_at_ordinal, created_at, completed_at
FROM workflows;

-- Drop old table and rename new one
DROP TABLE workflows;
ALTER TABLE workflows_new RENAME TO workflows;

-- Recreate indexes
CREATE INDEX idx_workflows_parent ON workflows(parent_id);
CREATE INDEX idx_workflows_chat ON workflows(chat_id);
CREATE INDEX idx_workflows_status ON workflows(status);
CREATE INDEX idx_workflows_thread ON workflows(chat_id, thread);
CREATE INDEX idx_workflows_forked_from ON workflows(forked_from_thread) WHERE forked_from_thread IS NOT NULL;
CREATE INDEX idx_workflows_spawned_by_node ON workflows(parent_id, spawned_by_node_id) WHERE spawned_by_node_id IS NOT NULL;

-- +goose Down
-- Remove 'pending' status (revert to original constraint)
-- Note: This will fail if any workflows have status='pending'

CREATE TABLE workflows_old (
    id TEXT PRIMARY KEY,
    parent_id TEXT,
    chat_id TEXT NOT NULL,
    workflow_name TEXT NOT NULL,
    thread TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'running' CHECK (status IN (
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

-- This will fail if there are pending workflows - intentional
INSERT INTO workflows_old (id, parent_id, chat_id, workflow_name, thread, status, spawned_by_node_id, loop_iteration, forked_from_chat_id, forked_from_thread, forked_at_ordinal, created_at, completed_at)
SELECT id, parent_id, chat_id, workflow_name, thread, status, spawned_by_node_id, loop_iteration, forked_from_chat_id, forked_from_thread, forked_at_ordinal, created_at, completed_at
FROM workflows
WHERE status != 'pending';

DROP TABLE workflows;
ALTER TABLE workflows_old RENAME TO workflows;

CREATE INDEX idx_workflows_parent ON workflows(parent_id);
CREATE INDEX idx_workflows_chat ON workflows(chat_id);
CREATE INDEX idx_workflows_status ON workflows(status);
CREATE INDEX idx_workflows_thread ON workflows(chat_id, thread);
CREATE INDEX idx_workflows_forked_from ON workflows(forked_from_thread) WHERE forked_from_thread IS NOT NULL;
CREATE INDEX idx_workflows_spawned_by_node ON workflows(parent_id, spawned_by_node_id) WHERE spawned_by_node_id IS NOT NULL;
