-- +goose Up
-- Add explicit threads table for thread registry
-- This replaces implicit thread tracking via workflows.thread

CREATE TABLE threads (
    id TEXT PRIMARY KEY,                    -- Thread UUID (matches workflow.thread value)
    chat_id TEXT NOT NULL,
    parent_thread_id TEXT,                  -- NULL for root thread
    
    -- Ownership
    workflow_id TEXT NOT NULL,              -- Workflow that owns this thread
    node_id TEXT,                           -- Node that created it (NULL for root)
    thread_key TEXT NOT NULL DEFAULT 'static', -- 'static', 'iteration', or custom key
    thread_mode TEXT NOT NULL DEFAULT 'own',   -- 'own', 'inherit', 'fork'
    
    -- Status (synced from workflow, cached for fast queries)
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN (
        'active',       -- Workflow is running
        'paused',       -- Workflow explicitly paused (waiting for input)
        'completed',    -- Workflow finished successfully
        'failed',       -- Workflow failed
        'cancelled'     -- Workflow was cancelled
    )),
    
    -- For fork context inheritance
    forked_from_thread_id TEXT,
    forked_at_ordinal INTEGER,
    
    -- Display metadata
    display_name TEXT,                      -- e.g., "Advocate", "Critic", "Implementation #1"
    
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    
    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE,
    FOREIGN KEY (workflow_id) REFERENCES workflows(id) ON DELETE CASCADE,
    FOREIGN KEY (parent_thread_id) REFERENCES threads(id) ON DELETE SET NULL,
    FOREIGN KEY (forked_from_thread_id) REFERENCES threads(id) ON DELETE SET NULL
);

CREATE INDEX idx_threads_chat ON threads(chat_id);
CREATE INDEX idx_threads_workflow ON threads(workflow_id);
CREATE INDEX idx_threads_parent ON threads(parent_thread_id);
CREATE INDEX idx_threads_status ON threads(chat_id, status);
CREATE INDEX idx_threads_forked_from ON threads(forked_from_thread_id) WHERE forked_from_thread_id IS NOT NULL;

-- Add thread update types to chat_updates
-- Note: SQLite doesn't support ALTER CHECK, so we recreate with new constraint
-- For now, we'll just insert with new types - the CHECK was already flexible

-- Add workflow/thread update types to user_updates  
-- These are used for sidebar activity indicators

-- +goose Down
DROP INDEX IF EXISTS idx_threads_forked_from;
DROP INDEX IF EXISTS idx_threads_status;
DROP INDEX IF EXISTS idx_threads_parent;
DROP INDEX IF EXISTS idx_threads_workflow;
DROP INDEX IF EXISTS idx_threads_chat;
DROP TABLE IF EXISTS threads;
