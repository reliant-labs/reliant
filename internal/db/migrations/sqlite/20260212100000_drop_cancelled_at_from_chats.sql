-- +goose Up
-- Remove dead cancelled_at column from chats table.
-- The old DB-polling cancellation mechanism has been replaced with
-- Temporal signal-based activity cancellation.

-- SQLite doesn't support DROP COLUMN directly, so recreate the table.

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
    selected_presets TEXT,
    archived_worktree_name TEXT,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
);

INSERT INTO chats_new (
    id, title, project_id, user_id, state, workflow_id, run_id,
    created_at, updated_at, last_active, worktree_id, workflow_name,
    selected_presets, archived_worktree_name
)
SELECT
    id, title, project_id, user_id, state, workflow_id, run_id,
    created_at, updated_at, last_active, worktree_id, workflow_name,
    selected_presets, archived_worktree_name
FROM chats;

DROP TABLE chats;
ALTER TABLE chats_new RENAME TO chats;

-- Recreate indexes (without idx_chats_cancelled_at)
CREATE INDEX idx_chats_project ON chats(project_id);
CREATE INDEX idx_chats_user_id ON chats(user_id);
CREATE INDEX idx_chats_last_active ON chats(last_active DESC);
CREATE INDEX idx_chats_state ON chats(state);
CREATE INDEX idx_chats_workflow ON chats(workflow_id);
CREATE INDEX idx_chats_archived_worktree_name ON chats(archived_worktree_name);

-- +goose Down
ALTER TABLE chats ADD COLUMN cancelled_at DATETIME;
CREATE INDEX idx_chats_cancelled_at ON chats(id, cancelled_at);
