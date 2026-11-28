// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// The two statements that end live threads find them by workflow_id: the
// terminal cascade (store.CascadeTerminalStatusToThreadSubtree, every time a
// workflow ends) and the reconciler's reap (store.ReapOrphanedThreads, every
// pass). threads had no index on workflow_id, so each read the whole table: in
// prod the cascade ran 13 a minute at ~130ms, a Seq Scan of ~108k rows, and the
// reap a parallel Seq Scan every pass, on a shared instance whose CPU ran out.
//
// The property is that both read threads through idx_threads_live_workflow,
// which holds only while each spells the index's literal predicate,
// status IN (2, 6): a parameterised or reworded status test keeps every result
// and silently loses the index. Asserted under the generic plan a prepared
// statement settles on, with seq and bitmap scans off so an empty table cannot
// make any plan acceptable.
func TestLiveThreadStatementsUseLiveWorkflowIndex(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()

	for name, tc := range map[string]struct {
		prepare string
		execute string
	}{
		"terminal cascade": {
			prepare: `PREPARE live_threads_cascade(text, int) AS ` +
				strings.NewReplacer("?0", "$1", "?1", "$2").Replace(store.CascadeTerminalStatusToThreadSubtreeSQL),
			execute: `EXPLAIN EXECUTE live_threads_cascade('workflow', 3)`,
		},
		"orphan reap": {
			prepare: `PREPARE live_threads_reap AS ` + store.ReapOrphanedThreadsSQL,
			execute: `EXPLAIN EXECUTE live_threads_reap`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			conn, err := env.SQL.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			for _, stmt := range []string{
				`SET plan_cache_mode = force_generic_plan`,
				`SET enable_seqscan = off`,
				`SET enable_bitmapscan = off`,
				tc.prepare,
			} {
				if _, err := conn.ExecContext(ctx, stmt); err != nil {
					t.Fatalf("%s: %v", stmt, err)
				}
			}
			rows, err := conn.QueryContext(ctx, tc.execute)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var lines []string
			for rows.Next() {
				var line string
				if err := rows.Scan(&line); err != nil {
					t.Fatal(err)
				}
				lines = append(lines, line)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			plan := strings.Join(lines, "\n")
			if !strings.Contains(plan, "using idx_threads_live_workflow on threads") {
				t.Fatalf("live threads must be read through idx_threads_live_workflow; anything else reads "+
					"every thread. This happens when the statement's status test no longer spells the "+
					"index predicate status IN (2, 6). Plan:\n%s", plan)
			}
		})
	}
}
