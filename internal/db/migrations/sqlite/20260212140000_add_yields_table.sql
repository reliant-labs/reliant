-- +goose Up
-- Add yields table for interactive loop pause/resume state
-- Includes thread_id so replies are written to the correct thread/fork.

CREATE TABLE IF NOT EXISTS yields (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    workflow_id TEXT NOT NULL,
    thread_id TEXT NOT NULL,
    step_id TEXT NOT NULL,
    loop_node_id TEXT,
    loop_iteration INTEGER,
    status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending', 'resolved')),
    action_taken TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resolved_at DATETIME
);

CREATE INDEX IF NOT EXISTS idx_yields_chat_id_status ON yields(chat_id, status);
CREATE INDEX IF NOT EXISTS idx_yields_workflow_step ON yields(workflow_id, step_id, status);

-- +goose Down
DROP INDEX IF EXISTS idx_yields_workflow_step;
DROP INDEX IF EXISTS idx_yields_chat_id_status;
DROP TABLE IF EXISTS yields;
