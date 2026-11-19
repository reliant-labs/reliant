// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// liveChatsPlanSeed is prod's chat shape on 2026-11-18, scaled down: one user
// owns nearly every chat, and nearly all of theirs are archived automation
// runs (44,385 of 46,639 in prod). 20,000 chats, 19,000 archived; the 1,000
// live ones are mostly runs too, so the sidebar policy keeps one in ten. A
// second user owns a handful. Every timestamp is a fixed instant, never now(),
// so no row and no assertion depends on the wall clock.
const liveChatsPlanSeed = `
INSERT INTO projects (id, name, path, user_id, is_git_repo, created_at, updated_at, last_active, is_forge)
VALUES ('lc-p', 'p', '/lc', 'lc-u', true, '2026-01-01T12:00:00Z'::timestamptz, '2026-01-01T12:00:00Z'::timestamptz, '2026-01-01T12:00:00Z'::timestamptz, false),
       ('lc-p2', 'p', '/lc2', 'lc-u2', true, '2026-01-01T12:00:00Z'::timestamptz, '2026-01-01T12:00:00Z'::timestamptz, '2026-01-01T12:00:00Z'::timestamptz, false);

INSERT INTO chats (id, title, project_id, user_id, state, workflow_id, created_at, updated_at, last_active, unread, no_machine, origin)
SELECT 'lc-c' || n, 'run', 'lc-p', 'lc-u', CASE WHEN n < 19000 THEN 3 ELSE 1 END, 'lc-c' || n,
       '2026-01-01T12:00:00Z'::timestamptz - (20000 - n) * interval '1 minute', '2026-01-01T12:00:00Z'::timestamptz, '2026-01-01T12:00:00Z'::timestamptz - (20000 - n) * interval '1 minute', 0, false,
       CASE WHEN n % 10 = 0 THEN 1 ELSE 2 END
FROM generate_series(0, 19999) n;

INSERT INTO chats (id, title, project_id, user_id, state, workflow_id, created_at, updated_at, last_active, unread, no_machine, origin)
SELECT 'lc-o' || n, 'chat', 'lc-p2', 'lc-u2', 1, 'lc-o' || n, '2026-01-01T12:00:00Z'::timestamptz, '2026-01-01T12:00:00Z'::timestamptz, '2026-01-01T12:00:00Z'::timestamptz, 0, false, 1
FROM generate_series(0, 9) n;

ANALYZE projects, chats;
`

// liveChatsStatement is store.ListChats as the sidebar sends it
// (ExcludeArchived, SidebarOnly, LIMIT 100), with $1 the user.
const liveChatsStatement = `SELECT * FROM chats_with_activity WHERE (user_id = $1) AND (NOT EXISTS (SELECT 1 FROM projects dp WHERE dp.id = project_id AND dp.delete_after IS NOT NULL)) AND (state != 3) AND (list_in_sidebar = TRUE) ORDER BY last_active DESC LIMIT 100`

// The chat list reads the caller's live chats from
// idx_chats_user_live_last_active, never by walking every chat they ever
// had: in prod it was a Seq Scan of chats discarding 46,783 rows (44,385 of
// them the user's archived runs) to return 26.
func TestChatListReadsLiveChatsFromTheirIndex(t *testing.T) {
	t.Parallel()

	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	ctx := context.Background()
	_, err := raw.ExecContext(ctx, liveChatsPlanSeed)
	require.NoError(t, err)

	for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
		t.Run(mode, func(t *testing.T) {
			conn, err := raw.Conn(ctx)
			require.NoError(t, err)
			defer conn.Close()
			_, err = conn.ExecContext(ctx, `SET plan_cache_mode = `+mode)
			require.NoError(t, err)
			_, err = conn.ExecContext(ctx, `PREPARE live_chats(text) AS `+liveChatsStatement)
			require.NoError(t, err)
			defer func() { _, _ = conn.ExecContext(ctx, `DEALLOCATE live_chats`) }()
			plan := explainLines(t, ctx, conn,
				`EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF, SUMMARY OFF) EXECUTE live_chats('lc-u')`)

			require.Contains(t, plan, "idx_chats_user_live_last_active",
				"the live chats must be found from their partial index. Plan:\n%s", plan)
			require.NotContains(t, plan, "Seq Scan on chats",
				"the chat list may not scan every chat. Plan:\n%s", plan)
			// What the chats scan discards is bounded by the live chats it
			// read, never the archived ones.
			lines := strings.Split(plan, "\n")
			for i, line := range lines {
				if !strings.Contains(line, " on chats ") {
					continue
				}
				for _, next := range lines[i+1:] {
					if strings.Contains(next, "->") {
						break
					}
					if strings.Contains(next, "Rows Removed by Filter") {
						var removed int
						_, err := fmt.Sscanf(strings.TrimSpace(next), "Rows Removed by Filter: %d", &removed)
						require.NoError(t, err, next)
						require.Less(t, removed, 1000,
							"the chats scan discarded archived chats:\n%s\nPlan:\n%s", line, plan)
					}
				}
			}
		})
	}

	// And the page is the user's live sidebar chats, newest first.
	rows, err := raw.QueryContext(ctx, strings.Replace(liveChatsStatement, "SELECT *", "SELECT id", 1), "lc-u")
	require.NoError(t, err)
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	require.Len(t, ids, 100, "every tenth of the 1,000 live chats is a conversation")
	require.Equal(t, "lc-c19990", ids[0])
	require.Equal(t, "lc-c19000", ids[99])
}
