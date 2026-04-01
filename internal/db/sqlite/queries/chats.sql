-- name: CreateChat :exec
INSERT INTO chats (
    id, user_id, title, project_id, worktree_id,
    workflow_name, state, workflow_id, run_id, selected_presets, created_at, updated_at, last_active
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetChat :one
SELECT * FROM chats_with_activity WHERE id = ?;

-- name: ListChats :many
SELECT * FROM chats_with_activity
WHERE
    user_id = sqlc.arg('user_id')
    AND project_id = sqlc.narg('project_id')
    AND (sqlc.narg('state') IS NULL OR state = sqlc.narg('state'))
    AND (sqlc.narg('exclude_archived') IS NULL OR state != 3)
ORDER BY last_active DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: GetChatWithUserCheck :one
SELECT * FROM chats_with_activity WHERE id = ? AND user_id = ?;

-- name: UpdateChat :exec
UPDATE chats SET
    title = ?,
    project_id = ?,
    worktree_id = ?,
    workflow_name = ?,
    state = ?,
    workflow_id = ?,
    run_id = ?,
    selected_presets = ?,
    last_active = ?,
    updated_at = datetime('now', 'utc')
WHERE id = ?;

-- name: UpdateChatTitle :exec
UPDATE chats SET
    title = ?,
    updated_at = datetime('now', 'utc')
WHERE id = ?;

-- name: UpdateChatSelectedPresets :exec
UPDATE chats SET
    selected_presets = ?,
    updated_at = datetime('now', 'utc')
WHERE id = ?;

-- name: DeleteChat :exec
DELETE FROM chats WHERE id = ?;

-- name: SearchChats :many
SELECT DISTINCT cws.*
FROM chats_with_activity cws
LEFT JOIN messages m ON cws.id = m.chat_id
LEFT JOIN message_content_blocks mcb ON m.id = mcb.message_id AND mcb.block_type = 1
WHERE
    cws.user_id = ?
    AND cws.project_id = ?
    AND (? IS NULL OR cws.state = ?)
    AND (
        cws.title LIKE ?
        OR mcb.content LIKE ?
    )
ORDER BY cws.last_active DESC
LIMIT ? OFFSET ?;

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