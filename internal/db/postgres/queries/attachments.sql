-- name: CreateAttachment :exec
INSERT INTO attachments (
    id,
    user_id,
    filename,
    size,
    mime_type,
    file_hash,
    file_path,
    attachment_type,
    content,
    created_at,
    updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
);

-- name: GetAttachment :one
SELECT * FROM attachments
WHERE id = $1 LIMIT 1;

-- name: GetAttachmentsByIDs :many
SELECT * FROM attachments
WHERE id IN (sqlc.slice('ids'));

-- name: ListAttachmentsByUser :many
SELECT * FROM attachments
WHERE user_id = $1
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;

-- name: DeleteAttachment :exec
DELETE FROM attachments
WHERE id = $1;