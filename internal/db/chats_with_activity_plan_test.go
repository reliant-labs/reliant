// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The chat reads go through chats_with_activity, whose per-chat facts (a live
// gate, a running or stopped workflow, the launch event, the newest message)
// must each be an index probe of THAT chat's rows. In prod the view's EXISTS
// subqueries were planned as hashed subplans and its launch-event join as a
// hash join: each built from every user's rows (a Seq Scan of all workflows,
// three times, and of all trigger_events) on every ListChats, which made the
// list's cost grow with the whole database instead of with the page.
//
// chatListPlanSeed is a database where those plans are what the planner
// picks: one user with enough sidebar chats that a 300-row page makes hashing
// look cheaper than probing, beside another user whose rows dwarf theirs.
const chatListPlanSeed = `
INSERT INTO projects (id, name, path, user_id, is_git_repo, created_at, updated_at, last_active, is_forge)
SELECT 'plan-p-' || u || '-' || k, 'p', '/plan/' || u || '/' || k, 'plan-u' || u, true, now(), now(), now(), false
FROM generate_series(0, 1) u, generate_series(0, 2) k;

INSERT INTO chats (id, title, project_id, user_id, state, workflow_id, created_at, updated_at, last_active, unread, no_machine, origin)
SELECT 'plan-c-' || u || '-' || n, 'chat', 'plan-p-' || u || '-' || (n % 3), 'plan-u' || u,
       CASE WHEN n % 5 = 0 THEN 3 ELSE 1 END, 'plan-w-' || u || '-' || n || '-0',
       now(), now(), now() - (n || ' minutes')::interval, 0, false, CASE WHEN n % 4 = 0 THEN 2 ELSE 1 END
FROM generate_series(0, 1) u, LATERAL generate_series(0, CASE WHEN u = 0 THEN 1199 ELSE 5999 END) n;

INSERT INTO workflows (id, parent_id, chat_id, workflow_name, thread, created_at, completed_at, state, stop_reason, owner_user_id)
SELECT c.workflow_id || w, CASE WHEN w = 0 THEN NULL ELSE c.workflow_id || '0' END, c.id, 'default', c.id || '-t' || w,
       now(), CASE WHEN w = 1 AND c.id LIKE '%7' THEN NULL ELSE now() END,
       CASE WHEN w = 1 AND c.id LIKE '%7' THEN 2 ELSE 3 END,
       CASE WHEN w = 1 AND c.id LIKE '%7' THEN 0 ELSE 1 + w END, c.user_id
FROM (SELECT id, user_id, left(workflow_id, length(workflow_id) - 1) AS workflow_id FROM chats WHERE id LIKE 'plan-c-%') c,
     generate_series(0, 3) w;

INSERT INTO trigger_events (id, user_id, kind, dedupe_key, occurred_at, payload, outcome, outcome_detail, chat_id, created_at, capacity_released)
SELECT 'plan-te-' || id, user_id, 'chat.start', id, now(), '{}', 'launched', '', id, now(), false
FROM chats WHERE id LIKE 'plan-c-%';

INSERT INTO threads (id, chat_id, created_at, origin, status)
SELECT id || '-t0', id, now(), 'main', 1 FROM chats WHERE id LIKE 'plan-c-%';

INSERT INTO messages (id, chat_id, ordinal, thread_id, context_window_id, role, created_at, updated_at, seq, sender_kind)
SELECT c.id || '-m' || i, c.id, i, c.id || '-t0', c.id || '-cw', 1 + i % 2, now(), now(), i, 1
FROM chats c, generate_series(0, 4) i WHERE c.id LIKE 'plan-c-%';

INSERT INTO approvals (id, chat_id, approval_type, entity_id, status, title, created_at, temporal_workflow_id)
SELECT 'plan-a-' || id, id, 1, 'plan-e-' || id, 1, 'approve?', now(), workflow_id
FROM chats WHERE id LIKE 'plan-c-%' AND id LIKE '%3';

INSERT INTO questions (id, chat_id, workflow_id, temporal_workflow_id, thread_id, step_id, status, created_at, async)
SELECT 'plan-q-' || id, id, workflow_id, workflow_id, id || '-t0', 's', 1, now(), false
FROM chats WHERE id LIKE 'plan-c-%' AND id LIKE '%9';

ANALYZE projects, chats, workflows, trigger_events, approvals, questions, messages, provider_backoff;
`

// chatListStatements are the chat list reads as the store issues them: the
// sidebar's one project (store.ListChats, LIMIT 100) and all projects (LIMIT
// 300), the archived list (store.ListArchivedChats) and one chat by id.
var chatListStatements = map[string]string{
	"list one project":  `SELECT * FROM chats_with_activity WHERE (user_id = $1) AND (project_id = 'plan-p-0-1') AND (state != 3) AND (list_in_sidebar = TRUE) ORDER BY last_active DESC LIMIT 100`,
	"list all projects": `SELECT * FROM chats_with_activity WHERE (user_id = $1) AND (state != 3) AND (list_in_sidebar = TRUE) ORDER BY last_active DESC LIMIT 300`,
	"archived": `SELECT c.*, COALESCE(w.name, c.archived_worktree_name, p.name) AS worktree_name
FROM chats_with_activity c LEFT JOIN worktrees w ON c.worktree_id = w.id LEFT JOIN projects p ON c.project_id = p.id
WHERE c.state = 3 AND c.user_id = $1 AND c.list_in_sidebar ORDER BY c.updated_at DESC`,
	"get": `SELECT * FROM chats_with_activity WHERE id = 'plan-c-0-1' AND user_id = $1`,
}

// TestChatListReadsProbePerChat asserts, under both the custom plan and the
// generic one a prepared statement settles on, that no chat read hashes or
// scans a whole table to answer a per-chat question.
func TestChatListReadsProbePerChat(t *testing.T) {
	t.Parallel()

	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	ctx := context.Background()
	_, err := raw.ExecContext(ctx, chatListPlanSeed)
	require.NoError(t, err)

	for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
		for name, stmt := range chatListStatements {
			t.Run(mode+"/"+name, func(t *testing.T) {
				plan := explainChatRead(t, ctx, raw, mode, stmt)
				for _, line := range strings.Split(plan, "\n") {
					// The root workflow is one primary-key row per chat; on a
					// seed this small the planner may hash it, which prod's
					// workflows-to-chats ratio rules out. Every other workflows
					// read is a per-chat fact.
					if strings.Contains(line, "Seq Scan on workflows rw ") {
						continue
					}
					for _, forbidden := range []string{
						"hashed SubPlan",
						"Seq Scan on workflows",
						"Seq Scan on trigger_events",
						"Seq Scan on approvals",
						"Seq Scan on questions",
						"Seq Scan on messages",
					} {
						require.NotContains(t, line, forbidden,
							"a chat read must answer each chat's facts by probing that chat's rows; %q builds from every user's rows. Plan:\n%s",
							forbidden, plan)
					}
				}
			})
		}
	}
}

// explainChatRead prepares stmt (with $1 the seeded user) on a connection of
// its own under plan_cache_mode = mode, and returns EXPLAIN EXECUTE's plan.
func explainChatRead(t *testing.T, ctx context.Context, raw *sql.DB, mode, stmt string) string {
	t.Helper()
	conn, err := raw.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.ExecContext(ctx, `SET plan_cache_mode = `+mode)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, `PREPARE chat_read(text) AS `+stmt)
	require.NoError(t, err)
	defer func() { _, _ = conn.ExecContext(ctx, `DEALLOCATE chat_read`) }()
	plan := explainLines(t, ctx, conn, `EXPLAIN EXECUTE chat_read('plan-u0')`)
	require.True(t, strings.Contains(plan, "chats"), "explained no chat read:\n%s", plan)
	return plan
}
