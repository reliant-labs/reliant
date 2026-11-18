package pgtest_test

import (
	"database/sql"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// Every database pgtest hands out has the Postgres 15+ public schema: PUBLIC
// may use it but not create in it, on whatever server, an external Postgres 14
// (FORGE_TEST_POSTGRES_URL) included. Otherwise a migrator's read-only check
// on the dashboard role (pkg/dbgrants) fails every test on 14.
func TestEveryDatabaseDeniesPublicCreateOnPublic(t *testing.T) {
	if testing.Short() {
		t.Skip("boots real postgres; skipped under -short")
	}
	creates := func(t *testing.T, db *sql.DB) bool {
		t.Helper()
		var can bool
		if err := db.QueryRow(`SELECT has_schema_privilege('public', 'public', 'CREATE')`).Scan(&can); err != nil {
			t.Fatal(err)
		}
		return can
	}
	t.Run("New", func(t *testing.T) {
		db, cleanup, err := pgtest.New()
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		if creates(t, db) {
			t.Error("PUBLIC can create in public in a database from New")
		}
	})
	t.Run("NewURL", func(t *testing.T) {
		dsn, cleanup, err := pgtest.NewURL()
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		db, err := sql.Open(pgtest.DriverName, dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		if creates(t, db) {
			t.Error("PUBLIC can create in public in a database from NewURL")
		}
	})
	t.Run("NewFromTemplate", func(t *testing.T) {
		db, _, cleanup, err := pgtest.NewFromTemplate("public_create_probe", func(*sql.DB, string) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		if creates(t, db) {
			t.Error("PUBLIC can create in public in a database from NewFromTemplate")
		}
	})
}
