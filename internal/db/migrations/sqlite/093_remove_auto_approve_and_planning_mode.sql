-- +goose Up
-- Migration: Remove auto_approve and is_planning_mode from chats table
-- These are now workflow input parameters (inputs.mode), not chat metadata
-- The workflow's inputs.mode enum ["manual", "auto", "plan"] is the source of truth

-- Drop the planning mode index first
DROP INDEX IF EXISTS idx_chats_planning_mode;

-- IMPORTANT: Drop triggers BEFORE dropping columns, otherwise SQLite errors
-- because the triggers reference the columns being dropped
DROP TRIGGER IF EXISTS chat_updates_chat_update;
DROP TRIGGER IF EXISTS user_updates_chat_config_update;

-- Drop the columns (SQLite 3.35+ supports ALTER TABLE DROP COLUMN)
ALTER TABLE chats DROP COLUMN auto_approve;
ALTER TABLE chats DROP COLUMN is_planning_mode;

-- Recreate chat_updates trigger without auto_approve
-- +goose StatementBegin
CREATE TRIGGER IF NOT EXISTS chat_updates_chat_update
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
    -- Emit to chat_updates for per-chat WebSocket
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

-- Recreate user_updates trigger without auto_approve
DROP TRIGGER IF EXISTS user_updates_chat_config_update;
-- +goose StatementBegin
CREATE TRIGGER IF NOT EXISTS user_updates_chat_config_update
AFTER UPDATE ON chats
FOR EACH ROW
WHEN (
    -- Only emit to global websocket when agent/workflow/model changes
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
        (SELECT COALESCE(MAX(sequence_number), 0) + 1 FROM user_updates WHERE user_id = NEW.user_id),
        'chat',
        NEW.id,
        'chat_config_changed',
        json_object(
            'update_type', 'chat_config_changed',
            'id', NEW.id,
            'agent', NEW.agent,
            'workflow_name', NEW.workflow_name,
            'model', NEW.model,
            'temperature', NEW.temperature,
            'max_tokens', NEW.max_tokens,
            'state', NEW.state,
            'title', NEW.title,
            'previous_agent', OLD.agent,
            'previous_workflow_name', OLD.workflow_name,
            'updated_at', NEW.updated_at
        )
    );
END;
-- +goose StatementEnd

-- +goose Down
-- Re-add the columns if needed for rollback
ALTER TABLE chats ADD COLUMN auto_approve BOOLEAN DEFAULT FALSE;
ALTER TABLE chats ADD COLUMN is_planning_mode BOOLEAN NOT NULL DEFAULT FALSE;

-- Recreate the index
CREATE INDEX IF NOT EXISTS idx_chats_planning_mode ON chats(is_planning_mode);

-- Recreate triggers with auto_approve
DROP TRIGGER IF EXISTS chat_updates_chat_update;
-- +goose StatementBegin
CREATE TRIGGER IF NOT EXISTS chat_updates_chat_update
AFTER UPDATE ON chats
FOR EACH ROW
WHEN (
    OLD.agent IS NOT NEW.agent OR
    OLD.workflow_name IS NOT NEW.workflow_name OR
    OLD.model IS NOT NEW.model OR
    OLD.temperature IS NOT NEW.temperature OR
    OLD.max_tokens IS NOT NEW.max_tokens OR
    OLD.auto_approve IS NOT NEW.auto_approve OR
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
            'auto_approve', NEW.auto_approve,
            'state', NEW.state,
            'title', NEW.title,
            'updated_at', NEW.updated_at
        )
    );
END;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS user_updates_chat_config_update;
-- +goose StatementBegin
CREATE TRIGGER IF NOT EXISTS user_updates_chat_config_update
AFTER UPDATE ON chats
FOR EACH ROW
WHEN (
    OLD.agent IS NOT NEW.agent OR
    OLD.workflow_name IS NOT NEW.workflow_name OR
    OLD.model IS NOT NEW.model OR
    OLD.auto_approve IS NOT NEW.auto_approve
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
        (SELECT COALESCE(MAX(sequence_number), 0) + 1 FROM user_updates WHERE user_id = NEW.user_id),
        'chat',
        NEW.id,
        'chat_config_changed',
        json_object(
            'update_type', 'chat_config_changed',
            'id', NEW.id,
            'agent', NEW.agent,
            'workflow_name', NEW.workflow_name,
            'model', NEW.model,
            'temperature', NEW.temperature,
            'max_tokens', NEW.max_tokens,
            'auto_approve', NEW.auto_approve,
            'state', NEW.state,
            'title', NEW.title,
            'previous_agent', OLD.agent,
            'previous_workflow_name', OLD.workflow_name,
            'updated_at', NEW.updated_at
        )
    );
END;
-- +goose StatementEnd
