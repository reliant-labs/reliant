-- name: CreateContentBlock :exec
INSERT INTO message_content_blocks (
    id, message_id, position, block_type, content,
    tool_name, tool_input, tool_call_id, thought_signature, is_error,
    version, activity_id, workflow_run_id, attempt_number,
    created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16);

-- name: CreateContentBlockIfNotExists :exec
INSERT INTO message_content_blocks (
    id, message_id, position, block_type, content,
    tool_name, tool_input, tool_call_id, thought_signature, is_error,
    version, activity_id, workflow_run_id, attempt_number,
    created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
ON CONFLICT(id) DO NOTHING;

-- name: GetContentBlock :one
SELECT * FROM message_content_blocks WHERE id = $1;

-- name: ListContentBlocks :many
SELECT * FROM message_content_blocks
WHERE message_id = $1
ORDER BY position ASC;

-- name: UpdateContentBlock :exec
UPDATE message_content_blocks SET
    content = $1,
    tool_input = $2,
    version = $3,
    updated_at = NOW()
WHERE id = $4;

-- name: ListContentBlocksForMessages :many
SELECT * FROM message_content_blocks
WHERE message_id IN (sqlc.slice('message_ids'))
ORDER BY message_id, position ASC;

-- name: AppendToContentBlock :exec
UPDATE message_content_blocks
SET content = COALESCE(content, '') || $1,
    version = version + 1,
    updated_at = NOW()
WHERE id = $2;