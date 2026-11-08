package testkit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/orm"
)

// execer is the narrow slice of orm.Context that insertFixture needs: the
// raw parameterized Exec. orm.Context (and thus NewMigratedPostgresDB's
// return) satisfies it; depending on the narrow interface keeps the SQL
// path unit-testable with a recording fake (no real DB).
type execer interface {
	Exec(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
}

// the production DB handle (orm.Context) satisfies the narrow seam — it
// declares Exec with the same signature, so LoadFixture accepts it.
var _ execer = orm.Context(nil)

// Fixture is the on-disk shape of a forge fixture file: a named bundle of
// table rows. It matches the JSON the projects already keep under
// db/fixtures/*.json (the "Auto-generated seed data" files), so existing
// fixtures load without conversion:
//
//	{
//	  "name": "users",
//	  "tables": {
//	    "users": [ {"id": "…", "email": "…"}, … ]
//	  }
//	}
//
// Each row is an object of column→value; values are decoded as JSON
// scalars (string/number/bool/null) and passed as bind parameters, so the
// database driver does the type coercion against the real column types.
type Fixture struct {
	Name        string                      `json:"name"`
	Description string                      `json:"description"`
	Tables      map[string][]map[string]any `json:"tables"`
}

// LoadFixture reads the fixture JSON at path and inserts its rows into db,
// failing the test on any error. db is expected to be a migrated
// real-postgres handle (NewMigratedPostgresDB) so the target tables exist;
// LoadFixture does not create schema, it only seeds data.
//
// Rows insert in a stable column order (sorted) with parameterized values,
// one INSERT per row, in the order they appear in the file — so a fixture
// can be authored to respect foreign-key ordering within a table. Tables
// themselves insert in sorted name order for determinism; cross-table FK
// ordering should be handled by loading dependency fixtures first, or by
// keeping FK-related rows in one fixture file ordered correctly.
//
// Returns the parsed Fixture so a test can assert on what it loaded
// (counts, ids) without re-reading the file.
//
//	db := app.NewMigratedTestDB(t)
//	fx := testkit.LoadFixture(t, db, "../../db/fixtures/users.json")
//	// …exercise a read path that expects len(fx.Tables["users"]) rows…
func LoadFixture(t testing.TB, db orm.Context, path string) *Fixture {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("testkit.LoadFixture: read %s: %v", path, err)
	}
	fx, err := parseFixture(raw)
	if err != nil {
		t.Fatalf("testkit.LoadFixture: parse %s: %v", path, err)
	}
	if err := insertFixture(context.Background(), db, fx); err != nil {
		t.Fatalf("testkit.LoadFixture: seed %s: %v", path, err)
	}
	return fx
}

// parseFixture decodes fixture JSON. Split out of LoadFixture for direct
// testing without a database.
func parseFixture(raw []byte) (*Fixture, error) {
	var fx Fixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		return nil, err
	}
	if len(fx.Tables) == 0 {
		return nil, fmt.Errorf("fixture has no tables")
	}
	return &fx, nil
}

// insertFixture writes every row of every table in fx to db. Split out of
// LoadFixture so the SQL-building path is unit-testable against any
// orm.Context.
func insertFixture(ctx context.Context, db execer, fx *Fixture) error {
	tableNames := make([]string, 0, len(fx.Tables))
	for name := range fx.Tables {
		tableNames = append(tableNames, name)
	}
	sort.Strings(tableNames)

	for _, table := range tableNames {
		for i, row := range fx.Tables[table] {
			query, args := buildInsert(table, row)
			if len(args) == 0 {
				continue // empty row object — nothing to insert
			}
			if _, err := db.Exec(ctx, query, args...); err != nil {
				return fmt.Errorf("insert %s row %d: %w", table, i, err)
			}
		}
	}
	return nil
}

// buildInsert renders a parameterized INSERT for one row. Columns sort by
// name so the generated SQL is deterministic (stable across runs and easy
// to read in a failed-query message). Identifiers are double-quoted to
// tolerate reserved-word column names; values always go through $N bind
// params, never string interpolation.
func buildInsert(table string, row map[string]any) (string, []any) {
	cols := make([]string, 0, len(row))
	for c := range row {
		cols = append(cols, c)
	}
	sort.Strings(cols)

	quotedCols := make([]string, len(cols))
	placeholders := make([]string, len(cols))
	args := make([]any, len(cols))
	for i, c := range cols {
		quotedCols[i] = quoteIdent(c)
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = row[c]
	}

	query := fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s)",
		quoteIdent(table),
		strings.Join(quotedCols, ", "),
		strings.Join(placeholders, ", "),
	)
	return query, args
}

// quoteIdent double-quotes a SQL identifier, escaping any embedded quote.
// Fixture column/table names come from db/fixtures/*.json checked into the
// repo (not user input), but quoting keeps reserved words working and is
// the correct default.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
