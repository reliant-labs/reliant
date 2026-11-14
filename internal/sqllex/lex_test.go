package sqllex

import (
	"slices"
	"strings"
	"testing"
)

// Split's spans are what `forge db analyze` executes, so src[Start:End] must
// be exactly the statement: from its first token to the `;` that ends it,
// with no leading comment and no terminator.
func TestSplitSpansAreTheStatementText(t *testing.T) {
	src := "-- header\nCREATE TABLE t (id int);\n\n/* note */ ALTER TABLE t\n  ADD COLUMN s text DEFAULT ';';\nDO $$ BEGIN PERFORM 1; END $$;\nSELECT 1"
	var got []string
	var lines []int
	for _, s := range Split(src) {
		got = append(got, strings.TrimSpace(src[s.Start:s.End]))
		lines = append(lines, s.Line)
	}
	want := []string{
		"CREATE TABLE t (id int)",
		"ALTER TABLE t\n  ADD COLUMN s text DEFAULT ';'",
		"DO $$ BEGIN PERFORM 1; END $$",
		"SELECT 1",
	}
	if !slices.Equal(got, want) {
		t.Errorf("statement text:\n got %q\nwant %q", got, want)
	}
	if wantLines := []int{2, 4, 6, 7}; !slices.Equal(lines, wantLines) {
		t.Errorf("lines = %v, want %v", lines, wantLines)
	}
}

func TestNonTransactional(t *testing.T) {
	cases := map[string]string{
		"CREATE INDEX CONCURRENTLY i ON t (a)":        "CREATE INDEX CONCURRENTLY",
		"CREATE UNIQUE INDEX CONCURRENTLY i ON t (a)": "CREATE INDEX CONCURRENTLY",
		"DROP INDEX CONCURRENTLY i":                   "DROP INDEX CONCURRENTLY",
		"CREATE INDEX i ON t (a)":                     "",
		"VACUUM t":                                    "VACUUM",
	}
	for sql, want := range cases {
		if got := NonTransactional(Split(sql)[0].Words); got != want {
			t.Errorf("NonTransactional(%q) = %q, want %q", sql, got, want)
		}
	}
}
