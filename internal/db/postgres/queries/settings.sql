-- name: CreateSetting :exec
-- Upsert, not a plain insert. A setting is identified by (user_id, project_id,
-- key), so writing the same key twice must REPLACE the value rather than add a
-- second row.
--
-- The table's UNIQUE (user_id, project_id, key) constraint does not achieve
-- that on its own: project_id is NULL for every user-level setting, and in SQL
-- NULL is never equal to NULL, so the constraint simply does not apply to the
-- rows we write most. Every save appended a duplicate, and ListSettings
-- (ORDER BY key) then returned them in an arbitrary order, letting a stale
-- value overwrite the current one on load. Hence the accompanying partial
-- unique index on (user_id, key) WHERE project_id IS NULL, which is what this
-- ON CONFLICT target resolves against.
INSERT INTO settings (
    id, user_id, project_id, key, value, value_type,
    created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (user_id, key) WHERE project_id IS NULL
DO UPDATE SET
    value = EXCLUDED.value,
    value_type = EXCLUDED.value_type,
    updated_at = EXCLUDED.updated_at;

-- name: CreateProjectSetting :exec
-- The project-scoped counterpart. project_id is NOT NULL here, so the table's
-- own UNIQUE (user_id, project_id, key) constraint is a usable conflict target.
INSERT INTO settings (
    id, user_id, project_id, key, value, value_type,
    created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (user_id, project_id, key)
DO UPDATE SET
    value = EXCLUDED.value,
    value_type = EXCLUDED.value_type,
    updated_at = EXCLUDED.updated_at;

-- name: GetSetting :one
SELECT * FROM settings
WHERE user_id = $1
    AND project_id IS NOT DISTINCT FROM $2
    AND key = $3;

-- name: ListSettings :many
-- ORDER BY key, updated_at: key alone is not a total order, so any duplicate
-- rows came back in an arbitrary sequence and the client — which loads rows
-- into localStorage one after another — could end up keeping a stale value.
-- The upsert above plus the partial unique index mean duplicates should no
-- longer exist; ordering by updated_at as well guarantees that if any survive,
-- the NEWEST wins rather than whichever the planner happened to emit last.
SELECT * FROM settings
WHERE user_id = $1
    AND project_id IS NOT DISTINCT FROM $2
ORDER BY key, updated_at;

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
