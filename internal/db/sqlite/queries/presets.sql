-- name: CreatePreset :one
INSERT INTO presets (
    id, user_id, project_id, name, slug, description, tag, params, created_at, updated_at
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, datetime('now', 'utc'), datetime('now', 'utc')
)
RETURNING *;

-- name: GetPreset :one
SELECT * FROM presets WHERE id = ?;

-- name: GetPresetBySlug :one
-- Lookup by user and slug (global scope when project_id IS NULL)
SELECT * FROM presets 
WHERE user_id = ? AND slug = ? AND project_id IS NULL;

-- name: GetPresetBySlugAndProject :one
-- Lookup by user, slug, and project scope
SELECT * FROM presets 
WHERE user_id = ? AND slug = ? AND project_id = ?;

-- name: ListUserPresets :many
-- List all presets for a user (both global and project-specific)
SELECT * FROM presets 
WHERE user_id = ?
ORDER BY name ASC;

-- name: ListUserPresetsGlobal :many
-- List only global (user-wide) presets
SELECT * FROM presets 
WHERE user_id = ? AND project_id IS NULL
ORDER BY name ASC;

-- name: ListUserPresetsByProject :many
-- List presets for a specific project (includes both global and project-specific)
SELECT * FROM presets 
WHERE user_id = ? AND (project_id IS NULL OR project_id = ?)
ORDER BY name ASC;

-- name: ListPresetsByTag :many
-- List presets by tag for a user
SELECT * FROM presets 
WHERE user_id = ? AND tag = ? AND (project_id IS NULL OR project_id = ?)
ORDER BY name ASC;

-- name: UpdatePreset :one
UPDATE presets SET
    name = ?,
    description = ?,
    tag = ?,
    params = ?,
    updated_at = datetime('now', 'utc')
WHERE id = ?
RETURNING *;

-- name: DeletePreset :exec
DELETE FROM presets WHERE id = ?;

-- name: DeletePresetBySlug :exec
-- Delete by user and slug (global scope)
DELETE FROM presets 
WHERE user_id = ? AND slug = ? AND project_id IS NULL;

-- name: DeletePresetBySlugAndProject :exec
-- Delete by user, slug, and project scope
DELETE FROM presets 
WHERE user_id = ? AND slug = ? AND project_id = ?;

-- name: UpsertPreset :one
-- Insert or update a preset
INSERT INTO presets (
    id, user_id, project_id, name, slug, description, tag, params, created_at, updated_at
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, datetime('now', 'utc'), datetime('now', 'utc')
)
ON CONFLICT(user_id, project_id, slug) DO UPDATE SET
    name = excluded.name,
    description = excluded.description,
    tag = excluded.tag,
    params = excluded.params,
    updated_at = datetime('now', 'utc')
RETURNING *;
