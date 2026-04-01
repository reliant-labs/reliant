-- +goose Up
-- Workflows table tracks workflow hierarchy (parent-child relationships)
-- Each workflow execution gets a UUID, with parent_id forming a linked list
CREATE TABLE workflows (
    id TEXT PRIMARY KEY,                    -- Workflow UUID
    parent_id TEXT,                         -- Parent workflow UUID (NULL for root workflows)
    chat_id TEXT NOT NULL,                  -- Associated chat
    workflow_name TEXT NOT NULL,            -- e.g., "builtin://agent", "my-custom-workflow"
    thread TEXT NOT NULL,              -- Thread path for message isolation (equals workflow ID for children)
    status TEXT NOT NULL DEFAULT 'running' CHECK (status IN (
        'running',
        'completed', 
        'failed',
        'cancelled'
    )),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME,
    
    FOREIGN KEY (parent_id) REFERENCES workflows(id) ON DELETE SET NULL,
    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE
);

-- Index for querying children of a parent workflow
CREATE INDEX idx_workflows_parent ON workflows(parent_id);

-- Index for querying all workflows in a chat
CREATE INDEX idx_workflows_chat ON workflows(chat_id);

-- Index for querying by status
CREATE INDEX idx_workflows_status ON workflows(status);

-- Index for querying by thread (message isolation lookup)
CREATE INDEX idx_workflows_thread ON workflows(chat_id, thread);

-- +goose Down
DROP INDEX IF EXISTS idx_workflows_thread;
DROP INDEX IF EXISTS idx_workflows_status;
DROP INDEX IF EXISTS idx_workflows_chat;
DROP INDEX IF EXISTS idx_workflows_parent;
DROP TABLE IF EXISTS workflows;
