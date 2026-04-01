-- +goose Up
-- Add content_block INSERT trigger for tool_call and tool_result blocks
--
-- Migration 037 removed all content_block triggers to prevent duplicate messages.
-- Migration 043 added back the UPDATE trigger for streaming_state changes.
--
-- However, tool_call and tool_result blocks are created with streaming_state='complete',
-- so they never trigger the UPDATE trigger. The UI needs to be notified when these
-- blocks are added to messages.
--
-- This trigger ONLY fires for tool_call and tool_result blocks (not text blocks),
-- preventing the duplication issue while ensuring tool usage is properly displayed.

-- +goose StatementBegin
CREATE TRIGGER chat_updates_content_block_insert
AFTER INSERT ON message_content_blocks
WHEN NEW.block_type IN ('tool_call', 'tool_result')
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
            'role', m.role,
            'ordinal', m.ordinal,
            'thread', m.thread,
            'context_sequence', m.context_sequence,
            'streaming_state', m.streaming_state,
            'created_at', m.created_at,
            'updated_at', m.updated_at,
            'content_block_id', NEW.id
        )
    FROM messages m
    WHERE m.id = NEW.message_id;
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER IF EXISTS chat_updates_content_block_insert;
