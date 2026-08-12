-- name: CreateApproval :exec
-- Create a new approval record (idempotent via UNIQUE constraint on entity_id)
INSERT INTO approvals (
    id, chat_id, approval_type, entity_id, status, denial_reason,
    title, metadata, temporal_workflow_id, created_at, resolved_at, thread_id
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT(entity_id) DO NOTHING;

-- name: GetApproval :one
-- Get a specific approval by ID
SELECT * FROM approvals WHERE id = $1;

-- name: GetApprovalByEntityID :one
-- Get an approval by its entity_id (content_block_id or event_id)
SELECT * FROM approvals WHERE entity_id = $1;

-- name: ListPendingApprovalsByChat :many
-- List all pending approvals for a chat
SELECT * FROM approvals
WHERE chat_id = $1 AND status = 1
ORDER BY created_at ASC;

-- name: ListApprovalsByChat :many
-- List all approvals for a chat (including resolved)
SELECT * FROM approvals
WHERE chat_id = $1
ORDER BY created_at DESC;

-- name: UpdateApprovalStatus :exec
-- Update the status of an approval
UPDATE approvals SET
    status = $1,
    denial_reason = $2,
    action_taken = $3,
    resolved_at = $4,
    metadata = COALESCE($5, metadata)
WHERE id = $6;

