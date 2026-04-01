-- +goose Up
-- Migration: Fix messages UNIQUE constraint to include thread
-- The constraint should be on (chat_id, thread, ordinal) not just (chat_id, ordinal)
-- since multiple threads in the same chat can have messages with the same ordinal

-- SQLite doesn't support ALTER TABLE to modify constraints directly
-- We need to recreate the table with the correct constraint

-- Step 1: Create new table with correct constraint
CREATE TABLE messages_new (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    ordinal BIGINT NOT NULL,
    thread TEXT NOT NULL DEFAULT '0',
    context_sequence INTEGER NOT NULL DEFAULT 0,
    role TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'system', 'tool')),
    model TEXT,
    agent TEXT,
    streaming_state TEXT NOT NULL DEFAULT 'complete' CHECK (streaming_state IN ('streaming', 'complete', 'failed')),
    streaming_completed_at TIMESTAMP,
    input_tokens INTEGER DEFAULT 0,
    output_tokens INTEGER DEFAULT 0,
    cache_creation_tokens INTEGER DEFAULT 0,
    cache_read_tokens INTEGER DEFAULT 0,
    workflow_id TEXT,
    run_id TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE,
    UNIQUE(chat_id, thread, ordinal)
);

-- Step 2: Copy data from old table to new table
INSERT INTO messages_new
SELECT * FROM messages;

-- Step 3: Drop old table
DROP TABLE messages;

-- Step 4: Rename new table to original name
ALTER TABLE messages_new RENAME TO messages;

-- Step 5: Recreate indexes (if any existed on the messages table)
-- Add any necessary indexes here

-- +goose Down
-- Rollback: Restore original UNIQUE constraint on (chat_id, ordinal)
CREATE TABLE messages_new (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    ordinal BIGINT NOT NULL,
    thread TEXT NOT NULL DEFAULT '0',
    context_sequence INTEGER NOT NULL DEFAULT 0,
    role TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'system', 'tool')),
    model TEXT,
    agent TEXT,
    streaming_state TEXT NOT NULL DEFAULT 'complete' CHECK (streaming_state IN ('streaming', 'complete', 'failed')),
    streaming_completed_at TIMESTAMP,
    input_tokens INTEGER DEFAULT 0,
    output_tokens INTEGER DEFAULT 0,
    cache_creation_tokens INTEGER DEFAULT 0,
    cache_read_tokens INTEGER DEFAULT 0,
    workflow_id TEXT,
    run_id TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE,
    UNIQUE(chat_id, ordinal)
);

INSERT INTO messages_new SELECT * FROM messages;
DROP TABLE messages;
ALTER TABLE messages_new RENAME TO messages;
