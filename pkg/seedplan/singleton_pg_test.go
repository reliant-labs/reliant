package seedplan

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// singletonMigration is the singleton-table idiom in its common spellings, each
// with a child referencing it. reliant's legacy_credential_copy is the first:
// `CHECK (id = 1)` on the sole key, which the seeder used to plan 20 rows for,
// writing id = 2 on row two and aborting the whole seed on the CHECK.
const singletonMigration = `
CREATE TABLE legacy_credential_copy (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    payload TEXT NOT NULL
);

CREATE TABLE copy_audits (
    id UUID PRIMARY KEY,
    copy_id INTEGER NOT NULL REFERENCES legacy_credential_copy(id),
    note TEXT NOT NULL
);

CREATE TABLE app_settings (
    singleton BOOLEAN PRIMARY KEY DEFAULT true CHECK (singleton),
    theme TEXT NOT NULL
);

CREATE TABLE named_singleton (
    id TEXT PRIMARY KEY CHECK (id IN ('only')),
    label TEXT NOT NULL
);

CREATE TABLE named_singleton_refs (
    id UUID PRIMARY KEY,
    singleton_id TEXT REFERENCES named_singleton(id)
);
`

func TestMaterialize_CheckPinnedSingletonOnRealPostgres(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	if _, err := db.Exec(singletonMigration); err != nil {
		t.Fatalf("apply migration to target: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "00001_init.up.sql"), []byte(singletonMigration), 0o644); err != nil {
		t.Fatalf("write migration: %v", err)
	}

	if _, err := Materialize(ctx, db, dir, "", Config{Rows: 20, Salt: 1}); err != nil {
		t.Fatalf("Materialize (a singleton CHECK violation would surface here): %v", err)
	}

	count := func(table string) int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		return n
	}
	for _, table := range []string{"legacy_credential_copy", "app_settings", "named_singleton"} {
		if n := count(table); n != 1 {
			t.Errorf("%s holds %d rows, want exactly 1 (its key admits one value)", table, n)
		}
	}
	if n := count("copy_audits"); n != 20 {
		t.Errorf("copy_audits holds %d rows, want 20: a singleton's child is not capped", n)
	}
	var id int
	if err := db.QueryRowContext(ctx, "SELECT id FROM legacy_credential_copy").Scan(&id); err != nil || id != 1 {
		t.Errorf("singleton id = %d (err %v), want the pinned 1", id, err)
	}
	var stray int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM copy_audits WHERE copy_id <> 1").Scan(&stray); err != nil || stray != 0 {
		t.Errorf("%d copy_audits rows reference something other than the singleton (err %v)", stray, err)
	}
}
