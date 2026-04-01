-- name: CreateChatUpdate :exec
INSERT INTO chat_updates (
    id,
    chat_id,
    sequence_number,
    update_type,
    entity_id,
    data,
    created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: GetNextSequenceNumber :one
SELECT COALESCE(MAX(sequence_number), 0) + 1
FROM chat_updates
WHERE chat_id = $1;

-- name: GetChatUpdates :many
SELECT *
FROM chat_updates
WHERE chat_id = $1
  AND sequence_number > $2
ORDER BY sequence_number ASC
LIMIT $3;

-- name: GetChatUpdatesSince :many
SELECT *
FROM chat_updates
WHERE chat_id = $1
  AND sequence_number > $2
ORDER BY sequence_number ASC;