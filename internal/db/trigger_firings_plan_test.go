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
