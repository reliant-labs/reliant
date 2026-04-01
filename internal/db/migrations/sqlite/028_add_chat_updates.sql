-- +goose Up
-- Add chat_updates table for efficient sequence-based polling
-- This replaces timestamp-based polling for better gap detection and ordering

CREATE TABLE chat_updates (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    chat_id TEXT NOT NULL,
    sequence_number INTEGER NOT NULL,
    update_type TEXT NOT NULL CHECK (update_type IN (
        'message',
        'tool_approval',
        'thread',
        'workflow_event',
        'workflow_approval'
    )),
    entity_id TEXT NOT NULL,  -- ID of the updated entity (message_id, approval_id, etc.)
    data TEXT NOT NULL,       -- JSON representation of the update
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    
    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE,
    UNIQUE(chat_id, sequence_number)
);

CREATE INDEX idx_chat_updates_chat_seq ON chat_updates(chat_id, sequence_number);
CREATE INDEX idx_chat_updates_type ON chat_updates(update_type);
CREATE INDEX idx_chat_updates_entity ON chat_updates(entity_id);

-- Triggers to populate chat_updates table

-- Message updates (insert and update)
-- +goose StatementBegin
CREATE TRIGGER chat_updates_message_insert
AFTER INSERT ON messages
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'message',
        NEW.id,
        json_object(
            'update_type', 'message',
            'id', NEW.id,
            'role', NEW.role,
            'ordinal', NEW.ordinal,
            'thread', NEW.thread,
            'context_sequence', NEW.context_sequence,
            'streaming_state', NEW.streaming_state,
            'created_at', NEW.created_at,
            'updated_at', NEW.updated_at
        )
    );
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER chat_updates_message_update
AFTER UPDATE ON messages
WHEN OLD.updated_at != NEW.updated_at OR OLD.streaming_state != NEW.streaming_state
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'message',
        NEW.id,
        json_object(
            'update_type', 'message',
            'id', NEW.id,
            'role', NEW.role,
            'ordinal', NEW.ordinal,
            'thread', NEW.thread,
            'context_sequence', NEW.context_sequence,
            'streaming_state', NEW.streaming_state,
            'created_at', NEW.created_at,
            'updated_at', NEW.updated_at
        )
    );
END;
-- +goose StatementEnd

-- Content block updates
-- +goose StatementBegin
CREATE TRIGGER chat_updates_content_block_insert
AFTER INSERT ON message_content_blocks
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    SELECT 
        m.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = m.chat_id), 0) + 1,
        'message',
        m.id,
        json_object(
            'update_type', 'message',
            'id', m.id,
            'content_block_id', NEW.id
        )
    FROM messages m
    WHERE m.id = NEW.message_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER chat_updates_content_block_update
AFTER UPDATE ON message_content_blocks
WHEN OLD.updated_at != NEW.updated_at OR OLD.streaming_state != NEW.streaming_state
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    SELECT 
        m.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = m.chat_id), 0) + 1,
        'message',
        m.id,
        json_object(
            'update_type', 'message',
            'id', m.id,
            'content_block_id', NEW.id
        )
    FROM messages m
    WHERE m.id = NEW.message_id;
END;
-- +goose StatementEnd

-- Tool approval updates
-- +goose StatementBegin
CREATE TRIGGER chat_updates_tool_approval_insert
AFTER INSERT ON tool_approvals
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'tool_approval',
        NEW.id,
        json_object(
            'update_type', 'tool_approval',
            'id', NEW.id,
            'chat_id', NEW.chat_id,
            'content_block_id', NEW.content_block_id,
            'status', NEW.status,
            'created_at', NEW.created_at,
            'updated_at', NEW.updated_at
        )
    );
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER chat_updates_tool_approval_update
AFTER UPDATE ON tool_approvals
WHEN OLD.updated_at != NEW.updated_at
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'tool_approval',
        NEW.id,
        json_object(
            'update_type', 'tool_approval',
            'id', NEW.id,
            'chat_id', NEW.chat_id,
            'content_block_id', NEW.content_block_id,
            'status', NEW.status,
            'created_at', NEW.created_at,
            'updated_at', NEW.updated_at
        )
    );
END;
-- +goose StatementEnd

-- Active thread updates
-- +goose StatementBegin
CREATE TRIGGER chat_updates_active_thread_insert
AFTER INSERT ON active_threads
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'thread',
        NEW.id,
        json_object(
            'update_type', 'thread',
            'id', NEW.id,
            'chat_id', NEW.chat_id,
            'thread', NEW.thread,
            'status', NEW.status,
            'created_at', NEW.created_at
        )
    );
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER chat_updates_active_thread_update
AFTER UPDATE ON active_threads
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'thread',
        NEW.id,
        json_object(
            'update_type', 'thread',
            'id', NEW.id,
            'chat_id', NEW.chat_id,
            'thread', NEW.thread,
            'status', NEW.status,
            'created_at', NEW.created_at
        )
    );
END;
-- +goose StatementEnd

-- Workflow event updates
-- +goose StatementBegin
CREATE TRIGGER chat_updates_workflow_event_insert
AFTER INSERT ON workflow_events
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'workflow_event',
        NEW.id,
        json_object(
            'update_type', 'workflow_event',
            'id', NEW.id,
            'chat_id', NEW.chat_id,
            'workflow_name', NEW.workflow_name,
            'event_name', NEW.event_name,
            'created_at', NEW.created_at
        )
    );
END;
-- +goose StatementEnd

-- Workflow approval updates
-- +goose StatementBegin
CREATE TRIGGER chat_updates_workflow_approval_insert
AFTER INSERT ON workflow_approvals
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'workflow_approval',
        NEW.id,
        json_object(
            'update_type', 'workflow_approval',
            'id', NEW.id,
            'chat_id', NEW.chat_id,
            'status', NEW.status,
            'created_at', NEW.created_at,
            'updated_at', NEW.updated_at
        )
    );
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER chat_updates_workflow_approval_update
AFTER UPDATE ON workflow_approvals
WHEN OLD.updated_at != NEW.updated_at
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'workflow_approval',
        NEW.id,
        json_object(
            'update_type', 'workflow_approval',
            'id', NEW.id,
            'chat_id', NEW.chat_id,
            'status', NEW.status,
            'created_at', NEW.created_at,
            'updated_at', NEW.updated_at
        )
    );
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER IF EXISTS chat_updates_workflow_approval_update;
DROP TRIGGER IF EXISTS chat_updates_workflow_approval_insert;
DROP TRIGGER IF EXISTS chat_updates_workflow_event_insert;
DROP TRIGGER IF EXISTS chat_updates_active_thread_update;
DROP TRIGGER IF EXISTS chat_updates_active_thread_insert;
DROP TRIGGER IF EXISTS chat_updates_tool_approval_update;
DROP TRIGGER IF EXISTS chat_updates_tool_approval_insert;
DROP TRIGGER IF EXISTS chat_updates_content_block_update;
DROP TRIGGER IF EXISTS chat_updates_content_block_insert;
DROP TRIGGER IF EXISTS chat_updates_message_update;
DROP TRIGGER IF EXISTS chat_updates_message_insert;
DROP TABLE IF EXISTS chat_updates;
