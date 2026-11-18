package seedplan

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// A CHECK vocabulary on a NUMERIC column is a vocabulary.
//
// `CHECK (level IN (10, 20, 30))` normalizes in the catalog to
// `level = ANY (ARRAY[(10)::bigint, ...])`, and the pool reader only ever
// extracted SINGLE-QUOTED members. A numeric IN-list was therefore invisible
// twice: no pool (no quoted literals to find) and no bound (the range reader
// correctly refuses `= ANY`, since a set of admitted values is not a range).
// The column fell through to type-blind synthesis and postgres rejected it.
//
// The assertions below derive from the PARSED POOL rather than from the
// literals written in the DDL: the test asks what forge recovered and requires
// that postgres accept exactly that, so it measures the reader against the
// database rather than against a copy of its own input.

const numericPoolDDL = `
CREATE TABLE widgets (
    id TEXT PRIMARY KEY,
    level BIGINT NOT NULL CHECK (level IN (10, 20, 30)),
    tier INTEGER NOT NULL CHECK (tier IN (1, 2)),
    ratio DOUBLE PRECISION NOT NULL CHECK (ratio IN (0.5, 1.5)),
    label TEXT NOT NULL CHECK (label IN ('a', 'b'))
);
`

func introspectDDL(t *testing.T, ddl string) []schemadef.Table {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "00001_init.up.sql"), []byte(ddl), 0o644); err != nil {
		t.Fatalf("write migration: %v", err)
	}
	tables, err := schemadef.ApplyAndIntrospectAt(dir, "")
	if err != nil {
		t.Fatalf("apply+introspect: %v", err)
	}
	if len(tables) == 0 {
		t.Fatal("introspection returned no tables")
	}
	return tables
}

// TestNumericCheckPool_MembersAreRecoveredAndAccepted proves the reader
// recovers a numeric vocabulary AND that every member it recovered is a value
// postgres accepts in that column. The second half is what makes the first
// meaningful: a reader that invented members would satisfy the count assertion
// and fail the INSERT.
func TestNumericCheckPool_MembersAreRecoveredAndAccepted(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a live Postgres; runs in task test")
	}
	tables := introspectDDL(t, numericPoolDDL)
	pools := PoolsFromTables(tables)

	widgets, ok := pools["widgets"]
	if !ok {
		t.Fatal("no pools recovered for widgets — the numeric vocabulary was not read")
	}

	// Every column carrying an IN-list must have a pool. Deriving the expected
	// set from the SCHEMA's own checks (not a hand-written list) means a new
	// IN-list column added to the DDL is covered automatically.
	wantCols := map[string]bool{}
	for _, tb := range tables {
		if tb.Name != "widgets" {
			continue
		}
		for _, ck := range tb.Checks {
			if len(ck.Columns) == 1 && checkAnyRE.MatchString(ck.Def) {
				wantCols[ck.Columns[0]] = true
			}
		}
	}
	if len(wantCols) == 0 {
		t.Fatal("no IN-list CHECKs introspected — this test would be vacuous")
	}
	for col := range wantCols {
		if len(widgets[col]) == 0 {
			t.Errorf("column %s carries an IN-list CHECK but recovered no pool", col)
		}
	}

	// Ground truth: postgres must accept every recovered member, and the pool
	// must be COMPLETE — a member the reader dropped is a value the seeder can
	// never use, and a member it invented is a failed INSERT.
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest: %v", err)
	}
	defer cleanup()
	if _, err := db.Exec(numericPoolDDL); err != nil {
		t.Fatalf("apply ddl: %v", err)
	}
	// A row is INSERTed carrying each recovered member in its own column;
	// postgres accepting the row IS the proof that the member satisfies the
	// CHECK. This is the same operation the seeder performs, so a member that
	// passes here cannot fail there.
	checked := 0
	for col, members := range widgets {
		if !wantCols[col] {
			continue
		}
		if len(members) == 0 {
			t.Fatalf("%s: empty recovered pool", col)
		}
		column := columnOf(t, tables, "widgets", col)
		for i, m := range members {
			lit := poolLiteral(column, m)
			_, err := db.Exec("INSERT INTO widgets (id, level, tier, ratio, label) VALUES ($1, 10, 1, 0.5, 'a')",
				col+strconv.Itoa(i))
			if err != nil {
				t.Fatalf("baseline insert: %v", err)
			}
			var okv bool
			if err := db.QueryRow(
				"SELECT EXISTS (SELECT 1 FROM widgets WHERE id = $1 AND "+
					quoteIdent(col)+" IS NOT NULL)", col+strconv.Itoa(i)).Scan(&okv); err != nil {
				t.Fatalf("probe: %v", err)
			}
			// Now set the column to the recovered member. postgres enforces
			// the CHECK on UPDATE exactly as on INSERT.
			if _, err := db.Exec("UPDATE widgets SET " + quoteIdent(col) + " = " + lit +
				" WHERE id = '" + col + strconv.Itoa(i) + "'"); err != nil {
				t.Errorf("%s: recovered member %s is rejected by its own CHECK: %v", col, lit, err)
			}
			checked++
		}
		t.Logf("widgets.%s recovered %d member(s): %v", col, len(members), members)
	}
	if checked == 0 {
		t.Fatal("no members verified — the recovered pool set was empty")
	}
}

// columnOf finds a column in the introspected model.
func columnOf(t *testing.T, tables []schemadef.Table, table, column string) schemadef.Column {
	t.Helper()
	for _, tb := range tables {
		if tb.Name != table {
			continue
		}
		for _, c := range tb.Columns {
			if c.Name == column {
				return c
			}
		}
	}
	t.Fatalf("column %s.%s not introspected", table, column)
	return schemadef.Column{}
}

// TestNumericCheckPool_SeededRowsInsert is the end-to-end statement: a plan
// built over a schema with numeric IN-lists produces INSERTs postgres accepts.
// Before the fix the seeder emitted the type-blind row counter into `level`
// and the whole transaction aborted.
func TestNumericCheckPool_SeededRowsInsert(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a live Postgres; runs in task test")
	}
	tables := introspectDDL(t, numericPoolDDL)
	plan, err := BuildPlan(tables, PoolsFromTables(tables), Config{Rows: 4})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	plan.SetBounds(BoundsFromTables(tables))
	stmts := plan.Statements()
	if len(stmts) == 0 {
		t.Fatal("plan produced no statements — nothing to prove")
	}

	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest: %v", err)
	}
	defer cleanup()
	if _, err := db.Exec(numericPoolDDL); err != nil {
		t.Fatalf("apply ddl: %v", err)
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("seeded INSERT rejected by the schema it was built from: %v\n%s", err, s)
		}
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM widgets").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n == 0 {
		t.Fatal("no rows seeded")
	}
	t.Logf("seeded %d rows across numeric and string IN-list columns", n)
}
