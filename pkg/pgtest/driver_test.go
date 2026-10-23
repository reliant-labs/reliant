package pgtest_test

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/reliant-labs/forge/pkg/observe/dbtrace"
	"github.com/reliant-labs/forge/pkg/pgtest"
)

// Every handle pgtest hands out runs on pgx under dbtrace, the way production
// opens its pool (serverkit.OpenDB). On lib/pq a DB test exercised a different
// program: other array and time scanning, no statement cache, and
// *pq.Error where production code sees *pgconn.PgError — so a classifier
// that only matched one of them passed its tests and failed in production.
func TestHandlesRunTheProductionDriver(t *testing.T) {
	if testing.Short() {
		t.Skip("boots real postgres; skipped under -short")
	}
	key := uniqueKey(t)
	build := func(db *sql.DB, _ string) error {
		if !dbtrace.IsTraced(db) {
			return fmt.Errorf("template build handle driver = %T, want production's: pgx under dbtrace", db.Driver())
		}
		_, err := db.Exec(`CREATE TABLE seeded (v int PRIMARY KEY)`)
		return err
	}
	handles := map[string]func() (*sql.DB, func(), error){
		"New": pgtest.New,
		"NewFromTemplate": func() (*sql.DB, func(), error) {
			db, _, cleanup, err := pgtest.NewFromTemplate(key, build)
			return db, cleanup, err
		},
	}
	for name, open := range handles {
		t.Run(name, func(t *testing.T) {
			db, cleanup, err := open()
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			t.Cleanup(cleanup)
			// Traced, as production's pool is; the PgError below proves
			// pgx underneath.
			if !dbtrace.IsTraced(db) {
				t.Fatalf("driver = %T, want production's: pgx under dbtrace", db.Driver())
			}

			// The error a test sees is the error production sees.
			_, err = db.Exec(`SELECT 1/0`)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "22012" {
				t.Fatalf("division by zero: %v (%T), want a *pgconn.PgError with SQLSTATE 22012", err, err)
			}
		})
	}
}
