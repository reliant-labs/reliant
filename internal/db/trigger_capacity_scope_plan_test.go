// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/store"
)

// Every capacity admission lists its scope's occupied slots
// (store.ListTriggerCapacityExecutionsSQL). The query named the scope and
// outcome = 'launched' AND NOT capacity_released, but not capacity_order IS
// NOT NULL, so the planner could not prove it implies the predicate of
// trigger_events_capacity_occupied, the partial index over exactly those rows,
// and fell back to a parallel Seq Scan of the whole table: in prod ~17 a
// minute at ~0.9s each over 330k rows, the largest share of reliant-pg's CPU.
//
// The property is that the read goes through trigger_events_capacity_occupied
// under the generic plan a prepared statement settles on after five runs.
func TestListTriggerCapacityExecutionsUsesOccupiedIndex(t *testing.T) {
	t.Parallel()

	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	ctx := context.Background()
	conn, err := raw.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()

	// An empty table is cheaper to scan than to probe, so any plan would do
	// and the test would be vacuous; with seq and bitmap scans off the planner
	// must pick an index, and only a query that implies the partial index's
	// predicate may pick trigger_events_capacity_occupied.
	for _, stmt := range []string{
		`SET plan_cache_mode = force_generic_plan`,
		`SET enable_seqscan = off`,
		`SET enable_bitmapscan = off`,
	} {
		_, err := conn.ExecContext(ctx, stmt)
		require.NoError(t, err)
	}

	query := strings.NewReplacer("?0", "$1", "?1", "$2", "?2", "$3", "?3", "$4").Replace(store.ListTriggerCapacityExecutionsSQL)
	_, err = conn.ExecContext(ctx, `PREPARE capacity_slots(double precision, text, text, text) AS `+query)
	require.NoError(t, err)

	plan := explainLines(t, ctx, conn, `EXPLAIN EXECUTE capacity_slots(60, 'user', 'project', 'workflow')`)
	require.Contains(t, plan, "using trigger_events_capacity_occupied on trigger_events",
		"a scope's occupied slots must be read through trigger_events_capacity_occupied; "+
			"anything else reads every trigger event. Plan:\n%s", plan)
}
