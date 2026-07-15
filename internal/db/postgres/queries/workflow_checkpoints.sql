-- Workflow position checkpoints (resume-at-position support).
-- One row per workflow ID. Written at cheap boundaries: top-level node entry
-- and per loop iteration for top-level loop nodes.

-- name: UpsertWorkflowCheckpoint :exec
INSERT INTO workflow_checkpoints (workflow_id, chat_id, node_id, loop_iteration, updated_at)
VALUES ($1, $2, $3, $4, NOW())
ON CONFLICT (workflow_id) DO UPDATE SET
    chat_id = EXCLUDED.chat_id,
    node_id = EXCLUDED.node_id,
    loop_iteration = EXCLUDED.loop_iteration,
    updated_at = NOW();

-- name: GetWorkflowCheckpoint :one
SELECT * FROM workflow_checkpoints WHERE workflow_id = $1;

-- name: DeleteWorkflowCheckpoint :exec
DELETE FROM workflow_checkpoints WHERE workflow_id = $1;
