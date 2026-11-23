package migratekit_test

import (
	"database/sql"
	"testing"
	"testing/fstest"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver

	"github.com/reliant-labs/forge/pkg/migratekit"
	"github.com/reliant-labs/forge/pkg/pgtest"
)

// TestUpOnlyMigrationSetApplies proves the premise forge's roll-forward policy
// rests on: golang-migrate's iofs source driver does NOT need a .down.sql
// beside each .up.sql. A migration set with no down files at all opens, applies
// to head, and reaches the top version with a clean dirty flag.
//
// If this ever fails, the policy "forge writes and accepts no down migrations"
// would break every forge project's `db migrate up` — so it is pinned here, at
// the library every scaffolded binary migrates through, against real postgres.
func TestUpOnlyMigrationSetApplies(t *testing.T) {
	if testing.Short() {
		t.Skip("boots real postgres; skipped under -short")
	}
	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("test postgres: %v", err)
	}
	t.Cleanup(cleanup)

	fsys := fstest.MapFS{
		"migrations/00001_create_widgets.up.sql": {Data: []byte(`CREATE TABLE widgets (id TEXT PRIMARY KEY);`)},
		"migrations/00002_add_name.up.sql":       {Data: []byte(`ALTER TABLE widgets ADD COLUMN name TEXT;`)},
		"migrations/00010_seed_nothing.up.sql":   {Data: []byte(`SELECT 1;`)},
	}

	m, err := migratekit.Open(migratekit.Options{FS: fsys, Dir: "migrations", DSN: dsn})
	if err != nil {
		t.Fatalf("an up-only migration set must open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	res, err := m.Up()
	if err != nil {
		t.Fatalf("an up-only migration set must apply: %v", err)
	}
	if !res.Changed || res.After.Version != 10 || res.After.Dirty {
		t.Fatalf("want applied to version 10, clean; got %+v", res)
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`INSERT INTO widgets (id, name) VALUES ('a', 'b')`); err != nil {
		t.Fatalf("schema from the up-only set is not in place: %v", err)
	}

	// Re-running is a no-op, not an error — the steady state of every
	// deploy after the first.
	again, err := m.Up()
	if err != nil || again.Changed {
		t.Fatalf("second Up must be a clean no-op; got %+v, %v", again, err)
	}
}
