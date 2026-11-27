// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/require"
)

// TestCrudTableOwnersDeclared pins
// db/migrations/20261127221652_crud_table_owners.up.sql against a migrated
// database: attachments, plans and tasks each carry the `forge:unscoped:`
// table comment forge's crud-table-unowned lint reads, naming the handler
// check that scopes their rows. The migration is comment-only, so the tables
// must still read as before.
func TestCrudTableOwnersDeclared(t *testing.T) {
	testkit.Slow(t, "a DB-backed test against pgtest's Postgres")
	t.Parallel()

	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	cases := []struct {
		table string
		names string // the handler check the declaration names
		read  string // a read of the table, which the migration must leave working
	}{
		{table: "attachments", names: "ownedBy: user_id", read: `SELECT count(*) FROM attachments`},
		{table: "plans", names: "writableChatOfThread", read: `SELECT count(*) FROM plans`},
		{table: "tasks", names: "writableChatOfPlan", read: `SELECT count(*) FROM tasks`},
	}
	for _, c := range cases {
		var comment *string
		require.NoError(t, raw.QueryRowContext(context.Background(),
			`SELECT obj_description(format('%I', $1::text)::regclass, 'pg_class')`, c.table).Scan(&comment),
			"%s", c.table)
		require.NotNil(t, comment, "%s has no table comment: the crud_table_owners migration did not run", c.table)
		require.True(t, strings.HasPrefix(*comment, "forge:unscoped: "),
			"%s's comment %q is not a forge:unscoped declaration", c.table, *comment)
		require.Contains(t, *comment, c.names, "%s's declaration names its handler check", c.table)

		var n int
		require.NoError(t, raw.QueryRowContext(context.Background(),
			c.read).Scan(&n), "%s still reads after the comment-only migration", c.table)
	}
}
