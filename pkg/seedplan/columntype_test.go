// File: pkg/seedplan/columntype_test.go
//
// The DECLARED type is a constraint like any other, and the one postgres
// checks FIRST — before a CHECK, before a key, before a foreign key. A value
// of the wrong type is not a row that breaks a rule; it is a row postgres
// cannot parse, and the seed being one transaction, it takes every other table
// down with it. These pin the two places forge was writing one: a uuid column
// synthesized as `sample_user_id_1`, and a key function whose only two arms
// were "integer" and "assume UUID".

package seedplan

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// uuidRE is postgres's own accepted spelling: eight-four-four-four-twelve
// lowercase hex digits.
var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func uuidCol(name string) schemadef.Column {
	return schemadef.Column{Name: name, DeclType: "UUID", Type: schemadef.TypeString, NotNull: true}
}

// apiKeysTypeTable is the real control-plane shape: a uuid key and a uuid
// column that references nothing (the owning service lives elsewhere).
func apiKeysTypeTable() schemadef.Table {
	return schemadef.Table{
		Name:   "api_keys",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			{Name: "id", DeclType: "UUID", Type: schemadef.TypeString, NotNull: true, IsPK: true},
			uuidCol("user_id"),
			{Name: "prefix", DeclType: "VARCHAR", MaxChars: 12, Type: schemadef.TypeString, NotNull: true},
		},
		Indexes: []schemadef.Index{
			{Name: "api_keys_prefix_key", Columns: []string{"prefix"}, Unique: true},
		},
	}
}

// The measured failure: `user_id UUID NOT NULL` seeded `sample_user_id_1`, and
// postgres answered `invalid input syntax for type uuid`.
func TestUUIDColumn_SynthesizesARealUUID(t *testing.T) {
	const rows = 8
	p := planFor(t, apiKeysTypeTable(), rows)
	seen := map[string]int{}
	for i := 0; i < rows; i++ {
		v, ok := p.SeedValue("api_keys", "user_id", i)
		if !ok {
			t.Fatalf("api_keys.user_id row %d has no seeded scalar value", i)
		}
		if !uuidRE.MatchString(v) {
			t.Fatalf("row %d: user_id = %q, which is not a uuid — postgres parses the type before it checks anything else", i, v)
		}
		// Self-evidently synthetic, in the only room a uuid leaves for it.
		if !strings.HasPrefix(v, SyntheticUUIDPrefix) {
			t.Errorf("row %d: user_id = %q, which does not carry the %q stamp", i, v, SyntheticUUIDPrefix)
		}
		if prev, dup := seen[v]; dup {
			t.Errorf("rows %d and %d carry the same invented uuid %s", prev, i, v)
		}
		seen[v] = i
	}
	// A KEY is not stamped: those ids are a documented stable surface.
	id, _ := p.SeedValue("api_keys", "id", 0)
	if strings.HasPrefix(id, SyntheticUUIDPrefix) {
		t.Errorf("api_keys.id = %q — stamping a key would re-spell every primary key in every seeded database", id)
	}
}

// Determinism is the contract the planner rests on: no PRNG, no uuid.New(),
// and adding rows appends rather than reshuffling.
func TestUUIDColumn_IsDeterministicAndAppendOnly(t *testing.T) {
	read := func(rows int, n int) []string {
		p := planFor(t, apiKeysTypeTable(), rows)
		out := make([]string, n)
		for i := range out {
			out[i], _ = p.SeedValue("api_keys", "user_id", i)
		}
		return out
	}
	a, b := read(6, 6), read(6, 6)
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("row %d rendered %s on one run and %s on the next — uuid synthesis must be a pure function of (table, column, row)", i, a[i], b[i])
		}
	}
	for i, v := range read(20, 6) {
		if v != a[i] {
			t.Errorf("row %d moved when the row target grew: %s -> %s", i, a[i], v)
		}
	}
	// The same column name in a different table must not collide: the key
	// mixes the table in.
	other := apiKeysTypeTable()
	other.Name = "session_keys"
	p := planFor(t, other, 1)
	v, _ := p.SeedValue("session_keys", "user_id", 0)
	if v == a[0] {
		t.Errorf("api_keys.user_id and session_keys.user_id both seeded %s — two tables are not one column", v)
	}
}

// rollupsTable is the real control-plane shape: a composite key whose members
// are a reference, a timestamp and a date, one of them carrying its own CHECK
// vocabulary.
func rollupsTable() schemadef.Table {
	return schemadef.Table{
		Name:   "usage_breakdowns",
		PKCols: []string{"org_id", "period_start", "usage_date", "group_by"},
		Columns: []schemadef.Column{
			{Name: "org_id", DeclType: "TEXT", Type: schemadef.TypeString, NotNull: true, IsPK: true},
			{Name: "period_start", DeclType: "TIMESTAMPTZ", Type: schemadef.TypeTime, NotNull: true, IsPK: true},
			{Name: "usage_date", DeclType: "DATE", Type: schemadef.TypeTime, NotNull: true, IsPK: true},
			{Name: "group_by", DeclType: "TEXT", Type: schemadef.TypeString, NotNull: true, IsPK: true},
			intCol("request_count"),
		},
		Checks: []schemadef.CheckConstraint{
			check("chk_group_by",
				"CHECK ((group_by = ANY (ARRAY['provider'::text, 'model'::text, 'access_mode'::text])))", "group_by"),
		},
	}
}

// A TIME key member used to take the "everything that is not an integer is a
// UUID" arm, and postgres answered
// `invalid input syntax for type timestamp with time zone`.
func TestKeyMember_TimeIsATimestampAndVariesPerRow(t *testing.T) {
	const rows = 10
	p := planFor(t, rollupsTable(), rows)
	seen := map[string]int{}
	for i := 0; i < rows; i++ {
		v, ok := p.SeedValue("usage_breakdowns", "period_start", i)
		if !ok {
			t.Fatalf("period_start row %d has no seeded scalar value", i)
		}
		if _, err := time.Parse(time.RFC3339, v); err != nil {
			t.Fatalf("row %d: period_start = %q, which is not a timestamp: %v", i, v, err)
		}
		if prev, dup := seen[v]; dup {
			t.Errorf("rows %d and %d share period_start %s — a key member must tell rows apart", prev, i, v)
		}
		seen[v] = i
	}
}

// A DATE member is rendered date-only. Postgres would accept a full instant,
// but a seed file is read by people too, and a date column whose every value
// reads `T08:00:00Z` says something about the column that is not true. The
// step is a DAY, because anything finer collapses on a date column.
func TestKeyMember_DateIsADateAndVariesPerRow(t *testing.T) {
	const rows = 10
	p := planFor(t, rollupsTable(), rows)
	seen := map[string]bool{}
	for i := 0; i < rows; i++ {
		v, _ := p.SeedValue("usage_breakdowns", "usage_date", i)
		if _, err := time.Parse("2006-01-02", v); err != nil {
			t.Fatalf("row %d: usage_date = %q, which is not a bare date: %v", i, v, err)
		}
		if seen[v] {
			t.Errorf("row %d repeats usage_date %s — a day step is what keeps a date key member distinct", i, v)
		}
		seen[v] = true
	}
}

// A key member is still an ordinary column, and its CHECK is a declaration
// about EVERY value it holds. Inventing an id for it trades a key collision
// (which ON CONFLICT absorbs) for a CHECK violation (which aborts the seed).
func TestKeyMember_HonorsItsOwnCheckVocabulary(t *testing.T) {
	const rows = 6
	p := planFor(t, rollupsTable(), rows)
	allowed := map[string]bool{"provider": true, "model": true, "access_mode": true}
	for i := 0; i < rows; i++ {
		v, _ := p.SeedValue("usage_breakdowns", "group_by", i)
		if !allowed[v] {
			t.Fatalf("row %d: group_by = %q, which its own CHECK vocabulary does not admit", i, v)
		}
	}
}

// A SOLE key column keeps the documented, stable UUID even when it carries a
// vocabulary: collapsing a table's ids into four strings is not a trade the
// key function gets to make on its own.
func TestKeyMember_SoleKeyKeepsItsStableID(t *testing.T) {
	tbl := schemadef.Table{
		Name:   "plans",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			{Name: "id", DeclType: "TEXT", Type: schemadef.TypeString, NotNull: true, IsPK: true},
			textCol("label"),
		},
		Checks: []schemadef.CheckConstraint{
			check("chk_id", "CHECK ((id = ANY (ARRAY['free'::text, 'pro'::text])))", "id"),
		},
	}
	p := planFor(t, tbl, 5)
	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		v, _ := p.SeedValue("plans", "id", i)
		if seen[v] {
			t.Fatalf("row %d repeats the sole key %q — a one-column key must stay distinct per row", i, v)
		}
		seen[v] = true
	}
}

// A foreign key re-derives the PARENT's own key literal, so the type defect
// reached references too: an edge onto a timestamp key member was handed a
// UUID. The value must be of the REFERENCED column's type, whatever that is.
func TestForeignKey_MatchesTheReferencedColumnsType(t *testing.T) {
	tables := []schemadef.Table{
		{
			Name:   "periods",
			PKCols: []string{"org_id", "period_start"},
			Columns: []schemadef.Column{
				{Name: "org_id", DeclType: "TEXT", Type: schemadef.TypeString, NotNull: true, IsPK: true},
				{Name: "period_start", DeclType: "TIMESTAMPTZ", Type: schemadef.TypeTime, NotNull: true, IsPK: true},
			},
			Indexes: []schemadef.Index{
				{Name: "periods_period_start_key", Columns: []string{"period_start"}, Unique: true},
			},
		},
		{
			Name:   "period_notes",
			PKCols: []string{"id"},
			Columns: []schemadef.Column{
				idCol(),
				{Name: "period_start", DeclType: "TIMESTAMPTZ", Type: schemadef.TypeTime, NotNull: true},
				textCol("note"),
			},
			ForeignKeys: []schemadef.ForeignKey{
				{Column: "period_start", RefTable: "periods", RefColumn: "period_start", Name: "period_notes_period_start_fkey"},
			},
		},
	}
	p := planForTables(t, tables, Config{Rows: 6, Salt: 1})
	for i := 0; i < p.rowsOf["period_notes"]; i++ {
		v, ok := p.SeedValue("period_notes", "period_start", i)
		if !ok {
			t.Fatalf("period_notes.period_start row %d has no seeded scalar value", i)
		}
		if _, err := time.Parse(time.RFC3339, v); err != nil {
			t.Fatalf("row %d: the reference onto a timestamp key member = %q, which is not a timestamp: %v", i, v, err)
		}
	}
}

// SynthString is the ONE string synthesizer both the seeder and the generated
// CRUD fixtures call, so the declared type has to be read there — a fixture
// that violated the column's type would fail the born test exactly as the seed
// failed the transaction.
func TestSynthString_ReadsTheDeclaredUUIDType(t *testing.T) {
	tbl := apiKeysTypeTable()
	got := SynthString(tbl, uuidCol("user_id"), 0)
	if !uuidRE.MatchString(got) {
		t.Fatalf("SynthString on a uuid column = %q, which is not a uuid", got)
	}
	// A TEXT column is untouched: the branch reads the DECLARED type, never
	// the column's name.
	if plain := SynthString(tbl, textCol("user_id"), 0); plain != SyntheticStringPrefix+"user_id_1" {
		t.Errorf("SynthString on a text column = %q, want the placeholder — the type is what selects, not the name", plain)
	}
}

// columnTypeMigration carries every shape the type switch newly has to answer
// for: a uuid data column, and a composite key of a reference, a timestamp, a
// date and a vocabulary-constrained text column.
const columnTypeMigration = `
CREATE TABLE orgs (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL
);
CREATE TABLE api_keys (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL,
    scopes UUID[] NOT NULL DEFAULT '{}',
    prefix VARCHAR(12) NOT NULL UNIQUE
);
CREATE TABLE usage_rollups (
    org_id TEXT NOT NULL REFERENCES orgs(id),
    period_start TIMESTAMPTZ NOT NULL,
    usage_date DATE NOT NULL,
    group_by TEXT NOT NULL CHECK (group_by IN ('provider', 'model', 'access_mode')),
    request_count BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (org_id, period_start, usage_date, group_by)
);
`

// Postgres is the authority on what a column can hold, and it parses the type
// before it evaluates anything else — so the INSERTs landing IS the proof.
func TestMaterialize_ColumnTypesOnRealPostgres(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	if _, err := db.Exec(columnTypeMigration); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "00001_init.up.sql"), []byte(columnTypeMigration), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Materialize(ctx, db, dir, "", Config{Rows: 12, Salt: 1}); err != nil {
		t.Fatalf("a mistyped value would surface here: %v", err)
	}
	for _, tc := range []struct {
		table string
		want  int
	}{{"api_keys", 12}, {"usage_rollups", 12}} {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM ` + tc.table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != tc.want {
			t.Errorf("%s holds %d row(s), want %d", tc.table, n, tc.want)
		}
	}
	// Distinct uuids, and distinct key members: a column that does not vary
	// is the other way a table quietly ends up with one row.
	var users int
	if err := db.QueryRow(`SELECT count(DISTINCT user_id) FROM api_keys`).Scan(&users); err != nil {
		t.Fatal(err)
	}
	if users != 12 {
		t.Errorf("api_keys carries %d distinct user_id(s) across 12 rows, want 12", users)
	}
}
