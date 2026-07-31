// Copyright (c) 2025 Reliant Labs
package db

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// internal/db/postgres/schema.sql is sqlc's declared input, but the migrations
// are what actually build the database. Nothing forced the two to agree, and
// they stopped agreeing: the file described 29 tables while the migrations
// created 39, so sqlc could not see eleven tables the running system had.
//
// Both sides here are read out of a live Postgres catalog rather than parsed
// out of the SQL text. A regex over CREATE TABLE would answer a different
// question than "what does this file build" — it would miss a table dropped by
// a later migration, and it would count a CREATE that never executes. Applying
// each side and asking the catalog is the only comparison that reflects what
// sqlc and the running database each actually see.
func TestSchemaSQLMatchesMigrations(t *testing.T) {
	// Side A: this package's test database, built by goose from the migrations.
	_, migratedDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	// Side B: a throwaway database containing exactly what schema.sql builds.
	schemaDB, dropSchemaDB := loadSchemaSQLIntoScratchDB(t)
	defer dropSchemaDB()

	migTables := tableSet(t, migratedDB, "migrations")
	fileTables := tableSet(t, schemaDB, "schema.sql")

	// Fail loudly rather than pass vacuously. An empty set on either side makes
	// every "missing from" comparison below trivially satisfied, so a query that
	// silently stopped matching would look exactly like a clean schema.
	if len(migTables) == 0 {
		t.Fatal("derived zero tables from the migrated database — this guard is checking nothing")
	}
	if len(fileTables) == 0 {
		t.Fatal("derived zero tables from schema.sql — this guard is checking nothing")
	}

	if missing := difference(migTables, fileTables); len(missing) > 0 {
		t.Errorf("%d table(s) built by the migrations are absent from internal/db/postgres/schema.sql: %v\n"+
			"sqlc cannot generate code for these. Regenerate with scripts/generate-schema-sql.sh",
			len(missing), missing)
	}
	if extra := difference(fileTables, migTables); len(extra) > 0 {
		t.Errorf("%d table(s) in internal/db/postgres/schema.sql are not built by the migrations: %v\n"+
			"sqlc will generate code for tables the database does not have. "+
			"Regenerate with scripts/generate-schema-sql.sh",
			len(extra), extra)
	}
}

// Whole missing tables were the drift that prompted this guard, but a column
// added by a migration and never copied into schema.sql is the same defect one
// level down, and it fails more quietly: sqlc still generates the struct, just
// without the field. Types are compared too, because a column that changed type
// makes sqlc emit a Go type the driver will reject at runtime.
func TestSchemaSQLColumnsMatchMigrations(t *testing.T) {
	_, migratedDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	schemaDB, dropSchemaDB := loadSchemaSQLIntoScratchDB(t)
	defer dropSchemaDB()

	migCols := columnSet(t, migratedDB, "migrations")
	fileCols := columnSet(t, schemaDB, "schema.sql")

	if len(migCols) == 0 {
		t.Fatal("derived zero columns from the migrated database — this guard is checking nothing")
	}
	if len(fileCols) == 0 {
		t.Fatal("derived zero columns from schema.sql — this guard is checking nothing")
	}

	// Restrict to tables both sides have: whole-table drift is reported by
	// TestSchemaSQLMatchesMigrations, and repeating it here as hundreds of
	// column failures would bury any genuine column-level difference.
	shared := intersection(tableNamesOf(migCols), tableNamesOf(fileCols))
	if len(shared) == 0 {
		t.Fatal("the two schemas share no tables at all — this guard is checking nothing")
	}

	var diffs []string
	for key, migType := range migCols {
		table := key.table
		if !shared[table] {
			continue
		}
		fileType, ok := fileCols[key]
		if !ok {
			diffs = append(diffs, fmt.Sprintf("%s.%s: in migrations (%s), absent from schema.sql", table, key.column, migType))
			continue
		}
		if fileType != migType {
			diffs = append(diffs, fmt.Sprintf("%s.%s: migrations say %s, schema.sql says %s", table, key.column, migType, fileType))
		}
	}
	for key, fileType := range fileCols {
		if !shared[key.table] {
			continue
		}
		if _, ok := migCols[key]; !ok {
			diffs = append(diffs, fmt.Sprintf("%s.%s: in schema.sql (%s), absent from migrations", key.table, key.column, fileType))
		}
	}

	if len(diffs) > 0 {
		sort.Strings(diffs)
		t.Errorf("%d column difference(s) between the migrations and internal/db/postgres/schema.sql:\n  %s\n"+
			"Regenerate with scripts/generate-schema-sql.sh",
			len(diffs), strings.Join(diffs, "\n  "))
	}
}

type columnKey struct {
	table  string
	column string
}

// loadSchemaSQLIntoScratchDB executes internal/db/postgres/schema.sql against a
// freshly created database and returns a handle to it. Reading the result from
// the catalog — rather than parsing the file — means the comparison is against
// the schema the file actually produces, which is the thing sqlc consumes.
func loadSchemaSQLIntoScratchDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()

	baseDSN := os.Getenv("DATABASE_URL")
	if baseDSN == "" {
		t.Skip("DATABASE_URL not set, skipping database test")
	}

	// go test runs with the package directory as the working directory.
	schemaPath := filepath.Join("postgres", "schema.sql")
	contents, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read %s: %v", schemaPath, err)
	}
	if len(strings.TrimSpace(string(contents))) == 0 {
		t.Fatalf("%s is empty — nothing to compare against", schemaPath)
	}

	u, err := url.Parse(baseDSN)
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}

	admin, err := sql.Open("pgx", baseDSN)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	defer admin.Close()

	// Name it after this package's own test database so concurrent package
	// binaries running this same guard cannot collide.
	base := strings.TrimPrefix(u.Path, "/")
	if base == "" {
		base = "postgres"
	}
	name := base + "_schemasql"

	if _, err := admin.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS %s`, quoteIdent(name))); err != nil {
		t.Fatalf("drop scratch database: %v", err)
	}
	if _, err := admin.Exec(fmt.Sprintf(`CREATE DATABASE %s`, quoteIdent(name))); err != nil {
		t.Fatalf("create scratch database: %v", err)
	}

	scratchURL := *u
	scratchURL.Path = "/" + name
	scratchDB, err := sql.Open("pgx", scratchURL.String())
	if err != nil {
		t.Fatalf("open scratch database: %v", err)
	}

	if _, err := scratchDB.Exec(string(contents)); err != nil {
		scratchDB.Close()
		t.Fatalf("%s failed to load into an empty database: %v\n"+
			"schema.sql must be replayable — regenerate it with scripts/generate-schema-sql.sh", schemaPath, err)
	}
	// schema.sql sets an empty search_path for the session; restore it so the
	// catalog queries below resolve unqualified names normally.
	if _, err := scratchDB.Exec(`SET search_path TO public`); err != nil {
		scratchDB.Close()
		t.Fatalf("reset search_path: %v", err)
	}

	cleanup := func() {
		scratchDB.Close()
		admin2, err := sql.Open("pgx", baseDSN)
		if err != nil {
			return
		}
		defer admin2.Close()
		_, _ = admin2.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS %s`, quoteIdent(name)))
	}
	return scratchDB, cleanup
}

// tableSet reads the base tables of the public schema from the catalog. Views
// are excluded: sqlc reads them, but they are derived objects whose drift shows
// up as column drift in the tables they select from.
func tableSet(t *testing.T, db *sql.DB, label string) map[string]bool {
	t.Helper()
	rows, err := db.Query(`
		SELECT tablename
		FROM pg_tables
		WHERE schemaname = 'public' AND tablename <> 'goose_db_version'`)
	if err != nil {
		t.Fatalf("query tables (%s): %v", label, err)
	}
	defer rows.Close()

	set := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table (%s): %v", label, err)
		}
		set[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate tables (%s): %v", label, err)
	}
	return set
}

// columnSet maps every base-table column of the public schema to its type.
func columnSet(t *testing.T, db *sql.DB, label string) map[columnKey]string {
	t.Helper()
	rows, err := db.Query(`
		SELECT c.table_name, c.column_name, c.data_type
		FROM information_schema.columns c
		JOIN information_schema.tables t
		  ON t.table_schema = c.table_schema AND t.table_name = c.table_name
		WHERE c.table_schema = 'public'
		  AND t.table_type = 'BASE TABLE'
		  AND c.table_name <> 'goose_db_version'`)
	if err != nil {
		t.Fatalf("query columns (%s): %v", label, err)
	}
	defer rows.Close()

	set := map[columnKey]string{}
	for rows.Next() {
		var table, column, dataType string
		if err := rows.Scan(&table, &column, &dataType); err != nil {
			t.Fatalf("scan column (%s): %v", label, err)
		}
		set[columnKey{table: table, column: column}] = dataType
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate columns (%s): %v", label, err)
	}
	return set
}

func tableNamesOf(cols map[columnKey]string) map[string]bool {
	set := map[string]bool{}
	for k := range cols {
		set[k.table] = true
	}
	return set
}

func intersection(a, b map[string]bool) map[string]bool {
	out := map[string]bool{}
	for k := range a {
		if b[k] {
			out[k] = true
		}
	}
	return out
}

// difference returns the sorted members of a that are absent from b.
func difference(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
