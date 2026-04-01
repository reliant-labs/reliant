-- name: CreateContentBlock :exec
INSERT INTO message_content_blocks (
    id, message_id, position, block_type, content,
    tool_name, tool_input, tool_call_id, thought_signature, is_error,
    version, activity_id, workflow_run_id, attempt_number,
    created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: CreateContentBlockIfNotExists :exec
INSERT OR IGNORE INTO message_content_blocks (
    id, message_id, position, block_type, content,
    tool_name, tool_input, tool_call_id, thought_signature, is_error,
    version, activity_id, workflow_run_id, attempt_number,
    created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetContentBlock :one
SELECT * FROM message_content_blocks WHERE id = ?;

-- name: ListContentBlocks :many
SELECT * FROM message_content_blocks
WHERE message_id = ?
ORDER BY position ASC;

-- name: UpdateContentBlock :exec
UPDATE message_content_blocks SET
    content = ?,
    tool_input = ?,
    version = ?,
    updated_at = datetime('now', 'utc')
WHERE id = ?;

-- name: AppendToContentBlock :exec
UPDATE message_content_blocks
SET content = COALESCE(content, '') || ?,
    version = version + 1,
    updated_at = datetime('now', 'utc')
WHERE id = ?;
