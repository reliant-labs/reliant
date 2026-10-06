-- name: CreateChat :exec
INSERT INTO chats (
    id, user_id, title, project_id, worktree_id,
    workflow_name, state, workflow_id, run_id, selected_presets, created_at, updated_at, last_active,
    active_daemon_id, no_machine
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15);

-- name: GetChat :one
SELECT * FROM chats_with_activity WHERE id = $1;

-- name: ListChats :many
SELECT * FROM chats_with_activity
WHERE
    user_id = sqlc.arg('user_id')
    AND (sqlc.narg('project_id')::text IS NULL OR project_id = sqlc.narg('project_id')::text)
    AND (sqlc.narg('state')::integer IS NULL OR state = sqlc.narg('state')::integer)
    AND (NOT sqlc.arg('exclude_archived')::boolean OR state != 3)
    -- sidebar_only keeps chats the sidebar lists. The policy lives in ONE place,
    -- the chats_with_activity.list_in_sidebar view column.
    AND (NOT sqlc.arg('sidebar_only')::boolean OR list_in_sidebar)
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

-- name: UpdateChatActiveDaemon :exec
-- Pinning a daemon puts the chat on a machine, so it ends no-machine in the same
-- write (chats_no_machine_has_no_daemon_check). Clearing the daemon never sets
-- no_machine: a chat that has had a machine does not become one without.
UPDATE chats SET
    active_daemon_id = sqlc.narg('active_daemon_id'),
    no_machine = no_machine AND sqlc.narg('active_daemon_id')::text IS NULL,
    updated_at = NOW()
WHERE id = sqlc.arg('id');

-- name: DeleteChat :exec
DELETE FROM chats WHERE id = $1;

-- name: SearchChats :many
SELECT DISTINCT cws.*
FROM chats_with_activity cws
LEFT JOIN messages m ON cws.id = m.chat_id
LEFT JOIN message_content_blocks mcb ON m.id = mcb.message_id AND mcb.block_type = 1
WHERE
    cws.user_id = sqlc.arg('user_id')
    AND cws.project_id = sqlc.arg('project_id')
    -- The cast is load-bearing. `$3 IS NULL` on a bare parameter is a
    -- PARSE-time error in Postgres ("could not determine data type of
    -- parameter $3", 42P18) for every input, null or not, so this query
    -- failed unconditionally. ListChats above already casts for this reason.
    AND (sqlc.narg('state')::integer IS NULL OR cws.state = sqlc.narg('state')::integer)
    AND (
        cws.title LIKE sqlc.arg('title')
        OR mcb.content LIKE sqlc.arg('content')
    )
ORDER BY cws.last_active DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: ListArchivedChats :many
-- List archived chats with worktree info. Reads chats_with_activity (not chats)
-- so the root workflow state and last_message_at come back like every other
-- chat read; falls back to project name if worktree name is unavailable.
SELECT
    c.*,
    COALESCE(w.name, c.archived_worktree_name, p.name) as worktree_name,
    w.deleted_at as worktree_deleted_at
FROM chats_with_activity c
LEFT JOIN worktrees w ON c.worktree_id = w.id
LEFT JOIN projects p ON c.project_id = p.id
WHERE c.state = 3
  AND c.user_id = @user_id
ORDER BY c.updated_at DESC;
-- name: SetChatAdoptedAt :execrows
-- Idempotent: adopting an adopted chat keeps its original adopted_at, and
-- clearing an un-adopted one is a no-op. Scoped to the owner; zero rows means
-- the chat is not the caller's (or does not exist).
UPDATE chats SET
    adopted_at = CASE
        WHEN sqlc.arg('adopted')::boolean THEN COALESCE(adopted_at, NOW())
        ELSE NULL
    END,
    updated_at = NOW()
WHERE id = sqlc.arg('id') AND user_id = sqlc.arg('user_id');

-- name: SetChatDaemonBlocked :execrows
-- Sets or clears the daemon-pending marker. Returns 1 only when the value
-- actually changed, so callers emit chat_activity_changed on transitions and
-- not on every tool call.
UPDATE chats SET
    daemon_blocked_at = CASE WHEN sqlc.arg('blocked')::boolean THEN NOW() ELSE NULL END
WHERE id = sqlc.arg('id')
  AND (daemon_blocked_at IS NOT NULL) IS DISTINCT FROM sqlc.arg('blocked')::boolean;
