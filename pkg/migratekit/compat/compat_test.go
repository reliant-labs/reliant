package compat

import (
	"testing"
	"testing/fstest"
)

// TestDeclaredMatchesOnlyALineComment pins what counts as a declaration.
//
// The negative cases are the ones that matter. A migration that DISCUSSES
// compatibility in a block comment, or a string literal that happens to
// contain the words, must not be read as having claimed it: a false positive
// here lets the previous release serve on a schema its code cannot
// run against.
func TestDeclaredMatchesOnlyALineComment(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want bool
	}{
		{"exact", Directive + "\nALTER TABLE t ADD COLUMN c TEXT;", true},
		{"indented and spaced", "  --   forge:backward-compatible\nSELECT 1;", true},
		{"case-insensitive", "-- FORGE:BACKWARD-COMPATIBLE\n", true},
		{"after other statements", "ALTER TABLE t ADD COLUMN c TEXT;\n-- forge:backward-compatible\n", true},
		{"absent", "ALTER TABLE t ADD COLUMN c TEXT;", false},
		{"inside a block comment", "/*\n forge:backward-compatible \n*/\nSELECT 1;", false},
		{"inside a string", "SELECT '-- forge:backward-compatible';", false},
		{"with a rationale after it", "-- forge:backward-compatible — additive column, N-1 never reads it\n", true},
		{"a longer word", "-- forge:backward-compatible-ish\n", false},
		{"trailing on a statement", "SELECT 1; -- forge:backward-compatible", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Declared(tc.sql); got != tc.want {
				t.Errorf("Declared(%q) = %v, want %v", tc.sql, got, tc.want)
			}
		})
	}
}

// TestScanOrdersByVersionAndReadsOnlyUpFiles covers the file grammar: only
// `<digits>_<name>.up.<ext>` files are migrations, down files never carry the
// contract (it is a statement about what the UP leaves behind), and the order
// is numeric rather than lexical — 10 sorts after 9.
func TestScanOrdersByVersionAndReadsOnlyUpFiles(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/10_ten.up.sql":    {Data: []byte(Directive + "\n")},
		"migrations/10_ten.down.sql":  {Data: []byte(Directive + "\n")},
		"migrations/9_nine.up.sql":    {Data: []byte("DROP TABLE x;")},
		"migrations/.gitkeep":         {Data: nil},
		"migrations/README.md":        {Data: []byte("# notes")},
		"migrations/00001_one.up.sql": {Data: []byte("CREATE TABLE a (id INT);")},
	}
	got, err := Scan(fsys, "migrations")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	want := []Migration{
		{Version: 1, Name: "00001_one.up.sql", Descriptor: "one"},
		{Version: 9, Name: "9_nine.up.sql", Descriptor: "nine"},
		{Version: 10, Name: "10_ten.up.sql", Descriptor: "ten", BackwardCompatible: true},
	}
	if len(got) != len(want) {
		t.Fatalf("Scan = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Scan[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestScanDistinguishesMissingFromEmpty: no migrations is a normal state, a
// directory that cannot be read is a packaging bug — they must not collapse.
func TestScanDistinguishesMissingFromEmpty(t *testing.T) {
	if got, err := Scan(fstest.MapFS{"migrations/.gitkeep": {}}, "migrations"); err != nil || len(got) != 0 {
		t.Errorf("empty dir: Scan = %v, %v; want no migrations and no error", got, err)
	}
	if _, err := Scan(fstest.MapFS{}, "migrations"); err == nil {
		t.Error("missing dir: Scan returned no error; an unreadable migration set must not read as empty")
	}
}

func TestDeclarations_ReasonsAndBreaksRollback(t *testing.T) {
	compatible, breaks := Declarations("-- forge:backward-compatible — additive column\n" +
		"SELECT 1;\n" +
		"--  FORGE:breaks-rollback: drops y\n" +
		"-- forge:breaks-rollback-ish not this one\n" +
		"--   -- forge:breaks-rollback an example in prose, not a directive\n")
	if len(compatible) != 1 || compatible[0] != (Declaration{Line: 1, Reason: "additive column"}) {
		t.Errorf("compatible = %+v", compatible)
	}
	if len(breaks) != 1 || breaks[0] != (Declaration{Line: 3, Reason: "drops y"}) {
		t.Errorf("breaks-rollback = %+v", breaks)
	}
	if _, b := Declarations("-- forge:breaks-rollback <why, deliberately, it does not>\n"); len(b) != 1 || b[0].Reason != "" {
		t.Errorf("a <placeholder> is not a reason: %+v", b)
	}
}

// A file that declares both is read the safe way: not compatible.
func TestDeclared_ContradictionIsNotCompatible(t *testing.T) {
	if Declared("-- forge:backward-compatible\n-- forge:breaks-rollback drops y\n") {
		t.Error("a migration that also declares breaks-rollback was read as backward-compatible")
	}
	if !Declared("-- forge:backward-compatible\n") {
		t.Error("the plain declaration stopped counting")
	}
}
