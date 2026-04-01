-- +goose Up
-- Add tool_approvals table for tracking tool execution approvals
-- Separate from tool_calls for clean separation of concerns

CREATE TABLE tool_approvals (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    content_block_id TEXT NOT NULL UNIQUE,  -- 1:1 relationship with tool_call content blocks
    status TEXT NOT NULL CHECK (status IN ('pending', 'approved', 'denied')),
    denial_reason TEXT,
    sequence INTEGER,  -- For WebSocket synchronization
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    responded_at DATETIME,

    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE,
    FOREIGN KEY (content_block_id) REFERENCES message_content_blocks(id) ON DELETE CASCADE
);

CREATE INDEX idx_tool_approvals_chat_status ON tool_approvals(chat_id, status);
CREATE INDEX idx_tool_approvals_content_block ON tool_approvals(content_block_id);
CREATE INDEX idx_tool_approvals_sequence ON tool_approvals(sequence);
CREATE INDEX idx_tool_approvals_chat_sequence ON tool_approvals(chat_id, sequence);

-- +goose Down
-- Remove tool_approvals table

DROP INDEX IF EXISTS idx_tool_approvals_chat_sequence;
DROP INDEX IF EXISTS idx_tool_approvals_sequence;
DROP INDEX IF EXISTS idx_tool_approvals_content_block;
DROP INDEX IF EXISTS idx_tool_approvals_chat_status;
DROP TABLE IF EXISTS tool_approvals;
