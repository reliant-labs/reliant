// File: pkg/seedplan/opaque_unique_test.go
//
// Two facts about a column the schema describes only OPAQUELY, both of which
// used to cap a real table at one row:
//
//   - a BYTEA column's synthesized value must vary with the row, or a UNIQUE
//     index on it admits exactly one value and the table can never carry two;
//   - a PARTIAL unique index is a weaker statement than a plain one, and
//     reading it as plain describes a stricter table than the one that exists.

package seedplan

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

func bytesCol(name string) schemadef.Column {
	return schemadef.Column{Name: name, DeclType: "BYTEA", Type: schemadef.TypeBytes, NotNull: true}
}

// decodeBytea reads a rendered `'\xHEX'::bytea` literal back to its bytes.
// SeedValue deliberately refuses a bytea cast (it is not a plain scalar), so
// the assertions here read the rendered statement, which is what actually
// reaches postgres.
func decodeBytea(t *testing.T, lit string) []byte {
	t.Helper()
	body := strings.TrimSuffix(strings.TrimPrefix(lit, `'\x`), `'::bytea`)
	raw, err := hex.DecodeString(body)
	if err != nil {
		t.Fatalf("%q is not a hex bytea literal: %v", lit, err)
	}
	return raw
}

// byteaCells renders one column's literal for every row of the plan.
func byteaCells(t *testing.T, p *Plan, table, column string, rows int) []string {
	t.Helper()
	tp, cp, ok := p.colPlan(table, column)
	if !ok {
		t.Fatalf("%s.%s is not a planned column", table, column)
	}
	out := make([]string, rows)
	for i := 0; i < rows; i++ {
		out[i] = p.cellLiteral(tp, cp, i, 0)
	}
	return out
}

// apiKeysTable is the real control-plane shape minus its foreign key: a UNIQUE
// opaque hash, a second (non-unique) opaque column, and the partial unique
// index that says ONE ACTIVE key per user.
func apiKeysTable() schemadef.Table {
	return schemadef.Table{
		Name:   "reliant_api_keys",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			idCol(), textCol("user_id"), bytesCol("key_hash"),
			bytesCol("key_plaintext_encrypted"),
			{Name: "revoked_at", DeclType: "TIMESTAMPTZ", Type: schemadef.TypeTime},
		},
		Indexes: []schemadef.Index{
			{Name: "reliant_api_keys_key_hash_key", Columns: []string{"key_hash"}, Unique: true},
			{Name: "idx_cp_reliant_api_keys_user_active", Columns: []string{"user_id"}, Unique: true,
				Predicate: "(revoked_at IS NULL)"},
		},
	}
}

// The measured failure: `key_hash BYTEA NOT NULL UNIQUE` seeded one row.
func TestBytea_UniqueColumnCarriesTheFullRowTarget(t *testing.T) {
	const rows = 12
	p := planFor(t, apiKeysTable(), rows)
	if warns := p.Warnings(); len(warns) > 0 {
		t.Fatalf("an opaque UNIQUE column forge can vary must produce no cap warning, got:\n  %s",
			strings.Join(warns, "\n  "))
	}
	if got := p.rowsOf["reliant_api_keys"]; got != rows {
		t.Fatalf("reliant_api_keys holds %d row(s), want %d", got, rows)
	}
	seen := map[string]int{}
	for i, lit := range byteaCells(t, p, "reliant_api_keys", "key_hash", rows) {
		if prev, dup := seen[lit]; dup {
			t.Errorf("rows %d and %d both carry key_hash %s — a UNIQUE column cannot repeat a value", prev, i, lit)
		}
		seen[lit] = i
		// Self-evidently synthetic: the bytes decode to the emitter's own
		// stamp, which no real hash ever does.
		if raw := string(decodeBytea(t, lit)); !strings.HasPrefix(raw, SyntheticStringPrefix) {
			t.Errorf("row %d: key_hash decodes to %q, which does not carry the %q stamp", i, raw, SyntheticStringPrefix)
		}
	}
	if len(seen) != rows {
		t.Errorf("key_hash carries %d distinct value(s) across %d rows, want %d", len(seen), rows, rows)
	}
}

// Determinism is the contract the whole planner rests on, and the fix must not
// have bought variation with a PRNG.
func TestBytea_IsDeterministicAndAppendOnly(t *testing.T) {
	tbl := apiKeysTable()
	a := byteaCells(t, planFor(t, tbl, 6), "reliant_api_keys", "key_hash", 6)
	b := byteaCells(t, planFor(t, tbl, 6), "reliant_api_keys", "key_hash", 6)
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("row %d rendered %s on one run and %s on the next — bytea synthesis must be a pure function of (column, row)", i, a[i], b[i])
		}
	}
	wide := byteaCells(t, planFor(t, tbl, 20), "reliant_api_keys", "key_hash", 6)
	for i := range a {
		if a[i] != wide[i] {
			t.Errorf("row %d moved when the row target grew: %s -> %s", i, a[i], wide[i])
		}
	}
}

// A width the schema DECLARES is still honored, and honoring it must not cost
// the variation — truncation cuts off exactly the part that varies, so the
// discriminator is re-applied at the tail.
func TestBytea_RespectsADeclaredLengthAndStaysDistinct(t *testing.T) {
	const rows = 15
	tbl := schemadef.Table{
		Name:    "tokens",
		PKCols:  []string{"id"},
		Columns: []schemadef.Column{idCol(), bytesCol("digest")},
		Indexes: []schemadef.Index{{Name: "tokens_digest_key", Columns: []string{"digest"}, Unique: true}},
		Checks: []schemadef.CheckConstraint{
			check("tokens_digest_width", "CHECK ((length(digest) = 8))", "digest"),
		},
	}
	p := planFor(t, tbl, rows)
	seen := map[string]bool{}
	for i, lit := range byteaCells(t, p, "tokens", "digest", rows) {
		raw := decodeBytea(t, lit)
		if len(raw) != 8 {
			t.Errorf("row %d: digest is %d byte(s) (%q), want 8 — CHECK (length(digest) = 8)", i, len(raw), raw)
		}
		if seen[lit] {
			t.Errorf("row %d repeats digest %s under a UNIQUE index", i, lit)
		}
		seen[lit] = true
	}
}

// A bytea column with no UNIQUE index still varies — the fix is in synthesis,
// not in the distinct-value assignment, so two rows of the same table never
// carry identical opaque payloads by accident.
func TestBytea_PlainColumnVariesToo(t *testing.T) {
	p := planFor(t, apiKeysTable(), 5)
	lits := byteaCells(t, p, "reliant_api_keys", "key_plaintext_encrypted", 5)
	seen := map[string]bool{}
	for _, lit := range lits {
		seen[lit] = true
	}
	if len(seen) != len(lits) {
		t.Errorf("a plain bytea column carried %d distinct value(s) across %d rows: %v", len(seen), len(lits), lits)
	}
}

// ── Partial unique indexes ────────────────────────────────────────────────

// The over-constraint: `UNIQUE (user_id) WHERE revoked_at IS NULL` says ONE
// ACTIVE key per user. Read as a plain `UNIQUE (user_id)` it caps the table at
// one row per user — a dataset in which no user can ever hold a revoked key
// plus a live one, which is most of what such a table is for.
func TestPartialUniqueIndex_DoesNotMakeAForeignKeyOneToOne(t *testing.T) {
	users := schemadef.Table{
		Name: "users", PKCols: []string{"id"},
		Columns: []schemadef.Column{idCol(), textCol("email")},
	}
	keys := apiKeysTable()
	keys.Columns[1] = schemadef.Column{Name: "user_id", DeclType: "TEXT", Type: schemadef.TypeString, NotNull: true}
	keys.ForeignKeys = []schemadef.ForeignKey{
		{Column: "user_id", RefTable: "users", RefColumn: "id", Name: "reliant_api_keys_user_id_fkey"},
	}
	tables := []schemadef.Table{users, keys}
	p, err := BuildPlan(tables, PoolsFromTables(tables), Config{Rows: 3, Salt: 1, RowsPerTable: map[string]int{"reliant_api_keys": 9}})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	p.SetBounds(BoundsFromTables(tables))
	if got := p.rowsOf["reliant_api_keys"]; got != 9 {
		t.Fatalf("reliant_api_keys holds %d row(s), want 9 — a partial unique index must not cap the table at the parent's row count", got)
	}
	owners := map[string]int{}
	for row := 0; row < 9; row++ {
		v, ok := p.SeedValue("reliant_api_keys", "user_id", row)
		if !ok {
			t.Fatalf("row %d: user_id is not seeded", row)
		}
		owners[v]++
	}
	if len(owners) >= 9 {
		t.Errorf("every seeded key belongs to a different user (%d distinct owners over 9 rows) — the partial index was read as a plain UNIQUE", len(owners))
	}
}

// The other direction, and the one that must NOT change: on a soft-deleting
// table `WHERE deleted_at IS NULL` is true of every row the seeder writes (it
// seeds LIVE rows), so the index binds and the column keeps drawing distinct
// values.
func TestPartialUniqueIndex_SoftDeletePredicateStillBinds(t *testing.T) {
	const rows = 6
	tbl := schemadef.Table{
		Name: "accounts", PKCols: []string{"id"},
		Columns: []schemadef.Column{
			idCol(), textCol("email"),
			{Name: "created_at", DeclType: "TIMESTAMPTZ", Type: schemadef.TypeTime, NotNull: true},
			{Name: "updated_at", DeclType: "TIMESTAMPTZ", Type: schemadef.TypeTime, NotNull: true},
			{Name: "deleted_at", DeclType: "TIMESTAMPTZ", Type: schemadef.TypeTime},
		},
		Indexes: []schemadef.Index{
			{Name: "accounts_live_email", Columns: []string{"email"}, Unique: true, Predicate: "(deleted_at IS NULL)"},
		},
	}
	if !uniqueSingleColumn(tbl, "email") {
		t.Fatal("a partial unique index whose predicate is TRUE of every seeded row must still be treated as UNIQUE")
	}
	p := planFor(t, tbl, rows)
	seen := map[string]bool{}
	for row := 0; row < rows; row++ {
		v, _ := p.SeedValue("accounts", "email", row)
		if seen[v] {
			t.Errorf("row %d repeats email %q while deleted_at IS NULL on every seeded row", row, v)
		}
		seen[v] = true
	}
}

// The fallback is the strict one. A predicate this package does not read binds,
// which is exactly the behaviour that existed before partial indexes were
// looked at — an unqualified claim is worse than no claim.
func TestPartialIndexBinds_Fallbacks(t *testing.T) {
	base := func(pred string) (schemadef.Table, schemadef.Index) {
		return schemadef.Table{
			Name: "t", PKCols: []string{"id"},
			Columns: []schemadef.Column{
				idCol(), textCol("email"),
				{Name: "revoked_at", DeclType: "TIMESTAMPTZ", Type: schemadef.TypeTime},
				{Name: "deleted_at", DeclType: "TIMESTAMPTZ", Type: schemadef.TypeTime},
				{Name: "created_at", DeclType: "TIMESTAMPTZ", Type: schemadef.TypeTime, NotNull: true},
				{Name: "updated_at", DeclType: "TIMESTAMPTZ", Type: schemadef.TypeTime, NotNull: true},
			},
		}, schemadef.Index{
			Name: "ix", Columns: []string{"email"}, Unique: true, Predicate: pred,
		}
	}
	cases := []struct {
		name string
		pred string
		want bool
	}{
		{name: "a full index always binds", pred: "", want: true},
		{name: "a predicate forge cannot read binds", pred: "(status = 'active'::text)", want: true},
		{name: "a predicate over a column that is not on the table binds", pred: "(missing IS NULL)", want: true},
		{name: "IS NULL over a column forge always fills does not bind", pred: "(revoked_at IS NULL)", want: false},
		{name: "IS NOT NULL over a column forge always fills binds", pred: "(revoked_at IS NOT NULL)", want: true},
		{name: "IS NULL over the soft-delete marker binds", pred: "(deleted_at IS NULL)", want: true},
		{name: "IS NOT NULL over the soft-delete marker does not bind", pred: "(deleted_at IS NOT NULL)", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tbl, ix := base(tc.pred)
			if got := partialIndexBinds(tbl, ix); got != tc.want {
				t.Errorf("partialIndexBinds(%q) = %t, want %t", tc.pred, got, tc.want)
			}
		})
	}
}

// A predicate over a FOREIGN KEY binds: an optional reference declines on
// roughly one row in five, so forge cannot show the predicate false of every
// row and must keep the strict reading.
func TestPartialIndexBinds_ForeignKeyPredicateIsConservative(t *testing.T) {
	tbl := schemadef.Table{
		Name: "memberships", PKCols: []string{"id"},
		Columns: []schemadef.Column{
			idCol(), textCol("email"),
			{Name: "team_id", DeclType: "TEXT", Type: schemadef.TypeString},
		},
		ForeignKeys: []schemadef.ForeignKey{{Column: "team_id", RefTable: "teams", RefColumn: "id", Name: "fk"}},
	}
	ix := schemadef.Index{Name: "ix", Columns: []string{"email"}, Unique: true, Predicate: "(team_id IS NULL)"}
	if !partialIndexBinds(tbl, ix) {
		t.Error("a predicate over a foreign key must bind — an optional edge is NULL on some rows and not on others")
	}
}

// apiKeysMigration is the real control-plane table, verbatim except for the
// schema qualification and the users table it references.
const apiKeysMigration = `
CREATE TABLE users (
    id TEXT PRIMARY KEY,
    email TEXT NOT NULL UNIQUE
);

CREATE TABLE reliant_api_keys (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key_hash BYTEA NOT NULL UNIQUE,
    key_plaintext_encrypted BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX idx_cp_reliant_api_keys_user_active
    ON reliant_api_keys(user_id) WHERE revoked_at IS NULL;
`

// Real postgres is the judge for both halves at once: it enforces the plain
// UNIQUE on key_hash and the partial one on user_id, so a landed INSERT of the
// full row target IS the proof that the opaque column varies and that the
// partial index was read correctly.
func TestMaterialize_OpaqueUniqueAndPartialIndexOnRealPostgres(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	if _, err := db.Exec(apiKeysMigration); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "00001_init.up.sql"), []byte(apiKeysMigration), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Materialize(ctx, db, dir, "", Config{Rows: 12, Salt: 1}); err != nil {
		t.Fatalf("%v", err)
	}
	var n, distinct int
	if err := db.QueryRow(`SELECT count(*), count(DISTINCT key_hash) FROM reliant_api_keys`).Scan(&n, &distinct); err != nil {
		t.Fatal(err)
	}
	if n != 12 {
		t.Errorf("reliant_api_keys holds %d row(s), want 12 — an opaque UNIQUE column must not cap the table", n)
	}
	if distinct != n {
		t.Errorf("%d row(s) carry only %d distinct key_hash value(s)", n, distinct)
	}
}
