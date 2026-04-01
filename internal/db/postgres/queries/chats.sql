-- name: CreateChat :exec
INSERT INTO chats (
    id, user_id, title, project_id, worktree_id,
    workflow_name, state, workflow_id, run_id, selected_presets, created_at, updated_at, last_active
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13);

-- name: GetChat :one
SELECT * FROM chats_with_activity WHERE id = $1;

-- name: ListChats :many
SELECT * FROM chats_with_activity
WHERE
    user_id = sqlc.arg('user_id')
    AND project_id = sqlc.narg('project_id')
    AND (sqlc.narg('state')::integer IS NULL OR state = sqlc.narg('state')::integer)
    AND (NOT sqlc.arg('exclude_archived')::boolean OR state != 3)
ORDER BY last_active DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: GetChatWithUserCheck :one
SELECT * FROM chats_with_activity WHERE id = $1 AND user_id = $2;

-- name: UpdateChat :exec
UPDATE chats SET
    title = $1,
    project_id = $2,
    worktree_id = $3,
    workflow_name = $4,
    state = $5,
    workflow_id = $6,
    run_id = $7,
    selected_presets = $8,
    last_active = $9,
    updated_at = NOW()
WHERE id = $10;

-- name: UpdateChatTitle :exec
UPDATE chats SET
    title = $1,
    updated_at = NOW()
WHERE id = $2;

-- name: UpdateChatSelectedPresets :exec
UPDATE chats SET
    selected_presets = $1,
    updated_at = NOW()
WHERE id = $2;

-- name: DeleteChat :exec
DELETE FROM chats WHERE id = $1;

-- name: SearchChats :many
SELECT DISTINCT cws.*
FROM chats_with_activity cws
LEFT JOIN messages m ON cws.id = m.chat_id
LEFT JOIN message_content_blocks mcb ON m.id = mcb.message_id AND mcb.block_type = 1
WHERE
    cws.user_id = $1
    AND cws.project_id = $2
    AND ($3 IS NULL OR cws.state = $4)
    AND (
        cws.title LIKE $5
        OR mcb.content LIKE $6
    )
ORDER BY cws.last_active DESC
LIMIT $7 OFFSET $8;

-- name: ListArchivedChats :many
-- List archived chats with worktree info and computed last_message_at
-- Falls back to project name if worktree name is unavailable
SELECT
    c.*,
    (SELECT MAX(m.created_at) FROM messages m WHERE m.chat_id = c.id) as last_message_at,
    COALESCE(w.name, c.archived_worktree_name, p.name) as worktree_name,
    w.deleted_at as worktree_deleted_at
FROM chats c
LEFT JOIN worktrees w ON c.worktree_id = w.id
LEFT JOIN projects p ON c.project_id = p.id
WHERE c.state = 3
  AND c.user_id = @user_id
ORDER BY c.updated_at DESC;