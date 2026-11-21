package seedplan

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// twinMigration declares the SAME table twice under two names. Postgres holds
// both to the identical contract — a `uuid` primary key accepts only a
// canonical UUID — so any value the seeder derives from a table's NAME rather
// than from its shape either aborts the transactional seed or lands in exactly
// one of the two.
const twinMigration = `
CREATE TABLE users (
    id UUID PRIMARY KEY,
    email TEXT NOT NULL,
    name TEXT NOT NULL
);

CREATE TABLE accounts (
    id UUID PRIMARY KEY,
    email TEXT NOT NULL,
    name TEXT NOT NULL
);
`

// Two identically-shaped tables, one named `users` and one not, seed the same
// way on a real postgres. The INSERT succeeding at all is the sharp half: a
// value the seeder invents for a `uuid` key is rejected by the database, and
// because the seed is one transaction, ONE such table takes the whole dataset
// down with it.
func TestMaterialize_TableNameDoesNotChangeTheSeedOnRealPostgres(t *testing.T) {
	requirePG(t)
	ctx := context.Background()

	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	if _, err := db.Exec(twinMigration); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "00001_init.up.sql"), []byte(twinMigration), 0o644); err != nil {
		t.Fatalf("write migration: %v", err)
	}

	if _, err := Materialize(ctx, db, dir, "", Config{Rows: 6, Salt: 4}); err != nil {
		t.Fatalf("Materialize: %v\n\nA seeded value postgres rejects aborts the WHOLE dataset — "+
			"the seed is one transaction.", err)
	}

	// Both tables now hold rows, and every synthesized value in each carries
	// the emitter's stamp — the property forge itself puts on what it invents,
	// never a literal list.
	if SyntheticStringPrefix == "" {
		t.Fatal("the emitter stamps nothing — the assertions below would hold vacuously")
	}
	seen := map[string][]string{}
	for _, table := range []string{"users", "accounts"} {
		rows, err := db.QueryContext(ctx, `SELECT id::text, email, name FROM `+table+` ORDER BY id`)
		if err != nil {
			t.Fatalf("select %s: %v", table, err)
		}
		n := 0
		for rows.Next() {
			var id, email, name string
			if err := rows.Scan(&id, &email, &name); err != nil {
				t.Fatalf("scan %s: %v", table, err)
			}
			// postgres already proved id is a UUID by accepting it.
			for col, got := range map[string]string{"email": email, "name": name} {
				if !strings.HasPrefix(got, SyntheticStringPrefix) {
					t.Errorf("%s.%s = %q does not carry %q — nothing in this schema says what "+
						"either column holds, so forge invented it and must say so",
						table, col, got, SyntheticStringPrefix)
				}
			}
			seen[table] = append(seen[table], email+"|"+name)
			n++
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate %s: %v", table, err)
		}
		if n == 0 {
			t.Fatalf("%s seeded no rows — nothing was checked", table)
		}
	}
	// The twins are the same declaration, so they hold the same SET of values.
	// Not the same order: the key function salts on the table name, so the two
	// tables' rows sort differently by id — which is exactly the one place a
	// table's name is allowed to matter (it keeps two tables' keys distinct).
	sort.Strings(seen["users"])
	sort.Strings(seen["accounts"])
	if len(seen["users"]) != len(seen["accounts"]) {
		t.Fatalf("twins seeded %d and %d rows", len(seen["users"]), len(seen["accounts"]))
	}
	for i := range seen["users"] {
		if seen["users"][i] != seen["accounts"][i] {
			t.Errorf("row %d: users=%q accounts=%q — one of the twins was treated differently, "+
				"and the only thing that differs is the table's NAME",
				i, seen["users"][i], seen["accounts"][i])
		}
	}
}
