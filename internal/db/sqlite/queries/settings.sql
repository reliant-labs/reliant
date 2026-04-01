-- name: CreateSetting :exec
INSERT INTO settings (
    id, user_id, project_id, key, value, value_type,
    created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetSetting :one
SELECT * FROM settings
WHERE user_id = ?
    AND (project_id IS ? OR (project_id IS NULL AND ? IS NULL))
    AND key = ?;

-- name: ListSettings :many
SELECT * FROM settings
WHERE user_id = ?
    AND (project_id IS ? OR (project_id IS NULL AND ? IS NULL))
ORDER BY key;

-- name: ListSettingsByKey :many
SELECT * FROM settings
WHERE user_id = ?
    AND key LIKE ?
ORDER BY key;

-- name: UpdateSetting :exec
UPDATE settings SET
    value = ?,
    value_type = ?,
    updated_at = datetime('now', 'utc')
WHERE id = ?;

-- name: DeleteSetting :exec
DELETE FROM settings WHERE id = ?;

-- name: DeleteSettingByKey :exec
DELETE FROM settings
WHERE user_id = ?
    AND (project_id IS ? OR (project_id IS NULL AND ? IS NULL))
    AND key = ?;
