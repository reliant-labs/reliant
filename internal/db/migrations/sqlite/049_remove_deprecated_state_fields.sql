-- +goose Up
-- Migration: Remove deprecated state tracking (streaming_state, active_threads)
-- Purpose: Complete migration to computed state and workflow hierarchy

-- Drop active_threads table entirely
DROP TABLE IF EXISTS active_threads;

-- Drop and recreate messages table without streaming_state
DROP TABLE IF EXISTS messages;
CREATE TABLE messages (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    ordinal BIGINT NOT NULL,

    -- Execution context
    thread TEXT NOT NULL DEFAULT '0',
    context_sequence INTEGER NOT NULL DEFAULT 0,
    node_id TEXT,
    node_path TEXT,

    -- Message fields
    role TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'system', 'tool')),
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

    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE
);

CREATE INDEX idx_messages_chat ON messages(chat_id, ordinal);
CREATE UNIQUE INDEX idx_messages_unique ON messages(chat_id, thread, ordinal);
CREATE INDEX idx_messages_thread ON messages(chat_id, thread);
CREATE INDEX idx_messages_node ON messages(node_id);

-- Drop and recreate message_content_blocks table without streaming_state
DROP TABLE IF EXISTS message_content_blocks;
CREATE TABLE message_content_blocks (
    id TEXT PRIMARY KEY,
    message_id TEXT NOT NULL,
    position INTEGER NOT NULL DEFAULT 0,

    -- Block type and content
    block_type TEXT NOT NULL CHECK (block_type IN ('text', 'tool_call', 'tool_result')),
    content TEXT,

    -- Tool-specific fields
    tool_name TEXT,
    tool_input TEXT,
    tool_call_id TEXT,
    is_error BOOLEAN,

    -- Versioning
    version INTEGER,

    -- Workflow context
    node_id TEXT,
    node_path TEXT,

    -- Activity tracking (for idempotency)
    activity_id TEXT,
    workflow_run_id TEXT,
    attempt_number INTEGER DEFAULT 1,

    -- Metadata
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,

    FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE CASCADE
);

CREATE INDEX idx_content_blocks_message ON message_content_blocks(message_id, created_at);
CREATE INDEX idx_content_blocks_tool_call ON message_content_blocks(tool_call_id)
    WHERE tool_call_id IS NOT NULL;
CREATE INDEX idx_content_blocks_activity ON message_content_blocks(activity_id, attempt_number)
    WHERE activity_id IS NOT NULL;

-- Recreate triggers for chat_updates notifications
-- (These were lost when we dropped the messages table)

-- Trigger: Notify when a message is inserted
DROP TRIGGER IF EXISTS chat_updates_message_insert;

-- +goose StatementBegin
CREATE TRIGGER chat_updates_message_insert
AFTER INSERT ON messages
BEGIN
    INSERT INTO chat_updates (
        id, chat_id, sequence_number, update_type, entity_id, data, created_at
    )
    VALUES (
        lower(hex(randomblob(16))),
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'message',
        NEW.id,
        json_object(
            'id', NEW.id,
            'chat_id', NEW.chat_id,
            'ordinal', NEW.ordinal,
            'thread', NEW.thread,
            'role', NEW.role,
            'model', NEW.model,
            'agent', NEW.agent,
            'workflow_id', NEW.workflow_id,
            'run_id', NEW.run_id,
            'node_id', NEW.node_id,
            'node_path', NEW.node_path,
            'created_at', NEW.created_at,
            'updated_at', NEW.updated_at
        ),
        CURRENT_TIMESTAMP
    );
END;
-- +goose StatementEnd

-- Trigger: Notify when a message is updated
DROP TRIGGER IF EXISTS chat_updates_message_update;

-- +goose StatementBegin
CREATE TRIGGER chat_updates_message_update
AFTER UPDATE ON messages
BEGIN
    INSERT INTO chat_updates (
        id, chat_id, sequence_number, update_type, entity_id, data, created_at
    )
    VALUES (
        lower(hex(randomblob(16))),
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'message',
        NEW.id,
        json_object(
            'id', NEW.id,
            'chat_id', NEW.chat_id,
            'ordinal', NEW.ordinal,
            'thread', NEW.thread,
            'role', NEW.role,
            'model', NEW.model,
            'agent', NEW.agent,
            'workflow_id', NEW.workflow_id,
            'run_id', NEW.run_id,
            'node_id', NEW.node_id,
            'node_path', NEW.node_path,
            'created_at', NEW.created_at,
            'updated_at', NEW.updated_at
        ),
        CURRENT_TIMESTAMP
    );
END;
-- +goose StatementEnd

-- Trigger: Notify when a content block is inserted (all types)
DROP TRIGGER IF EXISTS chat_updates_content_block_insert;

-- +goose StatementBegin
CREATE TRIGGER chat_updates_content_block_insert
AFTER INSERT ON message_content_blocks
BEGIN
    INSERT INTO chat_updates (
        id, chat_id, sequence_number, update_type, entity_id, data, created_at
    )
    SELECT
        lower(hex(randomblob(16))),
        m.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = m.chat_id), 0) + 1,
        'content_block',
        NEW.id,
        json_object(
            'id', NEW.id,
            'message_id', NEW.message_id,
            'position', NEW.position,
            'block_type', NEW.block_type,
            'content', NEW.content,
            'tool_name', NEW.tool_name,
            'tool_input', NEW.tool_input,
            'tool_call_id', NEW.tool_call_id,
            'is_error', NEW.is_error,
            'node_id', NEW.node_id,
            'node_path', NEW.node_path,
            'created_at', NEW.created_at
        ),
        CURRENT_TIMESTAMP
    FROM messages m
    WHERE m.id = NEW.message_id;
END;
-- +goose StatementEnd

-- Trigger: Notify when a content block is updated
DROP TRIGGER IF EXISTS chat_updates_content_block_update;

-- +goose StatementBegin
CREATE TRIGGER chat_updates_content_block_update
AFTER UPDATE ON message_content_blocks
BEGIN
    INSERT INTO chat_updates (
        id, chat_id, sequence_number, update_type, entity_id, data, created_at
    )
    SELECT
        lower(hex(randomblob(16))),
        m.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = m.chat_id), 0) + 1,
        'content_block',
        NEW.id,
        json_object(
            'id', NEW.id,
            'message_id', NEW.message_id,
            'position', NEW.position,
            'block_type', NEW.block_type,
            'content', NEW.content,
            'tool_name', NEW.tool_name,
            'tool_input', NEW.tool_input,
            'tool_call_id', NEW.tool_call_id,
            'is_error', NEW.is_error,
            'node_id', NEW.node_id,
            'node_path', NEW.node_path,
            'created_at', NEW.created_at,
            'updated_at', NEW.updated_at
        ),
        CURRENT_TIMESTAMP
    FROM messages m
    WHERE m.id = NEW.message_id;
END;
-- +goose StatementEnd

-- +goose Down
-- Rollback not supported - this is a destructive migration that drops data
SELECT 'Migration 049 cannot be rolled back - messages and content blocks were dropped';
