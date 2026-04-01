-- +goose Up
-- Remove streaming_delta from chat_updates as it's now handled by in-memory hub
-- This eliminates DB bloat from ephemeral streaming events

-- Step 1: Drop triggers that reference chat_updates
DROP TRIGGER IF EXISTS chat_updates_chat_update;

-- Step 2: Delete existing streaming_delta rows (ephemeral, no longer needed)
DELETE FROM chat_updates WHERE update_type = 'streaming_delta';

-- Step 2: Recreate chat_updates table without streaming_delta in CHECK constraint
-- We use table recreation since SQLite doesn't support modifying CHECK constraints

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

-- Step 3: Copy existing data
INSERT INTO chat_updates_new (id, chat_id, sequence_number, update_type, entity_id, data, created_at)
SELECT id, chat_id, sequence_number, update_type, entity_id, data, created_at
FROM chat_updates
WHERE update_type != 'streaming_delta';

-- Step 4: Drop old table and rename new one
DROP TABLE chat_updates;
ALTER TABLE chat_updates_new RENAME TO chat_updates;

-- Step 5: Recreate indexes
CREATE INDEX idx_chat_updates_chat_seq ON chat_updates(chat_id, sequence_number DESC);
CREATE INDEX idx_chat_updates_created ON chat_updates(created_at DESC);

-- +goose StatementBegin
-- Step 6: Recreate trigger (matching current chats schema)
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
-- Add streaming_delta back to the CHECK constraint
-- Note: We cannot restore the deleted streaming_delta rows

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

INSERT INTO chat_updates_new (id, chat_id, sequence_number, update_type, entity_id, data, created_at)
SELECT id, chat_id, sequence_number, update_type, entity_id, data, created_at
FROM chat_updates;

DROP TABLE chat_updates;
ALTER TABLE chat_updates_new RENAME TO chat_updates;

CREATE INDEX idx_chat_updates_chat_seq ON chat_updates(chat_id, sequence_number DESC);
CREATE INDEX idx_chat_updates_created ON chat_updates(created_at DESC);
