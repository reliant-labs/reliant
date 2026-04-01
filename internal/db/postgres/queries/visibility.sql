-- name: GetVisibilityOverride :one
SELECT is_visible FROM visibility_overrides
WHERE user_id = $1
    AND item_type = $2
    AND slug = $3;

-- name: ListVisibilityOverrides :many
SELECT slug, is_visible FROM visibility_overrides
WHERE user_id = $1
    AND item_type = $2
ORDER BY slug;

-- name: SetVisibilityOverride :exec
INSERT INTO visibility_overrides (id, user_id, item_type, slug, is_visible, created_at)
VALUES ($1, $2, $3, $4, $5, NOW())
ON CONFLICT (user_id, item_type, slug) DO UPDATE SET
    is_visible = excluded.is_visible;

-- name: DeleteVisibilityOverride :exec
DELETE FROM visibility_overrides
WHERE user_id = $1
    AND item_type = $2
    AND slug = $3;

-- name: DeleteAllVisibilityOverrides :exec
DELETE FROM visibility_overrides
WHERE user_id = $1
    AND item_type = $2;

-- name: GetItemDefault :one
SELECT is_hidden, reason FROM item_defaults
WHERE item_type = $1
    AND slug = $2;

-- name: ListItemDefaults :many
SELECT slug, is_hidden, reason FROM item_defaults
WHERE item_type = $1
ORDER BY slug;

-- name: ListHiddenItemDefaults :many
SELECT slug, reason FROM item_defaults
WHERE item_type = $1
    AND is_hidden = true
ORDER BY slug;

-- name: CreateItemDefault :exec
INSERT INTO item_defaults (id, item_type, slug, is_hidden, reason, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, NOW(), NOW())
ON CONFLICT (item_type, slug) DO UPDATE SET
    is_hidden = excluded.is_hidden,
    reason = excluded.reason,
    updated_at = NOW();

-- name: GetDefaultPresetAssignments :many
SELECT group_name, preset_slug FROM default_preset_assignments
WHERE workflow_name = $1
ORDER BY group_name;

-- name: SetDefaultPresetAssignment :exec
INSERT INTO default_preset_assignments (id, workflow_name, group_name, preset_slug, created_at, updated_at)
VALUES ($1, $2, $3, $4, NOW(), NOW())
ON CONFLICT (workflow_name, group_name) DO UPDATE SET
    preset_slug = excluded.preset_slug,
    updated_at = NOW();

-- name: DeleteDefaultPresetAssignment :exec
DELETE FROM default_preset_assignments
WHERE workflow_name = $1
    AND group_name = $2;
