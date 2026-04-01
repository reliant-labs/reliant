-- Migration: Add 'image' to allowed block_type values
-- This allows storing image attachments as content blocks in messages

-- +goose Up

-- SQLite doesn't support ALTER COLUMN to modify CHECK constraints
-- We need to recreate the table with the new constraint

-- First, save existing data
CREATE TABLE message_content_blocks_backup AS SELECT * FROM message_content_blocks;

-- Drop old table
DROP TABLE message_content_blocks;

-- Recreate with updated CHECK constraint that includes 'image'
CREATE TABLE message_content_blocks (
    id TEXT PRIMARY KEY,
    message_id TEXT NOT NULL,
    position INTEGER NOT NULL DEFAULT 0,

    -- Block type and content (now includes 'image')
    block_type TEXT NOT NULL CHECK (block_type IN ('text', 'tool_call', 'tool_result', 'image')),
    content TEXT,

    -- Tool-specific fields
    tool_name TEXT,
    tool_input TEXT,
    tool_call_id TEXT,
    is_error BOOLEAN,

    -- Versioning
    version INTEGER,

    -- Workflow context
    node_id TEXT,
    node_path TEXT,

    -- Activity tracking (for idempotency)
    activity_id TEXT,
    workflow_run_id TEXT,
    attempt_number INTEGER DEFAULT 1,

    -- Gemini 2.x thought signature support
    thought_signature TEXT,

    -- Metadata
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,

    FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE CASCADE
);

-- Restore data
INSERT INTO message_content_blocks SELECT * FROM message_content_blocks_backup;

-- Drop backup
DROP TABLE message_content_blocks_backup;

-- Recreate indexes
CREATE INDEX idx_content_blocks_message ON message_content_blocks(message_id, position);
CREATE INDEX idx_content_blocks_tool_call ON message_content_blocks(tool_call_id);
CREATE INDEX idx_content_blocks_activity ON message_content_blocks(activity_id);

-- +goose Down

-- Revert back to old constraint (note: this will fail if there are image blocks)
CREATE TABLE message_content_blocks_backup AS SELECT * FROM message_content_blocks WHERE block_type != 'image';
DROP TABLE message_content_blocks;

CREATE TABLE message_content_blocks (
    id TEXT PRIMARY KEY,
    message_id TEXT NOT NULL,
    position INTEGER NOT NULL DEFAULT 0,
    block_type TEXT NOT NULL CHECK (block_type IN ('text', 'tool_call', 'tool_result')),
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

CREATE INDEX idx_content_blocks_message ON message_content_blocks(message_id, position);
CREATE INDEX idx_content_blocks_tool_call ON message_content_blocks(tool_call_id);
CREATE INDEX idx_content_blocks_activity ON message_content_blocks(activity_id);
