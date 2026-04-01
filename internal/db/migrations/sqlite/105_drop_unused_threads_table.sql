-- +goose Up
-- Drop the unused threads table
-- The threads table was added in 099 but never populated or used.
-- Thread tracking is done via workflows.thread column instead.
-- Removing to eliminate dead code and simplify the schema.

DROP INDEX IF EXISTS idx_threads_forked_from;
DROP INDEX IF EXISTS idx_threads_status;
DROP INDEX IF EXISTS idx_threads_parent;
DROP INDEX IF EXISTS idx_threads_workflow;
DROP INDEX IF EXISTS idx_threads_chat;
DROP TABLE IF EXISTS threads;

-- +goose Down
-- Recreate threads table if needed (from migration 099)
CREATE TABLE threads (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    parent_thread_id TEXT,
    workflow_id TEXT NOT NULL,
    node_id TEXT,
    thread_key TEXT NOT NULL DEFAULT 'static',
    thread_mode TEXT NOT NULL DEFAULT 'own',
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN (
        'active', 'paused', 'completed', 'failed', 'cancelled'
    )),
    forked_from_thread_id TEXT,
    forked_at_ordinal INTEGER,
    display_name TEXT,
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
