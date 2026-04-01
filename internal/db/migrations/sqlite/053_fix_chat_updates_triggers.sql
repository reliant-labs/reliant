-- +goose Up
-- Migration: Fix chat_updates triggers to use correct column names and update_type values
-- Purpose: Fix migration 049 which used wrong column names (update_data instead of data)
--          and wrong update_type value ('content_block' instead of 'message')

-- Drop and recreate all message/content_block triggers with correct column names

DROP TRIGGER IF EXISTS chat_updates_message_insert;
DROP TRIGGER IF EXISTS chat_updates_message_update;
DROP TRIGGER IF EXISTS chat_updates_content_block_insert;
DROP TRIGGER IF EXISTS chat_updates_content_block_update;

-- +goose StatementBegin
CREATE TRIGGER chat_updates_message_insert
AFTER INSERT ON messages
BEGIN
    INSERT INTO chat_updates (
        chat_id, sequence_number, update_type, entity_id, data
    )
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'message',
        NEW.id,
        json_object(
            'update_type', 'message',
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
        )
    );
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER chat_updates_message_update
AFTER UPDATE ON messages
BEGIN
    INSERT INTO chat_updates (
        chat_id, sequence_number, update_type, entity_id, data
    )
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'message',
        NEW.id,
        json_object(
            'update_type', 'message',
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
        )
    );
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER chat_updates_content_block_insert
AFTER INSERT ON message_content_blocks
BEGIN
    INSERT INTO chat_updates (
        chat_id, sequence_number, update_type, entity_id, data
    )
    SELECT
        m.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = m.chat_id), 0) + 1,
        'message',
        NEW.message_id,
        json_object(
            'update_type', 'content_block',
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
        )
    FROM messages m
    WHERE m.id = NEW.message_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER chat_updates_content_block_update
AFTER UPDATE ON message_content_blocks
BEGIN
    INSERT INTO chat_updates (
        chat_id, sequence_number, update_type, entity_id, data
    )
    SELECT
        m.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = m.chat_id), 0) + 1,
        'message',
        NEW.message_id,
        json_object(
            'update_type', 'content_block',
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
        )
    FROM messages m
    WHERE m.id = NEW.message_id;
END;
-- +goose StatementEnd

-- +goose Down
-- Restore broken triggers (not recommended)
DROP TRIGGER IF EXISTS chat_updates_message_insert;
DROP TRIGGER IF EXISTS chat_updates_message_update;
DROP TRIGGER IF EXISTS chat_updates_content_block_insert;
DROP TRIGGER IF EXISTS chat_updates_content_block_update;
