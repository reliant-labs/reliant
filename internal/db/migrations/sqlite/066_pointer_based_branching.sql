-- +goose Up
-- Migration: Pointer-based chat branching
-- Adds support for efficient branching without copying messages

-- Add parent_context_sequence to chats table
-- This stores the context_sequence from the parent chat at branch time
-- so we know which messages to inherit
ALTER TABLE chats ADD COLUMN parent_context_sequence INTEGER;

-- Recreate chat_updates table with branch_snapshot update type
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

-- +goose Down
-- Remove the new column
ALTER TABLE chats DROP COLUMN parent_context_sequence;

-- Revert chat_updates table to original constraint
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
        'error'
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
WHERE update_type != 'branch_snapshot';

DROP TABLE chat_updates;
ALTER TABLE chat_updates_old RENAME TO chat_updates;

CREATE INDEX idx_chat_updates_chat_seq ON chat_updates(chat_id, sequence_number DESC);
CREATE INDEX idx_chat_updates_created ON chat_updates(created_at DESC);
