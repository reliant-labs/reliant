// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
)

// `timestamp without time zone` stores the wall clock it is handed and drops
// the offset, so which instant a row means depends on a convention the column
// cannot enforce. It was not held: step_executions.created_at was written with
// time.Now() (local) and workflows.created_at with time.Now().UTC(), into the
// same database, and every ordering across the two was silently wrong by the
// host's offset.
//
// The column list is derived from the catalog rather than written down, because
// a written list only ever covers the columns someone remembered.
func TestNoNaiveTimestampColumns(t *testing.T) {
	_, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	rows, err := rawDB.Query(`
		SELECT c.table_name, c.column_name, c.data_type
		FROM information_schema.columns c
		JOIN information_schema.tables t
		  ON t.table_schema = c.table_schema AND t.table_name = c.table_name
		WHERE c.table_schema = 'public'
		  AND t.table_type = 'BASE TABLE'
		  AND c.data_type LIKE 'timestamp%'
		ORDER BY c.table_name, c.column_name`)
	if err != nil {
		t.Fatalf("query timestamp columns: %v", err)
	}
	defer rows.Close()

	var total int
	var naive []string
	for rows.Next() {
		var table, column, dataType string
		if err := rows.Scan(&table, &column, &dataType); err != nil {
			t.Fatalf("scan: %v", err)
		}
		total++
		if dataType == "timestamp without time zone" {
			naive = append(naive, table+"."+column)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}

	// Fail loudly rather than pass vacuously: an empty set means the query
	// stopped matching, not that the schema is clean.
	if total == 0 {
		t.Fatal("found no timestamp columns at all — this guard is checking nothing")
	}
	if len(naive) > 0 {
		t.Fatalf("%d of %d timestamp columns still discard their offset: %v", len(naive), total, naive)
	}
}

// The payoff, stated as behaviour rather than as a column type: a writer that
// does NOT remember to call .UTC() stores the same instant as one that does.
// That is what makes the bug unrepresentable instead of merely absent today.
func TestLocalTimeRoundTripsAsTheSameInstant(t *testing.T) {
	repo, _, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	// A fixed zone well away from UTC, so a dropped offset cannot coincidentally
	// produce the right answer on a UTC host.
	written := time.Date(2026, 7, 26, 9, 30, 0, 0, time.FixedZone("EDT", -4*60*60))

	exec := &StepExecution{
		ID:           uuid.New().String(),
		WorkflowID:   uuid.New().String(),
		StepID:       "lint",
		ActivityName: "ExecuteRunStep",
		Success:      sql.NullBool{Bool: true, Valid: true},
		CreatedAt:    written,
	}
	if err := repo.CreateStepExecution(ctx, exec); err != nil {
		t.Fatalf("CreateStepExecution: %v", err)
	}

	got, err := repo.GetStepExecution(ctx, exec.ID)
	if err != nil {
		t.Fatalf("GetStepExecution: %v", err)
	}
	if !got.CreatedAt.Equal(written) {
		t.Fatalf("stored %s, read back %s — a %s offset was discarded on the way in",
			written.Format(time.RFC3339), got.CreatedAt.Format(time.RFC3339),
			written.Sub(got.CreatedAt.In(written.Location())))
	}
}
