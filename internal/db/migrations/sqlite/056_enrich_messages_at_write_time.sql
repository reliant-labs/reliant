-- +goose Up
-- Migration: Enrich message updates with content_blocks at WRITE time instead of READ time
-- Purpose: Simplify architecture by enriching chat_updates at the database level
--          instead of doing it in the websocket polling code.
--
-- Changes:
--   1. Drop old content_block triggers (no longer needed)
--   2. Recreate message triggers to include content_blocks in the JSON data

-- Drop the old content_block triggers (we don't need separate updates for content blocks)
DROP TRIGGER IF EXISTS chat_updates_content_block_insert;
DROP TRIGGER IF EXISTS chat_updates_content_block_update;

-- Drop existing message triggers so we can recreate them with enrichment
DROP TRIGGER IF EXISTS chat_updates_message_insert;
DROP TRIGGER IF EXISTS chat_updates_message_update;

-- +goose StatementBegin
-- Create new message_insert trigger that enriches with content_blocks
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
            'updated_at', NEW.updated_at,
            -- ENRICHMENT: Add content_blocks array by querying content_block_chunks (for UI display)
            'content_blocks', COALESCE(
                (
                    SELECT json_group_array(
                        json_object(
                            'id', cbc.id,
                            'index', cbc.block_index,
                            'type', cbc.block_type,
                            'content', cbc.content,
                            'chunk_index', cbc.chunk_index
                        )
                    )
                    FROM content_block_chunks cbc
                    WHERE cbc.message_id = NEW.id
                    ORDER BY cbc.block_index, cbc.chunk_index
                ),
                json_array()  -- Empty array if no content chunks
            )
        )
    );
END;
-- +goose StatementEnd

-- +goose StatementBegin
-- Create new message_update trigger that enriches with content_blocks
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
            'updated_at', NEW.updated_at,
            -- ENRICHMENT: Add content_blocks array by querying content_block_chunks (for UI display)
            'content_blocks', COALESCE(
                (
                    SELECT json_group_array(
                        json_object(
                            'id', cbc.id,
                            'index', cbc.block_index,
                            'type', cbc.block_type,
                            'content', cbc.content,
                            'chunk_index', cbc.chunk_index
                        )
                    )
                    FROM content_block_chunks cbc
                    WHERE cbc.message_id = NEW.id
                    ORDER BY cbc.block_index, cbc.chunk_index
                ),
                json_array()  -- Empty array if no content chunks
            )
        )
    );
END;
-- +goose StatementEnd

-- NOTE: We don't need a separate enrichment trigger because SaveMessage creates
-- content_block_chunks BEFORE creating the message, so the message INSERT trigger
-- above already has all chunks available when it queries.

-- +goose Down
-- Restore the old triggers from migration 053
DROP TRIGGER IF EXISTS chat_updates_message_insert;
DROP TRIGGER IF EXISTS chat_updates_message_update;
DROP TRIGGER IF EXISTS chat_updates_content_block_enrich;

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
