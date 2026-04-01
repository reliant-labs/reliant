-- +goose Up
-- Remove agent column from chats table
-- Agent is now a workflow input parameter, not stored on the chat

-- IMPORTANT: Drop triggers BEFORE dropping columns, otherwise SQLite errors
DROP TRIGGER IF EXISTS chat_updates_chat_update;
DROP TRIGGER IF EXISTS user_updates_chat_config_update;

-- Drop the agent column
ALTER TABLE chats DROP COLUMN agent;

-- Recreate chat_updates trigger without agent
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

-- Recreate user_updates trigger without agent
-- +goose StatementBegin
CREATE TRIGGER user_updates_chat_config_update
AFTER UPDATE ON chats
FOR EACH ROW
WHEN (
    -- Only emit to global websocket when workflow/model changes
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

-- +goose Down
-- Add agent column back
ALTER TABLE chats ADD COLUMN agent TEXT;

-- Recreate triggers with agent
DROP TRIGGER IF EXISTS chat_updates_chat_update;
DROP TRIGGER IF EXISTS user_updates_chat_config_update;

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

-- +goose StatementBegin
CREATE TRIGGER user_updates_chat_config_update
AFTER UPDATE ON chats
FOR EACH ROW
WHEN (
    OLD.agent IS NOT NEW.agent OR
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
            'agent', NEW.agent,
            'workflow_name', NEW.workflow_name,
            'model', NEW.model
        )
    );
END;
-- +goose StatementEnd
