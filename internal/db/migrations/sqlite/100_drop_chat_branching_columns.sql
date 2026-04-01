-- +goose Up
-- Remove chat-level branching columns - replaced by workflow fork mechanism
-- Context inheritance now uses workflows.forked_from_thread / forked_at_ordinal

-- SQLite doesn't support DROP COLUMN directly, so we need to recreate the table
-- First, create a new table without the branching columns

CREATE TABLE chats_new (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    project_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    state TEXT DEFAULT 'idle' CHECK (state IN ('needs_attention', 'idle', 'archived')),
    workflow_id TEXT,
    run_id TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_active TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    worktree_id TEXT REFERENCES worktrees(id) ON DELETE SET NULL,
    workflow_name TEXT,
    cancelled_at DATETIME,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
);

-- Copy data (excluding branching columns)
INSERT INTO chats_new (
    id, title, project_id, user_id, state, workflow_id, run_id,
    created_at, updated_at, last_active, worktree_id, workflow_name, cancelled_at
)
SELECT 
    id, title, project_id, user_id, state, workflow_id, run_id,
    created_at, updated_at, last_active, worktree_id, workflow_name, cancelled_at
FROM chats;

-- Drop old table and rename new one
DROP TABLE chats;
ALTER TABLE chats_new RENAME TO chats;

-- Recreate indexes
CREATE INDEX idx_chats_project ON chats(project_id);
CREATE INDEX idx_chats_user_id ON chats(user_id);
CREATE INDEX idx_chats_last_active ON chats(last_active DESC);
CREATE INDEX idx_chats_state ON chats(state);
CREATE INDEX idx_chats_workflow ON chats(workflow_id);
CREATE INDEX idx_chats_cancelled_at ON chats(id, cancelled_at);

-- +goose Down
-- Re-add branching columns (can't restore data)
CREATE TABLE chats_old (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    project_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    state TEXT DEFAULT 'idle' CHECK (state IN ('needs_attention', 'idle', 'archived')),
    branched_from_chat_id TEXT,
    branched_at_ordinal BIGINT,
    parent_context_sequence INTEGER,
    workflow_id TEXT,
    run_id TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_active TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    worktree_id TEXT REFERENCES worktrees(id) ON DELETE SET NULL,
    workflow_name TEXT,
    cancelled_at DATETIME,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    FOREIGN KEY (branched_from_chat_id) REFERENCES chats(id) ON DELETE SET NULL
);

INSERT INTO chats_old (
    id, title, project_id, user_id, state, workflow_id, run_id,
    created_at, updated_at, last_active, worktree_id, workflow_name, cancelled_at
)
SELECT 
    id, title, project_id, user_id, state, workflow_id, run_id,
    created_at, updated_at, last_active, worktree_id, workflow_name, cancelled_at
FROM chats;

DROP TABLE chats;
ALTER TABLE chats_old RENAME TO chats;

CREATE INDEX idx_chats_project ON chats(project_id);
CREATE INDEX idx_chats_user_id ON chats(user_id);
CREATE INDEX idx_chats_last_active ON chats(last_active DESC);
CREATE INDEX idx_chats_state ON chats(state);
CREATE INDEX idx_chats_branched_from ON chats(branched_from_chat_id);
CREATE INDEX idx_chats_workflow ON chats(workflow_id);
CREATE INDEX idx_chats_cancelled_at ON chats(id, cancelled_at);
