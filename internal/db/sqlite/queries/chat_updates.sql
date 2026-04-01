-- name: CreateChatUpdate :exec
INSERT INTO chat_updates (
    id,
    chat_id,
    sequence_number,
    update_type,
    entity_id,
    data,
    created_at
) VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: GetNextSequenceNumber :one
SELECT COALESCE(MAX(sequence_number), 0) + 1
FROM chat_updates
WHERE chat_id = ?;

-- name: GetChatUpdates :many
SELECT *
FROM chat_updates
WHERE chat_id = ?
  AND sequence_number > ?
ORDER BY sequence_number ASC
LIMIT ?;

-- name: GetChatUpdatesSince :many
SELECT *
FROM chat_updates
WHERE chat_id = ?
  AND sequence_number > ?
ORDER BY sequence_number ASC;
