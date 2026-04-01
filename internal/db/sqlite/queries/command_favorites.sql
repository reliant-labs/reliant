-- name: ListCommandFavorites :many
SELECT command_key FROM command_favorites
WHERE user_id = ?
    AND project_id = ?
ORDER BY created_at;

-- name: AddCommandFavorite :exec
INSERT INTO command_favorites (id, user_id, project_id, command_key, created_at)
VALUES (?, ?, ?, ?, datetime('now', 'utc'))
ON CONFLICT (user_id, project_id, command_key) DO NOTHING;

-- name: RemoveCommandFavorite :exec
DELETE FROM command_favorites
WHERE user_id = ?
    AND project_id = ?
    AND command_key = ?;

-- name: IsCommandFavorite :one
SELECT 1 FROM command_favorites
WHERE user_id = ?
    AND project_id = ?
    AND command_key = ?
LIMIT 1;
