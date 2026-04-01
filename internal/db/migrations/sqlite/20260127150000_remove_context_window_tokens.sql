-- +goose Up
-- Remove token columns from context_windows table.
-- Token counts are now derived from messages using GetThreadTokenCount.
-- This eliminates sync issues and simplifies the data model.

-- SQLite doesn't support DROP COLUMN directly in older versions,
-- so we need to recreate the table without the token columns.

-- Step 1: Create new table without token columns
CREATE TABLE context_windows_new (
    id TEXT PRIMARY KEY,
    thread_id TEXT NOT NULL,
    sequence INTEGER NOT NULL DEFAULT 0,
    compaction_summary_message_id TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (thread_id) REFERENCES threads(id) ON DELETE CASCADE,
    UNIQUE(thread_id, sequence)
);

-- Step 2: Copy data (excluding token columns)
INSERT INTO context_windows_new (id, thread_id, sequence, compaction_summary_message_id, created_at)
SELECT id, thread_id, sequence, compaction_summary_message_id, created_at
FROM context_windows;

-- Step 3: Drop old table
DROP TABLE context_windows;

-- Step 4: Rename new table
ALTER TABLE context_windows_new RENAME TO context_windows;

-- Step 5: Recreate indexes
CREATE INDEX idx_context_windows_thread ON context_windows(thread_id, sequence DESC);

-- +goose Down
-- Restore token columns
ALTER TABLE context_windows ADD COLUMN total_input_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE context_windows ADD COLUMN total_output_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE context_windows ADD COLUMN total_cache_creation_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE context_windows ADD COLUMN total_cache_read_tokens INTEGER NOT NULL DEFAULT 0;
