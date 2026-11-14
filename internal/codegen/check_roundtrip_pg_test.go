package codegen

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/schemadef"
	"github.com/reliant-labs/forge/pkg/seedplan"
)

// The CHECK expression is a SERIALIZATION FORMAT that forge both writes and
// reads, and until this test nothing held the two ends together.
//
// FieldConstraints.SQLChecks is the sole WRITER: one protovalidate rule set
// projects to a `CHECK (...)` in the born migration. But seeddata READS those
// same expressions back with its own hand-rolled regexes — LengthBounds /
// BoundsFromTables / PoolsFromTables, so synthesized rows satisfy the
// constraints. A reader can silently stop matching the writer and NOTHING
// fails — the seed just produces rows the DB rejects.
//
// That is not hypothetical. seeddata's LengthBounds had a declared-varchar
// branch that had NEVER matched a live schema: it keyed off DeclType, which is
// rebuilt from udt_name and spells both `varchar` and `varchar(4)` as
// "VARCHAR". It shipped, and the bug only surfaced when `forge db seed` began
// hard-aborting on CHECKs forge itself had emitted.
//
// The existing pg tests do not close this: they apply HAND-WRITTEN DDL that
// approximates forge's output, so a change to the emitter's spelling leaves
// them green. This test generates the DDL FROM THE EMITTER, applies it to a
// REAL postgres, introspects it back through the production path
// (schemadef.ApplyAndIntrospect, which reads pg_get_constraintdef), and
// asserts each reader recovers the constraint the emitter was given.
//
// That round trip is the only thing that can catch the two failure modes that
// matter, because postgres CANONICALIZES what it is given — `CHECK (n >= 0)`
// comes back as `CHECK ((n >= 0))`, and on a varchar column `char_length(c)`
// comes back as `char_length((c)::text)`. A reader tested only against the
// emitter's literal output never sees the form it will actually meet.

// checkRoundTripCase is one constraint shape the emitter can produce, paired
// with what each reader must recover from it after a real postgres round trip.
type checkRoundTripCase struct {
	name string
	// kind is the proto scalar kind driving SQLChecks' numeric/string split.
	kind string
	// sqlType is the column's declared SQL type.
	sqlType string
	// constraints is the rule set handed to the WRITER.
	constraints FieldConstraints
	// wantMinLen/wantMaxLen are what seedplan.LengthBounds must recover.
	// -1 means "reader must report unbounded on that side".
	wantMinLen int
	wantMaxLen int
	// wantNumMin/wantNumMax are what seedplan.BoundsFromTables must recover.
	wantNumMin *int64
	wantNumMax *int64
}

func i64(v int64) *int64 { return &v }

// TestCheckExpressions_SurviveRoundTripThroughPostgres pins that every CHECK
// the emitter writes is still understood by the readers after postgres has
// canonicalized it.
func TestCheckExpressions_SurviveRoundTripThroughPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("applies migrations to a real postgres; skipped under -short")
	}

	cases := []checkRoundTripCase{
		{
			name:        "numeric gte",
			kind:        "int64",
			sqlType:     "BIGINT",
			constraints: FieldConstraints{Gte: "0"},
			wantMinLen:  -1, wantMaxLen: -1,
			wantNumMin: i64(0),
		},
		{
			name:        "numeric gte and lte",
			kind:        "int64",
			sqlType:     "BIGINT",
			constraints: FieldConstraints{Gte: "1", Lte: "50"},
			wantMinLen:  -1, wantMaxLen: -1,
			wantNumMin: i64(1), wantNumMax: i64(50),
		},
		{
			name:        "string max_len on TEXT",
			kind:        "string",
			sqlType:     "TEXT",
			constraints: FieldConstraints{MaxLen: u64(10)},
			wantMinLen:  -1, wantMaxLen: 10,
		},
		{
			// The shape that regressed: char_length on a VARCHAR column comes
			// back from postgres as char_length((col)::text).
			name:        "string max_len on VARCHAR",
			kind:        "string",
			sqlType:     "VARCHAR(64)",
			constraints: FieldConstraints{MaxLen: u64(10)},
			wantMinLen:  -1, wantMaxLen: 10,
		},
		{
			name:        "string min and max len",
			kind:        "string",
			sqlType:     "TEXT",
			constraints: FieldConstraints{MinLen: u64(2), MaxLen: u64(8)},
			wantMinLen:  2, wantMaxLen: 8,
		},
		{
			// min==max collapses to `char_length(col) = N`, a DIFFERENT
			// spelling the readers must also understand.
			name:        "string exact len",
			kind:        "string",
			sqlType:     "TEXT",
			constraints: FieldConstraints{MinLen: u64(3), MaxLen: u64(3)},
			wantMinLen:  3, wantMaxLen: 3,
		},
		{
			name:        "required string folds to min length 1",
			kind:        "string",
			sqlType:     "TEXT",
			constraints: FieldConstraints{Required: true},
			wantMinLen:  1, wantMaxLen: -1,
		},
	}

	// One table per case keeps a failure attributable to its own shape.
	var ddl strings.Builder
	tableFor := func(i int) string { return fmt.Sprintf("rt_%d", i) }
	for i, tc := range cases {
		checks := tc.constraints.SQLChecks("val", tc.kind)
		if len(checks) == 0 {
			t.Fatalf("case %q: SQLChecks produced NOTHING — the emitter no longer projects this rule set, "+
				"so this case is asserting on a shape forge cannot emit", tc.name)
		}
		fmt.Fprintf(&ddl, "CREATE TABLE %s (\n    id TEXT PRIMARY KEY,\n    val %s NOT NULL %s\n);\n\n",
			tableFor(i), tc.sqlType, strings.Join(checks, " "))
	}
	t.Logf("emitter-generated DDL under test:\n%s", ddl.String())

	migDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(migDir, "001_roundtrip.up.sql"), []byte(ddl.String()), 0o644); err != nil {
		t.Fatalf("write migration: %v", err)
	}

	tables, err := schemadef.ApplyAndIntrospect(migDir)
	if err != nil {
		t.Skipf("no reachable postgres for the round trip (%v) — "+
			"this test is the only thing holding the CHECK writer and readers together, so do not leave it skipped in CI", err)
	}
	if len(tables) != len(cases) {
		t.Fatalf("introspected %d tables, want %d — the migration did not apply as written", len(tables), len(cases))
	}

	byName := map[string]schemadef.Table{}
	for _, tb := range tables {
		byName[tb.Name] = tb
	}

	// Readers consume the whole table set, exactly as production does.
	bounds := seedplan.BoundsFromTables(tables)

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tbl, ok := byName[tableFor(i)]
			if !ok {
				t.Fatalf("table %s missing from introspection", tableFor(i))
			}
			var col schemadef.Column
			for _, c := range tbl.Columns {
				if c.Name == "val" {
					col = c
				}
			}

			// Show what postgres actually stored — the canonicalized form is
			// the thing readers must cope with, and seeing it makes a failure
			// diagnosable instead of mysterious.
			for _, ck := range tbl.Checks {
				t.Logf("postgres canonicalized: %s", ck.Def)
			}

			gotMin, gotMax := seedplan.LengthBounds(tbl, col)
			assertBound(t, "LengthBounds min", gotMin, tc.wantMinLen)
			assertBound(t, "LengthBounds max", gotMax, tc.wantMaxLen)

			nb := bounds[tableFor(i)]["val"]
			assertNumBound(t, "BoundsFromTables min", nb.Min, tc.wantNumMin)
			assertNumBound(t, "BoundsFromTables max", nb.Max, tc.wantNumMax)
		})
	}
}

// assertBound compares a reader's recovered length bound. want -1 means the
// reader must report unbounded, which LengthBounds spells as 0.
func assertBound(t *testing.T, label string, got, want int) {
	t.Helper()
	if want == -1 {
		if got != 0 {
			t.Errorf("%s = %d, want unbounded (0) — the reader invented a bound the emitter never wrote", label, got)
		}
		return
	}
	if got != want {
		t.Errorf("%s = %d, want %d — the reader did not recover the bound the emitter wrote. "+
			"Writer and reader have drifted; see the canonicalized CHECK logged above", label, got, want)
	}
}

func assertNumBound(t *testing.T, label string, got, want *int64) {
	t.Helper()
	switch {
	case want == nil && got != nil:
		t.Errorf("%s = %d, want unbounded — the reader invented a bound the emitter never wrote", label, *got)
	case want != nil && got == nil:
		t.Errorf("%s = unbounded, want %d — the reader did not recover the bound the emitter wrote. "+
			"Writer and reader have drifted; see the canonicalized CHECK logged above", label, *want)
	case want != nil && got != nil && *got != *want:
		t.Errorf("%s = %d, want %d", label, *got, *want)
	}
}
