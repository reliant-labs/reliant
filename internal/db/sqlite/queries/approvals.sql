-- name: CreateApproval :exec
-- Create a new approval record (idempotent via UNIQUE constraint on entity_id)
INSERT INTO approvals (
    id, chat_id, approval_type, entity_id, status, denial_reason,
    title, metadata, temporal_workflow_id, created_at, resolved_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(entity_id) DO NOTHING;

-- name: GetApproval :one
-- Get a specific approval by ID
SELECT * FROM approvals WHERE id = ?;

-- name: GetApprovalByEntityID :one
-- Get an approval by its entity_id (content_block_id or event_id)
SELECT * FROM approvals WHERE entity_id = ?;

-- name: ListPendingApprovalsByChat :many
-- List all pending approvals for a chat
SELECT * FROM approvals
WHERE chat_id = ? AND status = 1
ORDER BY created_at ASC;

-- name: ListApprovalsByChat :many
-- List all approvals for a chat (including resolved)
SELECT * FROM approvals
WHERE chat_id = ?
ORDER BY created_at DESC;

-- name: UpdateApprovalStatus :exec
-- Update the status of an approval
UPDATE approvals SET
    status = ?,
    denial_reason = ?,
    action_taken = ?,
    resolved_at = ?,
    metadata = COALESCE(?, metadata)
WHERE id = ?;

