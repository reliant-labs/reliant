-- +goose Up
-- Position checkpoint for workflow runs: records the last top-level node the
-- run entered (and the loop iteration for loop nodes) so a run that fails or
-- is terminated can be resumed at position by the next user message instead of
-- restarting at graph entry. One row per workflow ID (workflow IDs are reused
-- across Temporal runs for the same chat), upserted at node-entry/iteration
-- boundaries and deleted on completed/cancelled.
CREATE TABLE IF NOT EXISTS workflow_checkpoints (
    workflow_id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    loop_iteration BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMP NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_workflow_checkpoints_chat_id ON workflow_checkpoints(chat_id);

-- +goose Down
DROP TABLE IF EXISTS workflow_checkpoints;
