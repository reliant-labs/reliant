// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/store"
)

// The Inbox judges each automation's health from its newest firings and its
// failure episode (store.RecentTriggerOutcomes, store.FiringsSinceLastSuccess).
// Both used to rank a trigger's whole history with row_number() and keep the
// first N, so an automation with 20,000 runs cost 280ms and 385ms on every
// Inbox read. Each must instead walk idx_trigger_events_trigger_occurred_id
// newest first and stop: no WindowAgg, no read of the history beyond the
// rows it returns.
const triggerFiringsPlanSeed = `
INSERT INTO projects (id, name, path, user_id, is_git_repo, created_at, updated_at, last_active, is_forge)
VALUES ('fire-p', 'p', '/fire', 'fire-u', true, now(), now(), now(), false);

INSERT INTO triggers (id, user_id, project_id, name, kind, enabled, workflow, presets, params, message, config, created_at, updated_at, notify_on_complete, acting_identity)
SELECT 'fire-t' || k, 'fire-u', 'fire-p', 'trigger ' || k, 'schedule', true, 'wf', '[]', '{}', '', '{}', now(), now(), false, 'owner'
FROM generate_series(0, 2) k;

INSERT INTO trigger_events (id, trigger_id, user_id, kind, dedupe_key, occurred_at, payload, outcome, outcome_detail, created_at, capacity_released, run_status)
SELECT 'fire-e' || k || '-' || n, 'fire-t' || k, 'fire-u', 'schedule', 'fire-' || k || '-' || n,
       now() - (n || ' minutes')::interval, '{}', CASE WHEN n % 7 = 0 THEN 'failed' ELSE 'launched' END, '',
       now() - (n || ' minutes')::interval, false, CASE WHEN n % 7 = 0 THEN NULL ELSE 'completed' END
FROM generate_series(0, 2) k, generate_series(0, 4999) n;

ANALYZE projects, triggers, trigger_events;
`

func TestInboxTriggerFiringReadsStopAtTheirLimit(t *testing.T) {
	t.Parallel()

	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	ctx := context.Background()
	_, err := raw.ExecContext(ctx, triggerFiringsPlanSeed)
	require.NoError(t, err)

	recent, since := store.InboxTriggerFiringsSQL()
	for name, query := range map[string]string{"recent outcomes": recent, "since last success": since} {
		for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				conn, err := raw.Conn(ctx)
				require.NoError(t, err)
				defer conn.Close()
				_, err = conn.ExecContext(ctx, `SET plan_cache_mode = `+mode)
				require.NoError(t, err)

				stmt := strings.NewReplacer("?0", "$1", "?1", "$2", "?2", "$3").Replace(query)
				_, err = conn.ExecContext(ctx, `PREPARE firings(text, text[], integer) AS `+stmt)
				require.NoError(t, err)
				// The connection goes back to the pool, prepared statements
				// and all.
				defer func() { _, _ = conn.ExecContext(ctx, `DEALLOCATE firings`) }()
				plan := explainLines(t, ctx, conn, `EXPLAIN EXECUTE firings('fire-u', ARRAY['fire-t0', 'fire-t1', 'fire-t2'], 10)`)

				require.NotContains(t, plan, "WindowAgg", "a trigger's firings must not be ranked over its whole history. Plan:\n%s", plan)
				require.NotContains(t, plan, "Seq Scan on trigger_events", "a trigger's firings must be read from its index range. Plan:\n%s", plan)
				require.Contains(t, plan, "idx_trigger_events_trigger_occurred_id",
					"each trigger's firings must be read newest first from idx_trigger_events_trigger_occurred_id. Plan:\n%s", plan)
			})
		}
	}
}

// episodePlanSeed is one user's two automations as they looked in prod
// (reliant#2736): ep-t0 succeeds almost every run, each run a chat of its
// own, and its newest firing failed; ep-t1 has fired 3,000 times without ever
// launching. The user owns every chat, so no index on user_id narrows them.
const episodePlanSeed = `
INSERT INTO projects (id, name, path, user_id, is_git_repo, created_at, updated_at, last_active, is_forge)
VALUES ('ep-p', 'p', '/ep', 'ep-u', true, now(), now(), now(), false);

INSERT INTO triggers (id, user_id, project_id, name, kind, enabled, workflow, presets, params, message, config, created_at, updated_at, notify_on_complete, acting_identity)
SELECT 'ep-t' || k, 'ep-u', 'ep-p', 'trigger ' || k, 'schedule', true, 'wf', '[]', '{}', '', '{}', now(), now(), false, 'owner'
FROM generate_series(0, 1) k;

INSERT INTO chats (id, title, project_id, user_id, state, workflow_id, created_at, updated_at, last_active, unread, no_machine)
SELECT 'ep-c' || n, 'run', 'ep-p', 'ep-u', 1, 'ep-c' || n, now(), now(), now(), 0, false
FROM generate_series(0, 2999) n;

INSERT INTO workflows (id, chat_id, workflow_name, thread, created_at, completed_at, state, stop_reason, owner_user_id)
SELECT id, id, 'wf', id, now(), now(), 3, 1, user_id FROM chats WHERE id LIKE 'ep-c%';

INSERT INTO trigger_events (id, trigger_id, user_id, kind, dedupe_key, occurred_at, payload, outcome, outcome_detail, chat_id, created_at, capacity_released, run_status)
SELECT 'ep-e0-' || n, 'ep-t0', 'ep-u', 'schedule', 'ep-0-' || n, now() - (n || ' minutes')::interval, '{}',
       CASE WHEN n % 7 = 0 THEN 'failed' ELSE 'launched' END, '',
       CASE WHEN n % 7 = 0 THEN NULL ELSE 'ep-c' || n END,
       now() - (n || ' minutes')::interval, false,
       CASE WHEN n % 7 = 0 OR n % 5 = 0 THEN NULL ELSE 'completed' END
FROM generate_series(0, 2999) n;

INSERT INTO trigger_events (id, trigger_id, user_id, kind, dedupe_key, occurred_at, payload, outcome, outcome_detail, created_at, capacity_released)
SELECT 'ep-e1-' || n, 'ep-t1', 'ep-u', 'schedule', 'ep-1-' || n, now() - (n || ' minutes')::interval, '{}', 'skipped', 'overlap',
       now() - (n || ' minutes')::interval, false
FROM generate_series(0, 2999) n;

ANALYZE projects, triggers, chats, workflows, trigger_events;
`

// The Inbox's episode read (store.FiringsSinceLastSuccess, at the Inbox's
// 500 firings a trigger) must read each trigger's firings only as far as it
// needs, and each firing's run by probing it. In prod (reliant#2736) it
// walked past the newest success to the end of the history, filtering every
// older firing out; walked the whole history of a trigger that never
// launched looking for a success; and hashed chats_with_activity, computing
// the activity of every chat in the database. 450-875ms and 173k buffers a
// read for one user, against 20-100ms and 3.6k.
//
// EXPLAIN ANALYZE, so the rows each trigger_events scan threw away are
// counted, not estimated.
func TestInboxEpisodeReadStopsAtTheSuccessAndProbesRuns(t *testing.T) {
	t.Parallel()

	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	ctx := context.Background()
	_, err := raw.ExecContext(ctx, episodePlanSeed)
	require.NoError(t, err)

	_, since := store.InboxTriggerFiringsSQL()
	for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
		t.Run(mode, func(t *testing.T) {
			conn, err := raw.Conn(ctx)
			require.NoError(t, err)
			defer conn.Close()
			_, err = conn.ExecContext(ctx, `SET plan_cache_mode = `+mode)
			require.NoError(t, err)

			stmt := strings.NewReplacer("?0", "$1", "?1", "$2", "?2", "$3").Replace(since)
			_, err = conn.ExecContext(ctx, `PREPARE episode(text, text[], integer) AS `+stmt)
			require.NoError(t, err)
			defer func() { _, _ = conn.ExecContext(ctx, `DEALLOCATE episode`) }()
			plan := explainLines(t, ctx, conn,
				`EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF, SUMMARY OFF) EXECUTE episode('ep-u', ARRAY['ep-t0', 'ep-t1'], 500)`)

			for _, forbidden := range []string{"Seq Scan on chats", "Seq Scan on workflows", "Seq Scan on trigger_events"} {
				require.NotContains(t, plan, forbidden,
					"each firing's run must be a probe of its chat, not a hash of every chat's activity. Plan:\n%s", plan)
			}
			// A trigger_events scan that discards rows read firings the
			// answer did not need: older than the success, or not launched.
			lines := strings.Split(plan, "\n")
			for i, line := range lines {
				if !strings.Contains(line, "on trigger_events") {
					continue
				}
				for _, next := range lines[i+1:] {
					if strings.Contains(next, "->") {
						break
					}
					require.NotContains(t, next, "Rows Removed by Filter",
						"a trigger's firings must be bounded by the index scan, not filtered after it:\n%s\nPlan:\n%s", line, plan)
				}
			}
		})
	}
}
