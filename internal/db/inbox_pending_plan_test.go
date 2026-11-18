// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/store"
)

// inboxPendingPlanSeed is one user who owns every chat in the database, as
// the heaviest prod user did (reliant#2736: 42,670 of 42,773): 3,000 runs an
// automation launched, two of them blocked on an unreachable machine and one
// paused. No index on user_id narrows anything for them.
const inboxPendingPlanSeed = `
INSERT INTO projects (id, name, path, user_id, is_git_repo, created_at, updated_at, last_active, is_forge)
VALUES ('ip-p', 'p', '/ip', 'ip-u', true, now(), now(), now(), false);

INSERT INTO triggers (id, user_id, project_id, name, kind, enabled, workflow, presets, params, message, config, created_at, updated_at, notify_on_complete, acting_identity)
VALUES ('ip-t', 'ip-u', 'ip-p', 'trigger', 'schedule', true, 'wf', '[]', '{}', '', '{}', now(), now(), false, 'owner');

INSERT INTO chats (id, title, project_id, user_id, state, workflow_id, created_at, updated_at, last_active, unread, no_machine, daemon_blocked_at)
SELECT 'ip-c' || n, 'run', 'ip-p', 'ip-u', 1, 'ip-c' || n, now(), now(), now(), 0, false,
       CASE WHEN n IN (7, 11) THEN now() END
FROM generate_series(0, 2999) n;

INSERT INTO workflows (id, chat_id, workflow_name, thread, created_at, completed_at, state, stop_reason, owner_user_id)
SELECT id, id, 'wf', id, now(), CASE WHEN id IN ('ip-c7', 'ip-c11') THEN NULL ELSE now() END,
       CASE WHEN id IN ('ip-c7', 'ip-c11') THEN 2 ELSE 3 END,
       CASE WHEN id IN ('ip-c7', 'ip-c11') THEN 0 WHEN id = 'ip-c5' THEN 3 ELSE 1 END, user_id
FROM chats WHERE id LIKE 'ip-c%';

INSERT INTO trigger_events (id, trigger_id, user_id, kind, dedupe_key, occurred_at, payload, outcome, outcome_detail, chat_id, created_at, capacity_released, run_status)
SELECT 'ip-e-' || id, 'ip-t', user_id, 'schedule', 'ip-' || id, now(), '{}', 'launched', '', id, now(), false, 'completed'
FROM chats WHERE id LIKE 'ip-c%';

ANALYZE projects, triggers, chats, workflows, trigger_events;
`

// The Inbox's pending read (store.ListInboxPending) runs on every Inbox
// refresh. Its waiting-for-machine branch must find the blocked chats from
// idx_chats_daemon_blocked and its paused-run branch from the paused root
// runs, never by walking the caller's chats: in prod (reliant#2736) the
// first was a Seq Scan of every chat in the database and the second computed
// the launch kind and display state of each of the user's 2,000 automation
// runs, 85ms and 60ms a read.
func TestInboxPendingReadFindsBlockedAndPausedRunsWithoutWalkingChats(t *testing.T) {
	t.Parallel()

	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	ctx := context.Background()
	_, err := raw.ExecContext(ctx, inboxPendingPlanSeed)
	require.NoError(t, err)

	for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
		t.Run(mode, func(t *testing.T) {
			conn, err := raw.Conn(ctx)
			require.NoError(t, err)
			defer conn.Close()
			_, err = conn.ExecContext(ctx, `SET plan_cache_mode = `+mode)
			require.NoError(t, err)
			_, err = conn.ExecContext(ctx, `PREPARE pending(text, text[]) AS `+store.InboxPendingSQL())
			require.NoError(t, err)
			defer func() { _, _ = conn.ExecContext(ctx, `DEALLOCATE pending`) }()
			plan := explainLines(t, ctx, conn,
				`EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF, SUMMARY OFF) EXECUTE pending('ip-u', ARRAY['schedule'])`)

			require.Contains(t, plan, "idx_chats_daemon_blocked",
				"blocked chats must be found from their partial index. Plan:\n%s", plan)
			require.NotContains(t, plan, "Seq Scan on chats",
				"no branch may scan every chat. Plan:\n%s", plan)
			// A chats scan that discards rows walked chats that were never
			// candidates.
			lines := strings.Split(plan, "\n")
			for i, line := range lines {
				if !strings.Contains(line, " on chats ") {
					continue
				}
				for _, next := range lines[i+1:] {
					if strings.Contains(next, "->") {
						break
					}
					require.NotContains(t, next, "Rows Removed by Filter",
						"a branch must find its chats, not filter the caller's:\n%s\nPlan:\n%s", line, plan)
				}
			}
		})
	}

	// And the items are there: the two blocked runs and the paused one.
	rows, err := raw.QueryContext(ctx, store.InboxPendingSQL(), "ip-u", []string{"schedule"})
	require.NoError(t, err)
	defer rows.Close()
	kinds := map[int]int{}
	for rows.Next() {
		cols, err := rows.Columns()
		require.NoError(t, err)
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		require.NoError(t, rows.Scan(ptrs...))
		kinds[int(vals[0].(int64))]++
	}
	require.NoError(t, rows.Err())
	require.Equal(t, map[int]int{3: 2, 10: 1}, kinds, "two waiting for a machine, one paused")
}
