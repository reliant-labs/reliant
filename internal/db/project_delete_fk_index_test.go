// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// Deleting a project removes millions of rows that other rows point at. For
// each deleted row Postgres looks up its referencing rows by the foreign key's
// column (the RESTRICT check, the SET NULL or the CASCADE), and the explicit
// purge in internal/store/project_delete.go filters by the same columns. With
// no index on the referencing column each lookup is a table scan, once per
// deleted row: that is what took prod's project delete to 17 minutes
// (2026-11-13). These eight indexes exist to make each lookup an index probe.
//
// Asserted as the plan of the lookup Postgres runs (`col = $1`), under the
// generic plan a prepared statement settles on, with seq and bitmap scans
// off so an empty table cannot make any plan acceptable; and each index must
// be valid, since a CREATE INDEX CONCURRENTLY that failed leaves an invalid
// one the planner never uses.
func TestProjectDeleteForeignKeyLookupsUseTheirIndexes(t *testing.T) {
	t.Parallel()

	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	conn, err := raw.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()
	for _, stmt := range []string{
		`SET plan_cache_mode = force_generic_plan`,
		`SET enable_seqscan = off`,
		`SET enable_bitmapscan = off`,
	} {
		_, err := conn.ExecContext(ctx, stmt)
		require.NoError(t, err)
	}

	for i, c := range []struct{ table, column, index string }{
		{"threads", "fork_at_message_id", "idx_threads_fork_at_message"},
		{"context_windows", "fork_at_message_id", "idx_context_windows_fork_at_message"},
		{"agent_messages", "delivered_message_id", "idx_agent_messages_delivered_message"},
		{"agent_messages", "from_thread_id", "idx_agent_messages_from_thread"},
		{"agent_messages", "to_thread_id", "idx_agent_messages_to_thread"},
		{"threads", "worktree_id", "idx_threads_worktree"},
		{"user_updates", "worktree_id", "idx_user_updates_worktree"},
		{"chats", "project_id", "idx_chats_project"},
	} {
		var valid, ready bool
		require.NoError(t, conn.QueryRowContext(ctx,
			`SELECT i.indisvalid, i.indisready FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid WHERE c.relname = $1`,
			c.index).Scan(&valid, &ready), "index %s must exist", c.index)
		require.True(t, valid && ready, "index %s is not valid (valid=%v ready=%v)", c.index, valid, ready)

		name := fmt.Sprintf("fk_lookup_%d", i)
		_, err := conn.ExecContext(ctx, fmt.Sprintf(`PREPARE %s AS SELECT 1 FROM %s WHERE %s = $1`, name, c.table, c.column))
		require.NoError(t, err)
		plan := explainLines(t, ctx, conn, fmt.Sprintf(`EXPLAIN EXECUTE %s('x')`, name))
		require.Regexp(t, `Index (Only )?Scan using `+c.index+` `, plan,
			"looking up %s by %s must probe %s, not scan the table once per deleted row. Plan:\n%s", c.table, c.column, c.index, plan)
	}
}
