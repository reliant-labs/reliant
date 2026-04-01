-- +goose Up
-- Remove model, temperature, max_tokens columns from chats table
-- These are now workflow input parameters, not stored on the chat

-- IMPORTANT: Drop triggers BEFORE dropping columns, otherwise SQLite errors
DROP TRIGGER IF EXISTS chat_updates_chat_update;
DROP TRIGGER IF EXISTS user_updates_chat_config_update;

-- Drop the columns
ALTER TABLE chats DROP COLUMN model;
ALTER TABLE chats DROP COLUMN temperature;
ALTER TABLE chats DROP COLUMN max_tokens;

-- Recreate chat_updates trigger without model/temperature/max_tokens
-- +goose StatementBegin
CREATE TRIGGER chat_updates_chat_update
AFTER UPDATE ON chats
FOR EACH ROW
WHEN (
    OLD.workflow_name IS NOT NEW.workflow_name OR
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
            'workflow_name', NEW.workflow_name,
            'state', NEW.state,
            'title', NEW.title,
            'updated_at', NEW.updated_at
        )
    );
END;
-- +goose StatementEnd

-- Recreate user_updates trigger without model
-- +goose StatementBegin
CREATE TRIGGER user_updates_chat_config_update
AFTER UPDATE ON chats
FOR EACH ROW
WHEN (
    -- Only emit to global websocket when workflow changes
    OLD.workflow_name IS NOT NEW.workflow_name
)
BEGIN
    INSERT INTO user_updates (
        user_id,
        sequence_number,
        entity_type,
        entity_id,
        update_type,
        data
    )
    VALUES (
        NEW.user_id,
        COALESCE((SELECT MAX(sequence_number) FROM user_updates WHERE user_id = NEW.user_id), 0) + 1,
        'chat',
        NEW.id,
        'config_update',
        json_object(
            'chat_id', NEW.id,
            'workflow_name', NEW.workflow_name
        )
    );
END;
-- +goose StatementEnd

-- +goose Down
-- Add columns back
ALTER TABLE chats ADD COLUMN model TEXT DEFAULT 'claude-3-5-sonnet';
ALTER TABLE chats ADD COLUMN temperature REAL DEFAULT 0.7;
ALTER TABLE chats ADD COLUMN max_tokens INTEGER;

-- Recreate triggers with model/temperature/max_tokens
DROP TRIGGER IF EXISTS chat_updates_chat_update;
DROP TRIGGER IF EXISTS user_updates_chat_config_update;

-- +goose StatementBegin
CREATE TRIGGER chat_updates_chat_update
AFTER UPDATE ON chats
FOR EACH ROW
WHEN (
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

-- +goose StatementBegin
CREATE TRIGGER user_updates_chat_config_update
AFTER UPDATE ON chats
FOR EACH ROW
WHEN (
    OLD.workflow_name IS NOT NEW.workflow_name OR
    OLD.model IS NOT NEW.model
)
BEGIN
    INSERT INTO user_updates (
        user_id,
        sequence_number,
        entity_type,
        entity_id,
        update_type,
        data
    )
    VALUES (
        NEW.user_id,
        COALESCE((SELECT MAX(sequence_number) FROM user_updates WHERE user_id = NEW.user_id), 0) + 1,
        'chat',
        NEW.id,
        'config_update',
        json_object(
            'chat_id', NEW.id,
            'workflow_name', NEW.workflow_name,
            'model', NEW.model
        )
    );
END;
-- +goose StatementEnd
