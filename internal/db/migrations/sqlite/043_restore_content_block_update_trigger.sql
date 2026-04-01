-- +goose Up
-- Restore content_block UPDATE trigger to notify UI when streaming completes
--
-- Migration 037 removed content_block triggers to prevent duplicate messages.
-- However, since message.streaming_state is deprecated (computed from blocks),
-- we need the content_block UPDATE trigger to fire when blocks are marked complete.
--
-- This trigger ONLY fires on streaming_state changes (not every update),
-- preventing the duplication issue while ensuring the UI gets notified when
-- streaming finishes.

-- +goose StatementBegin
CREATE TRIGGER chat_updates_content_block_update
AFTER UPDATE ON message_content_blocks
WHEN OLD.streaming_state != NEW.streaming_state
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
DROP TRIGGER IF EXISTS chat_updates_content_block_update;
