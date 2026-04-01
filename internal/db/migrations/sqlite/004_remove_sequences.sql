-- +goose Up
-- Remove sequence tracking and switch to timestamp-based synchronization

-- Drop sequence-related indexes
DROP INDEX IF EXISTS idx_content_blocks_message_sequence;
DROP INDEX IF EXISTS idx_content_blocks_sequence;
DROP INDEX IF EXISTS idx_messages_chat_sequence;
DROP INDEX IF EXISTS idx_messages_sequence;

-- Create new table for messages without sequence column
CREATE TABLE messages_new (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    ordinal BIGINT NOT NULL,
    thread TEXT NOT NULL DEFAULT '0',
    context_sequence INTEGER NOT NULL DEFAULT 0,
    role TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'system', 'tool')),
    model TEXT,
    agent TEXT,
    streaming_state TEXT NOT NULL DEFAULT 'complete' CHECK (streaming_state IN ('streaming', 'complete', 'failed')),
    streaming_completed_at TIMESTAMP,
    input_tokens INTEGER DEFAULT 0,
    output_tokens INTEGER DEFAULT 0,
    cache_creation_tokens INTEGER DEFAULT 0,
    cache_read_tokens INTEGER DEFAULT 0,
    workflow_id TEXT,
    run_id TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE,
    UNIQUE(chat_id, ordinal)
);

-- Copy data from old table
INSERT INTO messages_new SELECT
    id, chat_id, ordinal, thread, context_sequence,
    role, model, agent, streaming_state, streaming_completed_at,
    input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
    workflow_id, run_id, created_at, updated_at
FROM messages;

-- Drop old table and rename new one
DROP TABLE messages;
ALTER TABLE messages_new RENAME TO messages;

-- Recreate indexes
CREATE INDEX IF NOT EXISTS idx_messages_chat_ordinal ON messages(chat_id, ordinal);
CREATE INDEX IF NOT EXISTS idx_messages_thread ON messages(chat_id, thread, ordinal);
CREATE INDEX IF NOT EXISTS idx_messages_context ON messages(chat_id, thread, context_sequence, ordinal);
CREATE INDEX IF NOT EXISTS idx_messages_streaming ON messages(streaming_state, created_at)
    WHERE streaming_state = 'streaming';
CREATE INDEX IF NOT EXISTS idx_messages_updated_at ON messages(updated_at);
CREATE INDEX IF NOT EXISTS idx_messages_chat_updated ON messages(chat_id, updated_at);

-- Create new table for message_content_blocks without sequence column
CREATE TABLE message_content_blocks_new (
    id TEXT PRIMARY KEY,
    message_id TEXT NOT NULL,
    position INTEGER NOT NULL,
    block_type TEXT NOT NULL CHECK (block_type IN (
        'text',
        'thinking',
        'tool_call',
        'tool_result',
        'image',
        'file'
    )),
    content TEXT,
    tool_name TEXT,
    tool_input TEXT,
    tool_call_id TEXT,
    is_error BOOLEAN DEFAULT FALSE,
    streaming_state TEXT NOT NULL DEFAULT 'complete' CHECK (streaming_state IN ('streaming', 'complete', 'failed')),
    streaming_started_at TIMESTAMP,
    streaming_completed_at TIMESTAMP,
    version INTEGER DEFAULT 1,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE CASCADE,
    UNIQUE(message_id, position)
);

-- Copy data from old table
INSERT INTO message_content_blocks_new SELECT
    id, message_id, position, block_type, content,
    tool_name, tool_input, tool_call_id, is_error,
    streaming_state, streaming_started_at, streaming_completed_at,
    version, created_at, updated_at
FROM message_content_blocks;

-- Drop old table and rename new one
DROP TABLE message_content_blocks;
ALTER TABLE message_content_blocks_new RENAME TO message_content_blocks;

-- Recreate indexes
CREATE INDEX IF NOT EXISTS idx_content_blocks_message ON message_content_blocks(message_id, position);
CREATE INDEX IF NOT EXISTS idx_content_blocks_tool_call_id ON message_content_blocks(tool_call_id)
    WHERE tool_call_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_content_blocks_streaming ON message_content_blocks(streaming_state, streaming_started_at)
    WHERE streaming_state = 'streaming';
CREATE INDEX IF NOT EXISTS idx_content_blocks_updated_at ON message_content_blocks(updated_at);
CREATE INDEX IF NOT EXISTS idx_content_blocks_message_updated ON message_content_blocks(message_id, updated_at);

-- Create new table for chats without current_sequence column
CREATE TABLE chats_new (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    project_id TEXT,
    model TEXT DEFAULT 'claude-3-5-sonnet',
    temperature REAL DEFAULT 0.7,
    max_tokens INTEGER,
    auto_approve BOOLEAN DEFAULT FALSE,
    is_archived BOOLEAN NOT NULL DEFAULT FALSE,
    branched_from_chat_id TEXT,
    branched_at_ordinal BIGINT,
    workflow_id TEXT,
    run_id TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_active TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    FOREIGN KEY (branched_from_chat_id) REFERENCES chats(id) ON DELETE SET NULL
);

-- Copy data from old table
INSERT INTO chats_new SELECT
    id, title, project_id, model, temperature, max_tokens,
    auto_approve, is_archived, branched_from_chat_id, branched_at_ordinal,
    workflow_id, run_id, created_at, updated_at, last_active
FROM chats;

-- Drop old table and rename new one
DROP TABLE chats;
ALTER TABLE chats_new RENAME TO chats;

-- Recreate indexes
CREATE INDEX IF NOT EXISTS idx_chats_project ON chats(project_id);
CREATE INDEX IF NOT EXISTS idx_chats_last_active ON chats(last_active DESC);
CREATE INDEX IF NOT EXISTS idx_chats_is_archived ON chats(is_archived);
CREATE INDEX IF NOT EXISTS idx_chats_branched_from ON chats(branched_from_chat_id);
CREATE INDEX IF NOT EXISTS idx_chats_workflow ON chats(workflow_id);
CREATE INDEX IF NOT EXISTS idx_chats_updated_at ON chats(updated_at);

-- Create new table for tool_approvals without sequence column
CREATE TABLE tool_approvals_new (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    content_block_id TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL CHECK (status IN ('pending', 'approved', 'denied')),
    denial_reason TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    responded_at TIMESTAMP,
    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE,
    FOREIGN KEY (content_block_id) REFERENCES message_content_blocks(id) ON DELETE CASCADE
);

-- Copy data from old table
INSERT INTO tool_approvals_new SELECT
    id, chat_id, content_block_id, status, denial_reason,
    created_at, CURRENT_TIMESTAMP, responded_at
FROM tool_approvals;

-- Drop old table and rename new one
DROP TABLE tool_approvals;
ALTER TABLE tool_approvals_new RENAME TO tool_approvals;

-- Recreate indexes
CREATE INDEX IF NOT EXISTS idx_tool_approvals_chat_status ON tool_approvals(chat_id, status);
CREATE INDEX IF NOT EXISTS idx_tool_approvals_content_block ON tool_approvals(content_block_id);
CREATE INDEX IF NOT EXISTS idx_tool_approvals_updated_at ON tool_approvals(updated_at);
CREATE INDEX IF NOT EXISTS idx_tool_approvals_chat_updated ON tool_approvals(chat_id, updated_at);

-- +goose Down
-- This migration cannot be reversed - sequences have been removed
-- To roll back, restore from backup or recreate with migration 002
