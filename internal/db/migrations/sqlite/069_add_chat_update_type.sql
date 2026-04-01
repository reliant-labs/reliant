-- +goose Up
-- Add 'chat' update_type to chat_updates table for chat metadata changes

-- Drop triggers that reference chat_updates
DROP TRIGGER IF EXISTS chat_updates_chat_update;
DROP TRIGGER IF EXISTS chat_updates_message_insert;
DROP TRIGGER IF EXISTS chat_updates_message_update;
DROP TRIGGER IF EXISTS chat_updates_content_block_insert;
DROP TRIGGER IF EXISTS chat_updates_content_block_update;

-- Recreate chat_updates table with 'chat' update type
CREATE TABLE chat_updates_new (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    chat_id TEXT NOT NULL,
    sequence_number INTEGER NOT NULL,
    update_type TEXT NOT NULL CHECK (update_type IN (
        'message',
        'approval',
        'thread',
        'tool_call',
        'streaming_delta',
        'workflow_status',
        'error',
        'branch_snapshot',
        'chat'
    )),
    entity_id TEXT NOT NULL,
    data TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,

    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE,
    UNIQUE(chat_id, sequence_number)
);

-- Copy existing data
INSERT INTO chat_updates_new (id, chat_id, sequence_number, update_type, entity_id, data, created_at)
SELECT id, chat_id, sequence_number, update_type, entity_id, data, created_at
FROM chat_updates;

-- Drop old table and rename
DROP TABLE chat_updates;
ALTER TABLE chat_updates_new RENAME TO chat_updates;

-- Recreate indexes
CREATE INDEX idx_chat_updates_chat_seq ON chat_updates(chat_id, sequence_number DESC);
CREATE INDEX idx_chat_updates_created ON chat_updates(created_at DESC);

-- Recreate chat_updates_chat_update trigger
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

-- +goose Down
-- Revert chat_updates table to remove 'chat' update type
CREATE TABLE chat_updates_old (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    chat_id TEXT NOT NULL,
    sequence_number INTEGER NOT NULL,
    update_type TEXT NOT NULL CHECK (update_type IN (
        'message',
        'approval',
        'thread',
        'tool_call',
        'streaming_delta',
        'workflow_status',
        'error',
        'branch_snapshot'
    )),
    entity_id TEXT NOT NULL,
    data TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,

    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE,
    UNIQUE(chat_id, sequence_number)
);

INSERT INTO chat_updates_old (id, chat_id, sequence_number, update_type, entity_id, data, created_at)
SELECT id, chat_id, sequence_number, update_type, entity_id, data, created_at
FROM chat_updates
WHERE update_type != 'chat';

DROP TABLE chat_updates;
ALTER TABLE chat_updates_old RENAME TO chat_updates;

CREATE INDEX idx_chat_updates_chat_seq ON chat_updates(chat_id, sequence_number DESC);
CREATE INDEX idx_chat_updates_created ON chat_updates(created_at DESC);
