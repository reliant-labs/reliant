-- +goose Up
-- Add triggers to emit updates when chat metadata changes (agent, workflow_name, etc.)
-- This ensures the UI receives real-time updates when chat configuration changes
-- Two triggers:
--   1. chat_updates table (for per-chat WebSocket)
--   2. user_updates table (for global user WebSocket)

-- Trigger 1: Per-chat WebSocket updates
-- +goose StatementBegin
CREATE TRIGGER IF NOT EXISTS chat_updates_chat_update
AFTER UPDATE ON chats
FOR EACH ROW
WHEN (
    -- Only emit updates when relevant fields change
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
            'auto_approve', NEW.auto_approve,
            'state', NEW.state,
            'title', NEW.title,
            'updated_at', NEW.updated_at
        )
    );
END;
-- +goose StatementEnd

-- Trigger 2: Global user WebSocket updates (only for agent/workflow/model/auto_approve changes)
-- +goose StatementBegin
CREATE TRIGGER IF NOT EXISTS user_updates_chat_config_update
AFTER UPDATE ON chats
FOR EACH ROW
WHEN (
    -- Only emit to global websocket when agent/workflow/model/auto_approve changes
    -- (state changes have their own user_update via UpdateChatState)
    OLD.agent IS NOT NEW.agent OR
    OLD.workflow_name IS NOT NEW.workflow_name OR
    OLD.model IS NOT NEW.model OR
    OLD.auto_approve IS NOT NEW.auto_approve
)
BEGIN
    INSERT INTO user_updates (
        user_id,
        sequence_number,
        project_id,
        worktree_id,
        chat_id,
        update_type,
        entity_type,
        entity_id,
        data
    ) VALUES (
        NEW.user_id,
        COALESCE((SELECT MAX(sequence_number) FROM user_updates WHERE user_id = NEW.user_id), 0) + 1,
        NEW.project_id,
        NEW.worktree_id,
        NEW.id,
        'chat_config_changed',
        'chat',
        NEW.id,
        json_object(
            'chat_id', NEW.id,
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

-- +goose Down
DROP TRIGGER IF EXISTS chat_updates_chat_update;
DROP TRIGGER IF EXISTS user_updates_chat_config_update;
