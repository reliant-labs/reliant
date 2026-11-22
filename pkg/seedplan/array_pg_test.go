package seedplan

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// arrayChecksMigration is reliant's access_tokens.scopes shape — a required
// array whose CHECK confines it to a closed set and requires it non-empty —
// beside the same constraint over every element type the synthesizer knows,
// and the two cardinality-only forms.
//
// It used to seed ARRAY['sample_scopes_1', 'sample_scopes_2'], which no
// declared set contains, so the INSERT failed access_tokens_scopes and the
// one-transaction seed rolled back every table.
const arrayChecksMigration = `
CREATE TABLE access_tokens (
    id TEXT PRIMARY KEY,
    scopes TEXT[] NOT NULL,
    roles VARCHAR(16)[] NOT NULL CHECK (roles <@ ARRAY['admin', 'member']::varchar[]),
    levels BIGINT[] NOT NULL CHECK (cardinality(levels) >= 2 AND levels <@ ARRAY[10, 20, 30]::bigint[]),
    owners UUID[] NOT NULL CHECK (owners <@ ARRAY['5eed0000-0000-5000-8000-000000000001'::uuid, '5eed0000-0000-5000-8000-000000000002'::uuid]),
    colors TEXT[] NOT NULL CHECK (colors <@ '{red,"dark blue"}'::text[]),
    flags BOOLEAN[] NOT NULL CHECK (flags <@ ARRAY[true]),
    counts INT[] NOT NULL CHECK (array_length(counts, 1) >= 3),
    single TEXT[] NOT NULL CHECK (cardinality(single) <= 1),
    tags TEXT[],
    CONSTRAINT access_tokens_scopes CHECK (
        cardinality(scopes) > 0
        AND scopes <@ ARRAY['deploy:read', 'deploy:write', 'token:read', 'questions:answer']::text[]
    )
);
`

// arrayChecks is every CHECK above, restated as a predicate each seeded row
// must satisfy. postgres already enforced them on INSERT; asserting them again
// keeps the test honest if a constraint is ever made NOT VALID.
var arrayChecks = map[string]string{
	"scopes": `cardinality(scopes) > 0 AND scopes <@ ARRAY['deploy:read', 'deploy:write', 'token:read', 'questions:answer']::text[]`,
	"roles":  `cardinality(roles) > 0 AND roles <@ ARRAY['admin', 'member']::varchar[]`,
	"levels": `cardinality(levels) >= 2 AND levels <@ ARRAY[10, 20, 30]::bigint[]`,
	"owners": `cardinality(owners) > 0 AND owners <@ ARRAY['5eed0000-0000-5000-8000-000000000001'::uuid, '5eed0000-0000-5000-8000-000000000002'::uuid]`,
	"colors": `cardinality(colors) > 0 AND colors <@ '{red,"dark blue"}'::text[]`,
	"flags":  `cardinality(flags) > 0 AND flags <@ ARRAY[true]`,
	"counts": `array_length(counts, 1) >= 3`,
	"single": `cardinality(single) = 1`,
}

func TestMaterialize_RequiredArrayColumnsSatisfyTheirChecks(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	if _, err := db.Exec(arrayChecksMigration); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	migDir := filepath.Join(t.TempDir(), "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(migDir, "00001_init.up.sql"), []byte(arrayChecksMigration), 0o644); err != nil {
		t.Fatal(err)
	}

	const rows = 20
	if _, err := Materialize(ctx, db, migDir, "", Config{Rows: rows, Salt: 1}); err != nil {
		t.Fatalf("Materialize (a required array's CHECK violation surfaces here): %v", err)
	}
	assertCount(t, db, "access_tokens", rows)
	for col, pred := range arrayChecks {
		var bad int
		if err := db.QueryRow(`SELECT count(*) FROM access_tokens WHERE NOT (` + pred + `)`).Scan(&bad); err != nil {
			t.Fatalf("%s: %v", col, err)
		}
		if bad != 0 {
			t.Errorf("%d row(s) of access_tokens.%s violate %s", bad, col, pred)
		}
	}
	// The draw is spread across the set, not one scope on every token.
	var distinct int
	if err := db.QueryRow(`SELECT count(DISTINCT scopes) FROM access_tokens`).Scan(&distinct); err != nil {
		t.Fatal(err)
	}
	if distinct < 2 {
		t.Errorf("access_tokens.scopes holds %d distinct value(s) across %d rows, want a spread", distinct, rows)
	}
}

// The entity-factory path plans one row of a minimal plan from the
// introspected model; the same required array must insert there too.
func TestMinimalPlan_RequiredArrayColumnInserts(t *testing.T) {
	tables := applyDDL(t, arrayChecksMigration)
	p, err := BuildPlan(tables, PoolsFromTables(tables), Config{Rows: 1, Minimal: true})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	p.SetBounds(BoundsFromTables(tables))
	db := minimalHarness(t, arrayChecksMigration)
	for _, stmt := range p.Statements() {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("postgres refused the minimal plan:\n%s\n%v", stmt, err)
		}
	}
	assertCount(t, db, "access_tokens", 1)
}
