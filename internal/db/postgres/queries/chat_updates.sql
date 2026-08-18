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

-- NOTE: there is deliberately no MAX(sequence_number)+1 allocator here.
-- Sequence numbers come from the chat's row in update_stream_counters; see
-- allocateUpdateSequence in internal/db/repo.go and migration
-- 20260814130000_scoped_update_stream_counters.sql. The counter and this insert
-- share a transaction so sequence order and commit order cannot diverge.

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
