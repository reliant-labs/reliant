-- +goose Up
-- Migration: Add thread_id to messages table
--
-- This denormalizes thread_id onto messages for more efficient querying.
-- The thread_id is derived from context_window -> thread relationship.
-- context_window_id is preserved for compaction tracking.

-- Since SQLite doesn't support ALTER COLUMN to add NOT NULL, we recreate the table

-- Step 1: Create new messages table with thread_id column
CREATE TABLE messages_new (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    ordinal BIGINT NOT NULL,
    
    -- Thread (denormalized from context_window for efficient queries)
    thread_id TEXT NOT NULL REFERENCES threads(id),
    
    -- Context window (required for compaction tracking)
    context_window_id TEXT NOT NULL REFERENCES context_windows(id) ON DELETE CASCADE,
    
    -- Execution context
    node_id TEXT,
    node_path TEXT,
    
    -- Message fields
    role TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'system', 'tool')),
    display_style TEXT,
    model TEXT,
    agent TEXT,
    
    -- Workflow tracking
    workflow_id TEXT,
    run_id TEXT,
    
    -- Token tracking
    input_tokens INTEGER,
    output_tokens INTEGER,
    cache_creation_tokens INTEGER,
    cache_read_tokens INTEGER,
    
    -- Metadata
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    activity_id TEXT,
    
    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE
);

-- Step 2: Copy data from old table, deriving thread_id from context_window
INSERT INTO messages_new (
    id, chat_id, ordinal, thread_id, context_window_id, node_id, node_path,
    role, display_style, model, agent, workflow_id, run_id,
    input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
    created_at, updated_at, activity_id
)
SELECT 
    m.id, m.chat_id, m.ordinal,
    cw.thread_id,
    m.context_window_id, m.node_id, m.node_path,
    m.role, m.display_style, m.model, m.agent, m.workflow_id, m.run_id,
    m.input_tokens, m.output_tokens, m.cache_creation_tokens, m.cache_read_tokens,
    m.created_at, m.updated_at, m.activity_id
FROM messages m
JOIN context_windows cw ON cw.id = m.context_window_id;

-- Step 3: Drop old table and its indexes
DROP INDEX IF EXISTS idx_messages_context_window;
DROP INDEX IF EXISTS idx_messages_context_window_ordinal;
DROP INDEX IF EXISTS idx_messages_activity;
DROP INDEX IF EXISTS idx_messages_node;
DROP INDEX IF EXISTS idx_messages_chat;
DROP TABLE messages;

-- Step 4: Rename new table
ALTER TABLE messages_new RENAME TO messages;

-- Step 5: Recreate indexes
CREATE INDEX idx_messages_chat ON messages(chat_id, ordinal);
CREATE INDEX idx_messages_node ON messages(node_id) WHERE node_id IS NOT NULL;
CREATE INDEX idx_messages_activity ON messages(activity_id) WHERE activity_id IS NOT NULL;
CREATE UNIQUE INDEX idx_messages_context_window_ordinal ON messages(context_window_id, ordinal);
CREATE INDEX idx_messages_context_window ON messages(context_window_id);

-- New index for thread-based queries
CREATE INDEX idx_messages_thread ON messages(thread_id, ordinal);

-- +goose Down
-- Remove thread_id column by recreating table

-- Step 1: Create messages table without thread_id
CREATE TABLE messages_old (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    ordinal BIGINT NOT NULL,
    context_window_id TEXT NOT NULL REFERENCES context_windows(id) ON DELETE CASCADE,
    node_id TEXT,
    node_path TEXT,
    role TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'system', 'tool')),
    display_style TEXT,
    model TEXT,
    agent TEXT,
    workflow_id TEXT,
    run_id TEXT,
    input_tokens INTEGER,
    output_tokens INTEGER,
    cache_creation_tokens INTEGER,
    cache_read_tokens INTEGER,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    activity_id TEXT,
    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE
);

-- Step 2: Copy data (excluding thread_id)
INSERT INTO messages_old (
    id, chat_id, ordinal, context_window_id, node_id, node_path,
    role, display_style, model, agent, workflow_id, run_id,
    input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
    created_at, updated_at, activity_id
)
SELECT 
    id, chat_id, ordinal, context_window_id, node_id, node_path,
    role, display_style, model, agent, workflow_id, run_id,
    input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
    created_at, updated_at, activity_id
FROM messages;

-- Step 3: Drop new table
DROP INDEX IF EXISTS idx_messages_thread;
DROP INDEX IF EXISTS idx_messages_context_window;
DROP INDEX IF EXISTS idx_messages_context_window_ordinal;
DROP INDEX IF EXISTS idx_messages_activity;
DROP INDEX IF EXISTS idx_messages_node;
DROP INDEX IF EXISTS idx_messages_chat;
DROP TABLE messages;

-- Step 4: Rename old table
ALTER TABLE messages_old RENAME TO messages;

-- Step 5: Recreate original indexes
CREATE INDEX idx_messages_chat ON messages(chat_id, ordinal);
CREATE INDEX idx_messages_node ON messages(node_id) WHERE node_id IS NOT NULL;
CREATE INDEX idx_messages_activity ON messages(activity_id) WHERE activity_id IS NOT NULL;
CREATE UNIQUE INDEX idx_messages_context_window_ordinal ON messages(context_window_id, ordinal);
CREATE INDEX idx_messages_context_window ON messages(context_window_id);
