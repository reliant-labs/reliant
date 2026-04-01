-- +goose Up
-- Migration: Remove branch_snapshot update type
--
-- branch_snapshot was used to store inherited messages at branch time.
-- This is no longer needed - inherited messages are now resolved on-demand
-- via the context window chain when ListMessages is called.

-- Delete all existing branch_snapshot updates
DELETE FROM chat_updates WHERE update_type = 'branch_snapshot';

-- Recreate chat_updates table without branch_snapshot in the CHECK constraint
-- SQLite doesn't support ALTER CHECK CONSTRAINT, so we need to recreate

CREATE TABLE chat_updates_new (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    chat_id TEXT NOT NULL,
    sequence_number INTEGER NOT NULL,
    update_type TEXT NOT NULL CHECK (update_type IN (
        'message',
        'approval',
        'thread',
        'tool_call',
        'workflow_status',
        'error',
        'chat',
        'run_output',
        'node_execution',
        'execution_log',
        'workflow_execution',
        'info',
        'warning'
    )),
    entity_id TEXT NOT NULL,
    data TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,

    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE,
    UNIQUE(chat_id, sequence_number)
);

-- Copy all data (branch_snapshot already deleted above)
INSERT INTO chat_updates_new (id, chat_id, sequence_number, update_type, entity_id, data, created_at)
SELECT id, chat_id, sequence_number, update_type, entity_id, data, created_at
FROM chat_updates;

-- Drop trigger first (it references chat_updates)
DROP TRIGGER IF EXISTS chat_updates_chat_update;

-- Drop old table and rename
DROP TABLE chat_updates;
ALTER TABLE chat_updates_new RENAME TO chat_updates;

-- Recreate indexes
CREATE INDEX idx_chat_updates_chat_seq ON chat_updates(chat_id, sequence_number DESC);
CREATE INDEX idx_chat_updates_created ON chat_updates(created_at DESC);

-- Recreate the trigger for chat updates
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
        'chat',
        NEW.id,
        json_object(
            'update_type', 'chat',
            'chat_id', NEW.id,
            'workflow_name', NEW.workflow_name,
            'state', NEW.state,
            'title', NEW.title
        ),
        datetime('now')
    );
END;
-- +goose StatementEnd

-- +goose Down
-- Re-add branch_snapshot support (recreate table with the type)

CREATE TABLE chat_updates_new (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    chat_id TEXT NOT NULL,
    sequence_number INTEGER NOT NULL,
    update_type TEXT NOT NULL CHECK (update_type IN (
        'message',
        'approval',
        'thread',
        'tool_call',
        'workflow_status',
        'error',
        'branch_snapshot',
        'chat',
        'run_output',
        'node_execution',
        'execution_log',
        'workflow_execution',
        'info',
        'warning'
    )),
    entity_id TEXT NOT NULL,
    data TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,

    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE,
    UNIQUE(chat_id, sequence_number)
);

INSERT INTO chat_updates_new (id, chat_id, sequence_number, update_type, entity_id, data, created_at)
SELECT id, chat_id, sequence_number, update_type, entity_id, data, created_at
FROM chat_updates;

-- Drop trigger first (it references chat_updates)
DROP TRIGGER IF EXISTS chat_updates_chat_update;

DROP TABLE chat_updates;
ALTER TABLE chat_updates_new RENAME TO chat_updates;

CREATE INDEX idx_chat_updates_chat_seq ON chat_updates(chat_id, sequence_number DESC);
CREATE INDEX idx_chat_updates_created ON chat_updates(created_at DESC);

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
        'chat',
        NEW.id,
        json_object(
            'update_type', 'chat',
            'chat_id', NEW.id,
            'workflow_name', NEW.workflow_name,
            'state', NEW.state,
            'title', NEW.title
        ),
        datetime('now')
    );
END;
-- +goose StatementEnd
