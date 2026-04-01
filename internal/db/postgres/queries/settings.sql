-- name: CreateSetting :exec
INSERT INTO settings (
    id, user_id, project_id, key, value, value_type,
    created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: GetSetting :one
SELECT * FROM settings
WHERE user_id = $1
    AND project_id IS NOT DISTINCT FROM $2
    AND key = $3;

-- name: ListSettings :many
SELECT * FROM settings
WHERE user_id = $1
    AND project_id IS NOT DISTINCT FROM $2
ORDER BY key;

-- name: ListSettingsByKey :many
SELECT * FROM settings
WHERE user_id = $1
    AND key LIKE $2
ORDER BY key;

-- name: UpdateSetting :exec
UPDATE settings SET
    value = $1,
    value_type = $2,
    updated_at = NOW()
WHERE id = $3;

-- name: DeleteSetting :exec
DELETE FROM settings WHERE id = $1;

-- name: DeleteSettingByKey :exec
DELETE FROM settings
WHERE user_id = $1
    AND project_id IS NOT DISTINCT FROM $2
    AND key = $3;
