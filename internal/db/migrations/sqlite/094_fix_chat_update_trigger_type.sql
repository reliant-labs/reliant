-- +goose Up
-- Fix: Migration 093 incorrectly used 'chat_changed' instead of 'chat' for update_type
-- The CHECK constraint only allows 'chat', causing title updates to fail

DROP TRIGGER IF EXISTS chat_updates_chat_update;
-- +goose StatementBegin
CREATE TRIGGER chat_updates_chat_update
AFTER UPDATE ON chats
FOR EACH ROW
WHEN (
    OLD.agent IS NOT NEW.agent OR
    OLD.workflow_name IS NOT NEW.workflow_name OR
    OLD.model IS NOT NEW.model OR
    OLD.temperature IS NOT NEW.temperature OR
    OLD.max_tokens IS NOT NEW.max_tokens OR
    OLD.state IS NOT NEW.state OR
    OLD.title IS NOT NEW.title
)
BEGIN
    INSERT INTO chat_updates (
        chat_id,
        sequence_number,
        update_type,
        entity_id,
        data
    ) VALUES (
        NEW.id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.id), 0) + 1,
        'chat',
        NEW.id,
        json_object(
            'update_type', 'chat',
            'id', NEW.id,
            'chat_id', NEW.id,
            'agent', NEW.agent,
            'workflow_name', NEW.workflow_name,
            'model', NEW.model,
            'temperature', NEW.temperature,
            'max_tokens', NEW.max_tokens,
            'state', NEW.state,
            'title', NEW.title,
            'updated_at', NEW.updated_at
        )
    );
END;
-- +goose StatementEnd

-- +goose Down
-- Revert to the broken trigger (not recommended)
DROP TRIGGER IF EXISTS chat_updates_chat_update;
-- +goose StatementBegin
CREATE TRIGGER chat_updates_chat_update
AFTER UPDATE ON chats
FOR EACH ROW
WHEN (
    OLD.agent IS NOT NEW.agent OR
    OLD.workflow_name IS NOT NEW.workflow_name OR
    OLD.model IS NOT NEW.model OR
    OLD.temperature IS NOT NEW.temperature OR
    OLD.max_tokens IS NOT NEW.max_tokens OR
    OLD.state IS NOT NEW.state OR
    OLD.title IS NOT NEW.title
)
BEGIN
    INSERT INTO chat_updates (
        chat_id,
        sequence_number,
        update_type,
        entity_id,
        data
    ) VALUES (
        NEW.id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.id), 0) + 1,
        'chat_changed',
        NEW.id,
        json_object(
            'update_type', 'chat_changed',
            'id', NEW.id,
            'chat_id', NEW.id,
            'agent', NEW.agent,
            'workflow_name', NEW.workflow_name,
            'model', NEW.model,
            'temperature', NEW.temperature,
            'max_tokens', NEW.max_tokens,
            'state', NEW.state,
            'title', NEW.title,
            'updated_at', NEW.updated_at
        )
    );
END;
-- +goose StatementEnd
