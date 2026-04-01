-- +goose Up
-- Migration: Drop old thread and context_sequence columns from messages table
--
-- Now that all messages are properly linked to context_windows via context_window_id,
-- we can remove the legacy columns:
-- - thread: Replaced by context_window -> thread relationship
-- - context_sequence: Replaced by context_window.sequence
--
-- We also make context_window_id NOT NULL since all messages must belong to a context window.

-- Since SQLite doesn't support DROP COLUMN directly, we recreate the table

-- Step 1: Create new messages table without thread/context_sequence columns
CREATE TABLE messages_new (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    ordinal BIGINT NOT NULL,
    
    -- Context window (required - replaces thread + context_sequence)
    context_window_id TEXT NOT NULL REFERENCES context_windows(id) ON DELETE CASCADE,
    
    -- Execution context
    node_id TEXT,
    node_path TEXT,
    
    -- Message fields
    role TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'system', 'tool')),
    display_style TEXT, -- UI styling hint: 'info', 'warning', 'success', 'hidden', or NULL
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

-- Step 2: Copy data from old table (excluding thread and context_sequence)
-- Only copy messages that have context_window_id set
INSERT INTO messages_new (
    id, chat_id, ordinal, context_window_id, node_id, node_path,
    role, display_style, model, agent, workflow_id, run_id,
    input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
    created_at, updated_at, activity_id
)
SELECT 
    id, chat_id, ordinal, context_window_id, node_id, node_path,
    -- Convert old info/warning roles to proper role + display_style
    CASE 
        WHEN role IN ('info', 'warning') THEN 'system'
        ELSE role 
    END as role,
    CASE 
        WHEN role = 'info' THEN 'info'
        WHEN role = 'warning' THEN 'warning'
        ELSE display_style 
    END as display_style,
    model, agent, workflow_id, run_id,
    input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
    created_at, updated_at, activity_id
FROM messages
WHERE context_window_id IS NOT NULL;

-- Step 3: Drop old table and its indexes
DROP INDEX IF EXISTS idx_messages_context_window;
DROP INDEX IF EXISTS idx_messages_activity;
DROP INDEX IF EXISTS idx_messages_node;
DROP INDEX IF EXISTS idx_messages_thread;
DROP INDEX IF EXISTS idx_messages_unique;
DROP INDEX IF EXISTS idx_messages_chat;
DROP TABLE messages;

-- Step 4: Rename new table
ALTER TABLE messages_new RENAME TO messages;

-- Step 5: Recreate indexes (without thread-based indexes)
CREATE INDEX idx_messages_chat ON messages(chat_id, ordinal);
CREATE INDEX idx_messages_node ON messages(node_id) WHERE node_id IS NOT NULL;
CREATE INDEX idx_messages_activity ON messages(activity_id) WHERE activity_id IS NOT NULL;

-- Messages are now uniquely identified by context_window_id + ordinal
-- (ordinals are per-thread, and context_window is per-thread)
CREATE UNIQUE INDEX idx_messages_context_window_ordinal ON messages(context_window_id, ordinal);

-- Index for looking up messages by context window
CREATE INDEX idx_messages_context_window ON messages(context_window_id);

-- +goose Down
-- Restore thread and context_sequence columns

-- Step 1: Create messages table with old columns restored
CREATE TABLE messages_old (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    ordinal BIGINT NOT NULL,
    thread TEXT NOT NULL DEFAULT '0',
    context_sequence INTEGER NOT NULL DEFAULT 0,
    context_window_id TEXT REFERENCES context_windows(id) ON DELETE CASCADE,
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

-- Step 2: Copy data and derive thread/context_sequence from context_window
INSERT INTO messages_old (
    id, chat_id, ordinal, thread, context_sequence, context_window_id, node_id, node_path,
    role, display_style, model, agent, workflow_id, run_id,
    input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
    created_at, updated_at, activity_id
)
SELECT 
    m.id, m.chat_id, m.ordinal, 
    cw.thread_id AS thread,
    cw.sequence AS context_sequence,
    m.context_window_id, m.node_id, m.node_path,
    m.role, m.display_style, m.model, m.agent, m.workflow_id, m.run_id,
    m.input_tokens, m.output_tokens, m.cache_creation_tokens, m.cache_read_tokens,
    m.created_at, m.updated_at, m.activity_id
FROM messages m
JOIN context_windows cw ON cw.id = m.context_window_id;

-- Step 3: Drop new table
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
CREATE UNIQUE INDEX idx_messages_unique ON messages(chat_id, thread, ordinal);
CREATE INDEX idx_messages_thread ON messages(chat_id, thread);
CREATE INDEX idx_messages_node ON messages(node_id);
CREATE INDEX idx_messages_activity ON messages(activity_id) WHERE activity_id IS NOT NULL;
CREATE INDEX idx_messages_context_window ON messages(context_window_id, ordinal) WHERE context_window_id IS NOT NULL;
