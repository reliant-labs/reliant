-- +goose Up
-- Drop the chat_updates and user_updates triggers that fire on UPDATE chats.
-- These are replaced by application-level code in the repo layer that calls
-- CreateChatUpdate / CreateUserUpdate explicitly, which also publishes to the
-- streaming UpdateHub for event-driven delivery (no more polling).

DROP TRIGGER IF EXISTS chat_updates_chat_update;
DROP TRIGGER IF EXISTS user_updates_chat_config_update;

-- +goose Down
-- Recreate chat_updates trigger (update_type 7 = CHAT_UPDATE_TYPE_CHAT_UPDATE)
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
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data, created_at)
    VALUES (
        NEW.id,
        COALESCE((SELECT MAX(sequence_number) + 1 FROM chat_updates WHERE chat_id = NEW.id), 1),
        7,
        NEW.id,
        json_object(
            'update_type', 7,
            'chat_id', NEW.id,
            'workflow_name', NEW.workflow_name,
            'state', NEW.state,
            'title', NEW.title
        ),
        datetime('now')
    );
END;
-- +goose StatementEnd

-- Recreate user_updates trigger (update_type 2 = chat_config_changed, entity_type 1 = chat)
-- +goose StatementBegin
CREATE TRIGGER user_updates_chat_config_update
AFTER UPDATE ON chats
FOR EACH ROW
WHEN (
    OLD.workflow_name IS NOT NEW.workflow_name
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
        2,
        1,
        NEW.id,
        json_object(
            'update_type', 2,
            'chat_id', NEW.id,
            'workflow_name', NEW.workflow_name
        )
    );
END;
-- +goose StatementEnd
