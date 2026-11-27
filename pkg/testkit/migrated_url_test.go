package testkit_test

import (
	"context"
	"database/sql"
	"testing"
	"testing/fstest"

	"github.com/reliant-labs/forge/pkg/migratekit"
	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// openDSN opens the database a DSN names, closed at the test's end.
func openDSN(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open(pgtest.DriverName, dsn)
	if err != nil {
		t.Fatalf("open %s: %v", dsn, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// The DSN names a database holding the schema, and the boot migration a server
// root runs on it (AUTO_MIGRATE: migratekit.Apply over the same embedded set)
// finds it current. A database migrated by a raw replay has no applied record,
// so that boot migration would re-run 0001 and fail on a table that exists:
// that is the reason the URL variant applies through migratekit.
func TestNewMigratedPostgresURL_SchemaIsCurrentForTheBootMigrator(t *testing.T) {
	requirePG(t)
	t.Parallel()
	mfs := fstest.MapFS{
		"migrations/0001_init.up.sql":     {Data: []byte(`CREATE TABLE widgets (id BIGINT PRIMARY KEY, name TEXT NOT NULL);`)},
		"migrations/0001_init.down.sql":   {Data: []byte(`DROP TABLE widgets;`)},
		"migrations/0002_seed.up.sql":     {Data: []byte(`INSERT INTO widgets (id, name) VALUES (1, 'sprocket');`)},
		"migrations/0002_seed.down.sql":   {Data: []byte(`DELETE FROM widgets;`)},
		"migrations/0010_column.up.sql":   {Data: []byte(`ALTER TABLE widgets ADD COLUMN color TEXT;`)},
		"migrations/0010_column.down.sql": {Data: []byte(`ALTER TABLE widgets DROP COLUMN color;`)},
	}
	dsn := testkit.NewMigratedPostgresURL(t, mfs)
	db := openDSN(t, dsn)

	var name string
	if err := db.QueryRow(`SELECT name FROM widgets WHERE id = 1 AND color IS NULL`).Scan(&name); err != nil || name != "sprocket" {
		t.Fatalf("schema not applied: name=%q err=%v", name, err)
	}
	res, err := migratekit.Apply(context.Background(), db, mfs, "migrations")
	if err != nil {
		t.Fatalf("the boot migration over the same set failed: %v", err)
	}
	if res.Changed || res.After.Version != 10 {
		t.Fatalf("boot migration: Changed=%v After=%+v, want nothing applied at version 10", res.Changed, res.After)
	}
}

// Each call is its own database, so one test's writes never reach another's,
// and the SQL may sit at the FS root as NewMigratedPostgresDB allows.
func TestNewMigratedPostgresURL_ClonesAreIsolated(t *testing.T) {
	requirePG(t)
	t.Parallel()
	mfs := fstest.MapFS{
		"0001_t.up.sql": {Data: []byte(`CREATE TABLE url_probe (v int);`)},
	}
	a, b := testkit.NewMigratedPostgresURL(t, mfs), testkit.NewMigratedPostgresURL(t, mfs)
	if a == b {
		t.Fatalf("two calls returned the same DSN %q", a)
	}
	if _, err := openDSN(t, a).Exec(`INSERT INTO url_probe VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := openDSN(t, b).QueryRow(`SELECT count(*) FROM url_probe`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("clone b saw %d rows (err=%v), want 0", n, err)
	}
}
