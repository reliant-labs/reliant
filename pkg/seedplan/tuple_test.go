// File: pkg/seedplan/tuple_test.go
//
// Composite UNIQUE: the distinctness no single column carries. These pin the
// two halves the one-column paths already have — the shape forge PLACES, so
// the tuple holds by construction rather than by luck, and the shapes it
// refuses, which must reach a warning instead of being quietly assumed
// satisfied.

package seedplan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// fkCol builds a NOT NULL reference column.
func fkCol(name string) schemadef.Column {
	return schemadef.Column{Name: name, DeclType: "TEXT", Type: schemadef.TypeString, NotNull: true}
}

// membershipTables is the real control-plane shape: a join table whose two
// members are BOTH foreign keys, neither unique on its own, the PAIR unique.
func membershipTables() []schemadef.Table {
	return []schemadef.Table{
		{Name: "orgs", PKCols: []string{"id"}, Columns: []schemadef.Column{idCol(), textCol("name")}},
		{Name: "users", PKCols: []string{"id"}, Columns: []schemadef.Column{idCol(), textCol("handle")}},
		{
			Name:    "org_memberships",
			PKCols:  []string{"id"},
			Columns: []schemadef.Column{idCol(), fkCol("org_id"), fkCol("user_id"), textCol("role")},
			ForeignKeys: []schemadef.ForeignKey{
				{Column: "org_id", RefTable: "orgs", RefColumn: "id", Name: "org_memberships_org_id_fkey"},
				{Column: "user_id", RefTable: "users", RefColumn: "id", Name: "org_memberships_user_id_fkey"},
			},
			Indexes: []schemadef.Index{
				{Name: "org_memberships_org_id_user_id_key", Columns: []string{"org_id", "user_id"}, Unique: true},
			},
		},
	}
}

// planForTables builds the plan a real seed would build for a whole schema.
func planForTables(t *testing.T, tables []schemadef.Table, cfg Config) *Plan {
	t.Helper()
	p, err := BuildPlan(tables, PoolsFromTables(tables), cfg)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	p.SetBounds(BoundsFromTables(tables))
	return p
}

// tupleAt reads one row's rendered value for each of the named columns.
func tupleAt(t *testing.T, p *Plan, table string, cols []string, row int) string {
	t.Helper()
	parts := make([]string, len(cols))
	for i, c := range cols {
		v, ok := p.SeedValue(table, c, row)
		if !ok {
			t.Fatalf("%s.%s row %d has no seeded scalar value", table, c, row)
		}
		parts[i] = v
	}
	return strings.Join(parts, "|")
}

// The measured failure: `UNIQUE (org_id, user_id)` over two hash-picked
// references collided by row six and aborted the seed transaction.
func TestCompositeUnique_TupleIsDistinctAcrossRows(t *testing.T) {
	const rows = 20
	p := planForTables(t, membershipTables(), Config{Rows: rows, Salt: 1})

	if warns := p.Warnings(); len(warns) > 0 {
		t.Fatalf("a composite UNIQUE forge can place must produce no warning, got:\n  %s",
			strings.Join(warns, "\n  "))
	}
	if got := p.rowsOf["org_memberships"]; got != rows {
		t.Fatalf("org_memberships holds %d row(s), want %d — the table must not be capped", got, rows)
	}

	seen := map[string]int{}
	for i := 0; i < rows; i++ {
		key := tupleAt(t, p, "org_memberships", []string{"org_id", "user_id"}, i)
		if prev, dup := seen[key]; dup {
			t.Errorf("rows %d and %d both carry (org_id, user_id) = %s — a UNIQUE index cannot repeat a tuple", prev, i, key)
		}
		seen[key] = i
	}
}

// The point of a composite key, and the thing that makes it different from two
// single-column ones: each member REPEATS. A fix that made both columns unique
// would pass the test above and be wrong — it would describe a schema where an
// org has at most one member and a user at most one membership.
func TestCompositeUnique_MembersStillRepeatIndividually(t *testing.T) {
	const rows = 12
	p := planForTables(t, membershipTables(), Config{
		Rows:         rows,
		Salt:         1,
		RowsPerTable: map[string]int{"orgs": 4, "users": 5},
	})
	if warns := p.Warnings(); len(warns) > 0 {
		t.Fatalf("4 orgs x 5 users holds 12 rows without a cap, got:\n  %s", strings.Join(warns, "\n  "))
	}

	for _, col := range []string{"org_id", "user_id"} {
		seen := map[string]bool{}
		repeated := false
		for i := 0; i < rows; i++ {
			v := tupleAt(t, p, "org_memberships", []string{col}, i)
			if seen[v] {
				repeated = true
			}
			seen[v] = true
		}
		if !repeated {
			t.Errorf("%s takes a distinct value on every one of %d rows over a %d-row parent — "+
				"the composite constraint was satisfied by making the column unique, which is a different schema",
				col, rows, len(seen))
		}
	}
	// And the pairs are still all distinct.
	seen := map[string]bool{}
	for i := 0; i < rows; i++ {
		key := tupleAt(t, p, "org_memberships", []string{"org_id", "user_id"}, i)
		if seen[key] {
			t.Errorf("row %d repeats the tuple %s", i, key)
		}
		seen[key] = true
	}
}

// Capacity is a SCHEMA fact, knowable before a single INSERT: two parents of 2
// and 3 rows admit six pairs, so a 10-row target is a promise the constraint
// cannot keep. It caps and says so, exactly as a short one-column vocabulary
// does, rather than letting postgres abort the transaction mid-run.
func TestCompositeUnique_CapsTheTableWhenCombinationsRunOut(t *testing.T) {
	p := planForTables(t, membershipTables(), Config{
		Rows:         10,
		Salt:         1,
		RowsPerTable: map[string]int{"orgs": 2, "users": 3},
	})
	joined := strings.Join(p.Warnings(), "\n")
	if !strings.Contains(joined, "org_memberships_org_id_user_id_key") ||
		!strings.Contains(joined, "only 6 distinct combination(s)") ||
		!strings.Contains(joined, "capped to 6 row(s)") {
		t.Fatalf("a composite UNIQUE short of capacity must name the index, the capacity and the cap; got:\n%s", joined)
	}
	if got := p.rowsOf["org_memberships"]; got != 6 {
		t.Errorf("org_memberships holds %d row(s), want 6", got)
	}
	seen := map[string]bool{}
	for i := 0; i < 6; i++ {
		key := tupleAt(t, p, "org_memberships", []string{"org_id", "user_id"}, i)
		if seen[key] {
			t.Errorf("row %d repeats the tuple %s inside the capped range", i, key)
		}
		seen[key] = true
	}
}

// A member that is ALREADY distinct per row settles the tuple on its own.
// Placing anything then would be re-deciding a column another mechanism owns —
// and capping the table for a constraint that cannot bite would be worse.
func TestCompositeUnique_AlreadyDistinctMemberNeedsNoPlacement(t *testing.T) {
	tables := membershipTables()
	for i := range tables {
		if tables[i].Name != "org_memberships" {
			continue
		}
		// UNIQUE (id, org_id): id is the primary key, distinct by definition.
		tables[i].Indexes = []schemadef.Index{
			{Name: "org_memberships_id_org_id_key", Columns: []string{"id", "org_id"}, Unique: true},
		}
	}
	p := planForTables(t, tables, Config{Rows: 8, Salt: 1, RowsPerTable: map[string]int{"orgs": 2, "users": 2}})
	if warns := p.Warnings(); len(warns) > 0 {
		t.Fatalf("a composite UNIQUE containing the key needs no placement and no warning, got:\n  %s",
			strings.Join(warns, "\n  "))
	}
	if got := p.rowsOf["org_memberships"]; got != 8 {
		t.Errorf("org_memberships holds %d row(s), want 8 — a constraint the key already satisfies must not cap it", got)
	}
	if p.tupleOwns("org_memberships", "org_id") {
		t.Error("org_id was placed by the composite pass although the key member already makes the tuple distinct")
	}
}

// A constraint no member of which supplies distinct values is REFUSED, in the
// wording every other refusal in this package uses — not silently assumed.
func TestCompositeUnique_RefusesWhenNoMemberSupplies(t *testing.T) {
	tbl := schemadef.Table{
		Name:   "documents",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			idCol(),
			{Name: "tags", DeclType: "TEXT[]", Type: schemadef.TypeString, IsArray: true, NotNull: true},
			{Name: "meta", DeclType: "JSONB", Type: schemadef.TypeJSON, NotNull: true},
		},
		Indexes: []schemadef.Index{
			{Name: "documents_tags_meta_key", Columns: []string{"tags", "meta"}, Unique: true},
		},
	}
	joined := strings.Join(planFor(t, tbl, 4).Warnings(), "\n")
	if !strings.Contains(joined, `documents index "documents_tags_meta_key" is a composite UNIQUE but forge cannot place its values`) {
		t.Fatalf("an unplaceable composite UNIQUE must be named; got:\n%s", joined)
	}
	if !strings.Contains(joined, "seeded rows satisfy it only by chance") {
		t.Errorf("the refusal must end in the same clause every other refusal here ends in; got:\n%s", joined)
	}
}

// A member that is NULL on every row makes the constraint vacuous — postgres
// never conflicts two NULLs — so there is nothing to place and nothing to say.
func TestCompositeUnique_AlwaysNullMemberIsVacuous(t *testing.T) {
	tbl := schemadef.Table{
		Name:   "invites",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			idCol(), textCol("email"),
			{Name: "deleted_at", DeclType: "TIMESTAMPTZ", Type: schemadef.TypeTime},
			{Name: "created_at", DeclType: "TIMESTAMPTZ", Type: schemadef.TypeTime, NotNull: true},
			{Name: "updated_at", DeclType: "TIMESTAMPTZ", Type: schemadef.TypeTime, NotNull: true},
		},
		Indexes: []schemadef.Index{
			{Name: "invites_email_deleted_at_key", Columns: []string{"email", "deleted_at"}, Unique: true},
		},
	}
	p := planFor(t, tbl, 6)
	if warns := p.Warnings(); len(warns) > 0 {
		t.Fatalf("a soft-delete marker is NULL on every seeded row, so the constraint cannot bite; got:\n  %s",
			strings.Join(warns, "\n  "))
	}
	if got := p.rowsOf["invites"]; got != 6 {
		t.Errorf("invites holds %d row(s), want 6", got)
	}
}

// Determinism is the contract the whole planner rests on: the placement must
// be a pure function of the schema and the row, and adding rows must APPEND
// rather than reshuffle what is already seeded.
func TestCompositeUnique_IsDeterministicAndAppendOnly(t *testing.T) {
	cfg := func(rows int) Config {
		return Config{Rows: rows, Salt: 1, RowsPerTable: map[string]int{"orgs": 4, "users": 5}}
	}
	read := func(p *Plan, n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = tupleAt(t, p, "org_memberships", []string{"org_id", "user_id"}, i)
		}
		return out
	}
	a := read(planForTables(t, membershipTables(), cfg(8)), 8)
	b := read(planForTables(t, membershipTables(), cfg(8)), 8)
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("row %d rendered %s on one run and %s on the next — the placement must be a pure function of (schema, row)", i, a[i], b[i])
		}
	}
	wide := read(planForTables(t, membershipTables(), cfg(16)), 8)
	for i := range a {
		if a[i] != wide[i] {
			t.Errorf("row %d moved when the row target grew: %s -> %s", i, a[i], wide[i])
		}
	}
}

// A UNIQUE foreign key is DEALT a distinct parent per row, and the diamond
// resolution may not re-pick it. It already declined to narrow the VIA side of
// an `authoritative` pair for this reason; the AUTHORITATIVE side was
// unguarded, and the auto-resolved shape below — a required 1-1 edge plus an
// optional one that reaches the same parent — narrowed the unique column into
// a handful of buckets and aborted its own INSERT:
//
//	duplicate key value violates unique constraint "managed_reliant_access_user_id_key"
func TestUniqueFK_SurvivesTheAutoResolvedDiamond(t *testing.T) {
	const rows = 12
	tables := []schemadef.Table{
		{Name: "users", PKCols: []string{"id"}, Columns: []schemadef.Column{idCol(), textCol("handle")}},
		{
			Name:    "orgs",
			PKCols:  []string{"id"},
			Columns: []schemadef.Column{idCol(), fkCol("owner_id")},
			ForeignKeys: []schemadef.ForeignKey{
				{Column: "owner_id", RefTable: "users", RefColumn: "id", Name: "orgs_owner_id_fkey"},
			},
		},
		{
			Name:   "managed_access",
			PKCols: []string{"id"},
			Columns: []schemadef.Column{
				idCol(), fkCol("user_id"),
				{Name: "internal_org_id", DeclType: "TEXT", Type: schemadef.TypeString},
			},
			ForeignKeys: []schemadef.ForeignKey{
				{Column: "user_id", RefTable: "users", RefColumn: "id", Name: "managed_access_user_id_fkey"},
				{Column: "internal_org_id", RefTable: "orgs", RefColumn: "id", Name: "managed_access_internal_org_id_fkey"},
			},
			Indexes: []schemadef.Index{
				{Name: "managed_access_user_id_key", Columns: []string{"user_id"}, Unique: true},
			},
		},
	}
	p := planForTables(t, tables, Config{Rows: rows, Salt: 1})
	seen := map[string]int{}
	for i := 0; i < p.rowsOf["managed_access"]; i++ {
		v := tupleAt(t, p, "managed_access", []string{"user_id"}, i)
		if prev, dup := seen[v]; dup {
			t.Errorf("rows %d and %d both reference user %s — a UNIQUE foreign key is a 1-1 relationship", prev, i, v)
		}
		seen[v] = i
	}
}

// membershipMigration is the real control-plane shape, reduced to the
// constraint under test.
const membershipMigration = `
CREATE TABLE orgs (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL
);
CREATE TABLE users (
    id TEXT PRIMARY KEY,
    handle TEXT NOT NULL
);
CREATE TABLE org_memberships (
    id TEXT PRIMARY KEY,
    org_id TEXT NOT NULL REFERENCES orgs(id),
    user_id TEXT NOT NULL REFERENCES users(id),
    role TEXT NOT NULL,
    UNIQUE (org_id, user_id)
);
`

// Real postgres is the judge: it enforces the composite index on every row, so
// the INSERT landing IS the proof that no tuple repeats.
func TestMaterialize_CompositeUniqueOnRealPostgres(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	if _, err := db.Exec(membershipMigration); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "00001_init.up.sql"), []byte(membershipMigration), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Materialize(ctx, db, dir, "", Config{Rows: 20, Salt: 1}); err != nil {
		t.Fatalf("a duplicate (org_id, user_id) would surface here: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM org_memberships`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 20 {
		t.Errorf("org_memberships holds %d row(s), want 20", n)
	}
	// Each member repeats: the pair is what is unique, not either column.
	var orgs, users int
	if err := db.QueryRow(`SELECT count(DISTINCT org_id), count(DISTINCT user_id) FROM org_memberships`).Scan(&orgs, &users); err != nil {
		t.Fatal(err)
	}
	if orgs == 20 && users == 20 {
		t.Error("every org and every user appears exactly once — the seeder made both columns unique, which is a 1-1 schema, not a join table")
	}
}
