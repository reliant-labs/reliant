-- +goose Up
-- Add 'streaming_delta' to chat_updates.update_type CHECK constraint

-- Step 1: Create a new temporary table with the updated constraint
CREATE TABLE chat_updates_new (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    chat_id TEXT NOT NULL,
    sequence_number INTEGER NOT NULL,
    update_type TEXT NOT NULL CHECK (update_type IN (
        'message',
        'approval',
        'thread',
        'tool_call',
        'streaming_delta'
    )),
    entity_id TEXT NOT NULL,
    data TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,

    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE,
    UNIQUE(chat_id, sequence_number)
);

-- Step 2: Copy all existing data
INSERT INTO chat_updates_new (id, chat_id, sequence_number, update_type, entity_id, data, created_at)
SELECT id, chat_id, sequence_number, update_type, entity_id, data, created_at
FROM chat_updates;

-- Step 3: Drop the old table
DROP TABLE chat_updates;

-- Step 4: Rename the new table
ALTER TABLE chat_updates_new RENAME TO chat_updates;

-- Step 5: Recreate indexes
CREATE INDEX idx_chat_updates_chat_seq ON chat_updates(chat_id, sequence_number DESC);
CREATE INDEX idx_chat_updates_created ON chat_updates(created_at DESC);

-- +goose Down
-- Revert to original constraint without 'streaming_delta'

-- Step 1: Create a new temporary table with the original constraint
CREATE TABLE chat_updates_new (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    chat_id TEXT NOT NULL,
    sequence_number INTEGER NOT NULL,
    update_type TEXT NOT NULL CHECK (update_type IN (
        'message',
        'approval',
        'thread',
        'tool_call'
    )),
    entity_id TEXT NOT NULL,
    data TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,

    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE,
    UNIQUE(chat_id, sequence_number)
);

-- Step 2: Copy all data EXCEPT streaming_delta rows (they would violate the constraint)
INSERT INTO chat_updates_new (id, chat_id, sequence_number, update_type, entity_id, data, created_at)
SELECT id, chat_id, sequence_number, update_type, entity_id, data, created_at
FROM chat_updates
WHERE update_type != 'streaming_delta';

-- Step 3: Drop the old table
DROP TABLE chat_updates;

-- Step 4: Rename the new table
ALTER TABLE chat_updates_new RENAME TO chat_updates;

-- Step 5: Recreate indexes
CREATE INDEX idx_chat_updates_chat_seq ON chat_updates(chat_id, sequence_number DESC);
CREATE INDEX idx_chat_updates_created ON chat_updates(created_at DESC);
