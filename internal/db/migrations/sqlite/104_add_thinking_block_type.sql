-- Migration: Add 'thinking' to allowed block_type values
-- This enables saving Claude's extended thinking/reasoning blocks

-- +goose Up

-- SQLite doesn't support ALTER COLUMN to modify CHECK constraints
-- We need to recreate the table with the new constraint

-- Create new table with updated constraint (includes 'thinking')
CREATE TABLE message_content_blocks_new (
    id TEXT PRIMARY KEY,
    message_id TEXT NOT NULL,
    position INTEGER NOT NULL DEFAULT 0,
    block_type TEXT NOT NULL CHECK (block_type IN ('text', 'tool_call', 'tool_result', 'image', 'file_reference', 'thinking')),
    content TEXT,
    tool_name TEXT,
    tool_input TEXT,
    tool_call_id TEXT,
    is_error BOOLEAN,
    version INTEGER,
    node_id TEXT,
    node_path TEXT,
    activity_id TEXT,
    workflow_run_id TEXT,
    attempt_number INTEGER DEFAULT 1,
    thought_signature TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE CASCADE
);

-- Copy data from old table
INSERT INTO message_content_blocks_new
SELECT * FROM message_content_blocks;

-- Drop old table
DROP TABLE message_content_blocks;

-- Rename new table
ALTER TABLE message_content_blocks_new RENAME TO message_content_blocks;

-- Recreate indexes
CREATE INDEX idx_content_blocks_message_id ON message_content_blocks(message_id);
CREATE INDEX idx_content_blocks_tool_call_id ON message_content_blocks(tool_call_id);
CREATE INDEX idx_content_blocks_activity ON message_content_blocks(activity_id, workflow_run_id);

-- +goose Down
-- Reverting: drop 'thinking' from constraint
-- Note: this will fail if there are thinking blocks

CREATE TABLE message_content_blocks_backup AS SELECT * FROM message_content_blocks WHERE block_type != 'thinking';
DROP TABLE message_content_blocks;

CREATE TABLE message_content_blocks (
    id TEXT PRIMARY KEY,
    message_id TEXT NOT NULL,
    position INTEGER NOT NULL DEFAULT 0,
    block_type TEXT NOT NULL CHECK (block_type IN ('text', 'tool_call', 'tool_result', 'image', 'file_reference')),
    content TEXT,
    tool_name TEXT,
    tool_input TEXT,
    tool_call_id TEXT,
    is_error BOOLEAN,
    version INTEGER,
    node_id TEXT,
    node_path TEXT,
    activity_id TEXT,
    workflow_run_id TEXT,
    attempt_number INTEGER DEFAULT 1,
    thought_signature TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE CASCADE
);

INSERT INTO message_content_blocks SELECT * FROM message_content_blocks_backup;
DROP TABLE message_content_blocks_backup;

CREATE INDEX idx_content_blocks_message_id ON message_content_blocks(message_id);
CREATE INDEX idx_content_blocks_tool_call_id ON message_content_blocks(tool_call_id);
CREATE INDEX idx_content_blocks_activity ON message_content_blocks(activity_id, workflow_run_id);
