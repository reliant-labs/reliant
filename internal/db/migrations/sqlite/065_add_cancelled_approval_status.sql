-- +goose Up
-- Add 'cancelled' status to approvals table for cleanup on workflow cancellation
-- When a workflow is cancelled, pending approvals should be marked as cancelled

-- SQLite doesn't support ALTER TABLE to modify CHECK constraints, so we recreate the table
CREATE TABLE approvals_new (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,

    -- Type discriminator: 'tool' for tool calls, 'workflow_step' for workflow steps
    approval_type TEXT NOT NULL CHECK (approval_type IN ('tool', 'workflow_step')),

    -- Entity reference: content_block_id for tools, event_id for workflows
    entity_id TEXT NOT NULL UNIQUE,

    -- Approval state - now includes 'cancelled' for workflow cancellation
    status TEXT NOT NULL CHECK (status IN ('pending', 'approved', 'denied', 'cancelled')),
    denial_reason TEXT,

    -- Display fields
    title TEXT NOT NULL DEFAULT '',
    description TEXT,

    -- Optional fields (JSON for flexibility)
    actions TEXT,  -- JSON array of available actions
    metadata TEXT, -- JSON object with type-specific metadata

    -- Timestamps
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resolved_at DATETIME,

    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE
);

-- Copy existing data
INSERT INTO approvals_new (id, chat_id, approval_type, entity_id, status, denial_reason, title, description, actions, metadata, created_at, resolved_at)
SELECT id, chat_id, approval_type, entity_id, status, denial_reason, title, description, actions, metadata, created_at, resolved_at
FROM approvals;

-- Drop old table and rename new one
DROP TABLE approvals;
ALTER TABLE approvals_new RENAME TO approvals;

-- Recreate indexes
CREATE INDEX idx_approvals_chat_status ON approvals(chat_id, status);
CREATE INDEX idx_approvals_entity ON approvals(entity_id);
CREATE INDEX idx_approvals_type ON approvals(approval_type, status);
CREATE INDEX idx_approvals_created ON approvals(created_at DESC);

-- +goose Down
-- Remove 'cancelled' status (revert to original constraint)
-- Note: This will fail if any approvals have status='cancelled'

CREATE TABLE approvals_old (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    approval_type TEXT NOT NULL CHECK (approval_type IN ('tool', 'workflow_step')),
    entity_id TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL CHECK (status IN ('pending', 'approved', 'denied')),
    denial_reason TEXT,
    title TEXT NOT NULL DEFAULT '',
    description TEXT,
    actions TEXT,
    metadata TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resolved_at DATETIME,
    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE
);

-- This will fail if there are cancelled approvals - intentional
INSERT INTO approvals_old (id, chat_id, approval_type, entity_id, status, denial_reason, title, description, actions, metadata, created_at, resolved_at)
SELECT id, chat_id, approval_type, entity_id, status, denial_reason, title, description, actions, metadata, created_at, resolved_at
FROM approvals
WHERE status != 'cancelled';

DROP TABLE approvals;
ALTER TABLE approvals_old RENAME TO approvals;

CREATE INDEX idx_approvals_chat_status ON approvals(chat_id, status);
CREATE INDEX idx_approvals_entity ON approvals(entity_id);
CREATE INDEX idx_approvals_type ON approvals(approval_type, status);
CREATE INDEX idx_approvals_created ON approvals(created_at DESC);
