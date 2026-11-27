package seedplan

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// clearMigration is friction item #3's database: forge seeded jobs, and the
// app then wrote its own job, an assignment for a SEEDED job (ON DELETE
// CASCADE), and an API key whose optional job link no seeded row fills.
const clearMigration = `
CREATE TABLE jobs (
    id   TEXT PRIMARY KEY,
    name TEXT NOT NULL
);
CREATE TABLE runs (
    job_id TEXT   NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    seq    BIGINT NOT NULL,
    PRIMARY KEY (job_id, seq)
);
CREATE TABLE assignments (
    job_id     TEXT   NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    generation BIGINT NOT NULL,
    PRIMARY KEY (job_id, generation)
);
CREATE TABLE api_keys (
    id     TEXT PRIMARY KEY,
    job_id TEXT REFERENCES jobs (id) ON DELETE CASCADE
);
`

func clearFixture(t *testing.T) (context.Context, *sql.DB, *Plan, func(string) int, func(string)) {
	t.Helper()
	requirePG(t)
	ctx := context.Background()
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	if _, err := db.Exec(clearMigration); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "00001_init.up.sql"), []byte(clearMigration), 0o644); err != nil {
		t.Fatal(err)
	}
	// The env-up scope: the CRUD table and what it requires (runs is a
	// composite-keyed child seeded too, to exercise a multi-column key).
	plan, err := BuildLivePlan(ctx, db, dir, "", Config{Rows: 5, Tables: []string{"jobs", "runs"}})
	if err != nil {
		t.Fatalf("BuildLivePlan: %v", err)
	}
	if _, err := Apply(ctx, db, plan); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	count := func(q string) int {
		t.Helper()
		var n int
		if err := db.QueryRow(q).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	exec := func(q string) {
		t.Helper()
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return ctx, db, plan, count, exec
}

// TestClear_PreservesNonmatchingRows: `forge db seed clear` matches seed
// keys. The app's own job and API keys have different keys and stay;
// the assignment the app wrote for a seeded job goes with it, by the
// schema's ON DELETE CASCADE. (seed reset's TRUNCATE ... CASCADE would have
// emptied jobs, assignments AND api_keys.)
func TestClear_PreservesNonmatchingRows(t *testing.T) {
	ctx, db, plan, count, exec := clearFixture(t)
	exec(`INSERT INTO jobs (id, name) VALUES ('real-job', 'mine')`)
	exec(`INSERT INTO api_keys (id, job_id) VALUES ('k1', 'real-job'), ('k2', NULL)`)
	exec(`INSERT INTO assignments (job_id, generation) SELECT id, 1 FROM jobs`)

	deleted, _, err := Clear(ctx, db, plan, "")
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if deleted != 10 {
		t.Errorf("Clear deleted %d rows, want 10 (5 jobs + 5 runs)", deleted)
	}
	if got := count(`SELECT count(*) FROM jobs`); got != 1 {
		t.Errorf("jobs holds %d rows, want only the app's real-job", got)
	}
	if got := count(`SELECT count(*) FROM jobs WHERE id = 'real-job'`); got != 1 {
		t.Error("the app's own job was deleted")
	}
	if got := count(`SELECT count(*) FROM runs`); got != 0 {
		t.Errorf("runs (seeded, composite key) holds %d rows, want 0", got)
	}
	if got := count(`SELECT count(*) FROM assignments`); got != 1 {
		t.Errorf("assignments holds %d rows, want 1 (the real job's; seeded jobs' cascade away)", got)
	}
	if got := count(`SELECT count(*) FROM api_keys`); got != 2 {
		t.Errorf("api_keys holds %d rows, want both of the app's keys", got)
	}

	// Idempotent: nothing seeded is left to find.
	again, _, err := Clear(ctx, db, plan, "")
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Errorf("second Clear = %v rows, want 0", again)
	}
}

func TestClear_TableLimitsDirectDeletes(t *testing.T) {
	ctx, db, plan, count, _ := clearFixture(t)
	deleted, _, err := Clear(ctx, db, plan, "runs")
	if err != nil || deleted != 5 {
		t.Fatalf("clear runs: deleted %d, err %v; want all five matching runs", deleted, err)
	}
	if got := count(`SELECT count(*) FROM jobs`); got != 5 {
		t.Errorf("clearing runs deleted from jobs: count %d, want 5", got)
	}
	if _, _, err := Clear(ctx, db, plan, "unknown"); err == nil {
		t.Error("clear accepted a table not in the plan")
	}
}

// A reference that forbids the delete rolls the WHOLE clear back: no table
// is left half-cleared.
func TestClear_ForbiddenReferenceRollsBack(t *testing.T) {
	ctx, db, plan, count, exec := clearFixture(t)
	exec(`CREATE TABLE audit (id TEXT PRIMARY KEY, job_id TEXT NOT NULL REFERENCES jobs (id))`)
	exec(`INSERT INTO audit (id, job_id) SELECT 'a', id FROM jobs LIMIT 1`)

	_, _, err := Clear(ctx, db, plan, "")
	if err == nil || !strings.Contains(err.Error(), "audit") {
		t.Fatalf("Clear = %v, want postgres's foreign-key error naming audit", err)
	}
	if got := count(`SELECT count(*) FROM runs`); got != 5 {
		t.Errorf("runs holds %d rows after a failed clear, want all 5 (rolled back)", got)
	}
}

func TestClearStatement_KeysByEveryPrimaryKeyColumn(t *testing.T) {
	keyless := schemadef.Table{Name: "events", Columns: []schemadef.Column{{Name: "body", DeclType: "TEXT", Type: schemadef.TypeString, TypeKnown: true, NotNull: true}}}
	pair := schemadef.Table{Name: "pairs", PKCols: []string{"a", "b"}, Columns: []schemadef.Column{
		{Name: "a", DeclType: "TEXT", Type: schemadef.TypeString, TypeKnown: true, NotNull: true, IsPK: true},
		{Name: "b", DeclType: "BIGINT", Type: schemadef.TypeInt, TypeKnown: true, NotNull: true, IsPK: true},
	}}
	plan, err := BuildPlan([]schemadef.Table{keyless, pair}, nil, Config{Rows: 2})
	if err != nil {
		t.Fatal(err)
	}
	for _, tp := range plan.tables {
		stmt := plan.clearStatement(tp)
		switch tp.table.Name {
		case "events":
			if stmt != "" {
				t.Errorf("a keyless table's seeded rows cannot be identified; got %q", stmt)
			}
		case "pairs":
			if !strings.HasPrefix(stmt, `DELETE FROM "pairs" WHERE ("a", "b") IN ((`) || !strings.Contains(stmt, "), (") {
				t.Errorf("pairs clear = %q", stmt)
			}
		}
	}
}
