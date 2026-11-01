// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// GetWorkflowExecutions' BASIC view (store.BasicChatStepExecutionsSQL) finds
// each visible step's "-save" sibling by workflow, step id AND loop scope. An
// agent loop writes one execute_tools-save per turn under one step id, so a
// probe that keys only (workflow_id, step_id) and filters the scope walks the
// run's whole history to keep one row: in prod, 1,958 entries per probe and
// 1.3-3.9s per chat open on a 636-run chat.
//
// The property is that the loop scope is an INDEX CONDITION on
// idx_step_executions_save_scope, not a Filter. It holds only while the
// query spells the scope exactly as the index's expression key; a rewrite
// back to IS NOT DISTINCT FROM (never indexable) keeps every result and
// loses the index, which TestChatStepExecutionViews cannot see.
//
// The data has the prod shape that matters, one loop step with a long save
// history, and is ANALYZEd, so the planner probes the saves from the few
// visible steps rather than the other way round. Asserted under the generic
// plan, the one a prepared statement settles on after five runs.
func TestBasicStepSaveProbeUsesScopeIndex(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	seedSteps(t, env)

	// 2,000 turns of one loop step's saves, as one agent loop writes them.
	if _, err := env.SQL.ExecContext(ctx, `
		INSERT INTO step_executions (id, workflow_id, step_id, activity_name, output_json, created_at, loop_node_id, loop_iteration)
		SELECT 'turn-' || g, 'wf-s2', 'loop_body-save', 'SaveMessage',
		       '{"message_id":"turn-msg-' || g || '"}', now(), 'loop', 100 + g
		  FROM generate_series(1, 2000) g`); err != nil {
		t.Fatal(err)
	}

	conn, err := env.SQL.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	query := strings.ReplaceAll(store.BasicChatStepExecutionsSQL, "?0", "$1") // bun's ?0 is the one bound parameter
	for _, stmt := range []string{
		`ANALYZE step_executions`,
		`ANALYZE workflows`,
		`SET plan_cache_mode = force_generic_plan`,
		`PREPARE basic_steps(text) AS ` + query,
	} {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	rows, err := conn.QueryContext(ctx, `EXPLAIN EXECUTE basic_steps('chat-steps')`)
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

	if !scopeIsIndexCond(lines, "idx_step_executions_save_scope") {
		t.Fatalf("the save probe must match the loop scope as an Index Cond on idx_step_executions_save_scope; "+
			"otherwise it reads every save of the step's run. This happens when the query's scope "+
			"comparison no longer matches the index's ARRAY[loop_node_id, loop_iteration::text] key. Plan:\n%s",
			strings.Join(lines, "\n"))
	}
}

// scopeIsIndexCond reports whether the plan matches the save's loop scope
// (ARRAY[loop_node_id, ...]) in an Index Cond of a scan on index.
func scopeIsIndexCond(lines []string, index string) bool {
	for i, line := range lines {
		if !strings.Contains(line, "Index Cond") || !strings.Contains(line, "ARRAY[loop_node_id") {
			continue
		}
		// The Index Cond line belongs to the nearest scan node above it.
		for j := i; j >= 0; j-- {
			if strings.Contains(lines[j], "Scan") {
				return strings.Contains(lines[j], index)
			}
		}
	}
	return false
}
