// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// plans.status, plans.complexity and tasks.status must be plain integer
// columns with no forge:go-type comment: with one, forge projects them to the
// proto enum type, which its generated PlanService and TaskService CRUD ops
// cannot convert (migration plan_task_enum_columns_plain_ints).
func TestPlanTaskEnumColumnsArePlainInts(t *testing.T) {
	_, raw, cleanup := SetupTestDBWithRawDB(t)
	t.Cleanup(cleanup)

	for _, c := range []struct{ table, column string }{
		{"plans", "status"},
		{"plans", "complexity"},
		{"tasks", "status"},
	} {
		var dataType string
		var comment *string
		err := raw.QueryRowContext(context.Background(), `
			SELECT c.data_type,
			       col_description(format('%I', c.table_name)::regclass, c.ordinal_position::int)
			FROM information_schema.columns c
			WHERE c.table_schema = current_schema() AND c.table_name = $1 AND c.column_name = $2`,
			c.table, c.column).Scan(&dataType, &comment)
		require.NoError(t, err, "%s.%s", c.table, c.column)
		require.Equal(t, "integer", dataType, "%s.%s", c.table, c.column)
		require.Nil(t, comment, "%s.%s keeps a column comment", c.table, c.column)
	}
}
