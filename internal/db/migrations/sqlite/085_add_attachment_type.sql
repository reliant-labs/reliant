-- Migration: Add attachment_type column to distinguish images from file references
-- 
-- attachment_type values:
--   - 'image': Binary image file uploaded to server (JPEG, PNG, GIF, WebP)
--   - 'file_reference': Path to local file, content read at send time (text files)

-- +goose Up
ALTER TABLE attachments ADD COLUMN attachment_type TEXT NOT NULL DEFAULT 'image'
    CHECK (attachment_type IN ('image', 'file_reference'));

-- SQLite doesn't support ALTER CHECK constraint, so we need to recreate the table
-- to add 'file_reference' as a valid block_type. However, this is complex.
-- Instead, we'll handle it at the application level - the CHECK constraint
-- allows 'image' which we use for binary images. For file references, we'll
-- store the path in 'content' with block_type='file_reference'.
-- 
-- We need to drop and recreate the constraint. SQLite requires table recreation.

-- Create new table with updated constraint (must match existing columns exactly)
CREATE TABLE message_content_blocks_new (
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
-- Reverting is complex; for safety we leave the expanded constraint
-- The old code will still work as 'file_reference' just won't be used
