package migratekit

import (
	"context"
	"testing"
)

// TestAutoMigrateGivesItsConnectionBack: boot-time migration borrows one
// connection from the server's pool and must return it. golang-migrate's
// WithInstance held a dedicated *sql.Conn that only its Close released, and
// that Close would also have closed the caller's pool, so it was never called.
// Every server that booted kept one MaxOpenConns slot checked out, and one
// idle server session, for its whole life. The same leak made the migrated
// database unusable as a CREATE DATABASE ... TEMPLATE, which refuses a source
// that has a connected session.
func TestAutoMigrateGivesItsConnectionBack(t *testing.T) {
	dsn := requirePG(t)
	db := openRecordDB(t, dsn)

	for _, boot := range []string{"first boot (applies)", "second boot (no change)"} {
		if err := AutoMigrate(release(olderFiles), "migrations")(db, nil); err != nil {
			t.Fatalf("%s: %v", boot, err)
		}
		if inUse := db.Stats().InUse; inUse != 0 {
			t.Fatalf("%s: %d pool connection(s) still checked out after AutoMigrate returned; want 0", boot, inUse)
		}
	}

	// The pool is still the caller's: AutoMigrate must not have closed it.
	var n int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM plans`).Scan(&n); err != nil {
		t.Fatalf("the caller's pool is unusable after AutoMigrate: %v", err)
	}
}
