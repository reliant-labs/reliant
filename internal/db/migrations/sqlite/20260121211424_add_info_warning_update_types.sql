-- +goose Up
-- Migration: Add info and warning update types to chat_updates
-- These enable UI-only notifications that are shown to users but not saved to the thread

-- Step 1: Drop trigger that references chat_updates
DROP TRIGGER IF EXISTS chat_updates_chat_update;

-- Step 2: Create new table with updated constraint
-- SQLite doesn't support ALTER CHECK, so we need to recreate the table
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

-- Step 3: Copy data from old table
INSERT INTO chat_updates_new (id, chat_id, sequence_number, update_type, entity_id, data, created_at)
SELECT id, chat_id, sequence_number, update_type, entity_id, data, created_at
FROM chat_updates;

-- Step 4: Drop old table
DROP TABLE chat_updates;

-- Step 5: Rename new table
ALTER TABLE chat_updates_new RENAME TO chat_updates;

-- Step 6: Recreate indexes
CREATE INDEX idx_chat_updates_chat_seq ON chat_updates(chat_id, sequence_number DESC);
CREATE INDEX idx_chat_updates_created ON chat_updates(created_at DESC);

-- +goose StatementBegin
-- Step 7: Recreate trigger (matching current chats schema)
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

-- +goose Down
-- Revert: Remove info and warning from allowed update types

-- Step 1: Drop trigger
DROP TRIGGER IF EXISTS chat_updates_chat_update;

-- Step 2: Create table without new types
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
        'branch_snapshot',
        'chat',
        'run_output',
        'node_execution',
        'execution_log',
        'workflow_execution'
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
WHERE update_type NOT IN ('info', 'warning');

DROP TABLE chat_updates;

ALTER TABLE chat_updates_old RENAME TO chat_updates;

CREATE INDEX idx_chat_updates_chat_seq ON chat_updates(chat_id, sequence_number DESC);
CREATE INDEX idx_chat_updates_created ON chat_updates(created_at DESC);

-- +goose StatementBegin
-- Recreate trigger
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
