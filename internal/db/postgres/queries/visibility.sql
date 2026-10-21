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
