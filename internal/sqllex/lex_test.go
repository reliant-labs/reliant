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

func words(src string) [][]string {
	var out [][]string
	for _, s := range Split(src) {
		out = append(out, s.Words)
	}
	return out
}

// A `;` only ends a statement at the top level: never inside a string,
// quoted identifier, dollar quote, comment or parenthesis.
func TestSplitSemicolonOnlyEndsTopLevelStatements(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string // trimmed statement texts
	}{
		{"semicolon in string", "SELECT 'a;b'; SELECT 2", []string{"SELECT 'a;b'", "SELECT 2"}},
		{"doubled quote stays in the string", "SELECT 'it''s;'; SELECT 2", []string{"SELECT 'it''s;'", "SELECT 2"}},
		{"escape string with backslash quote", `SELECT E'a\';b'; SELECT 2`, []string{`SELECT E'a\';b'`, "SELECT 2"}},
		{"semicolon in quoted identifier", `SELECT "a;b"; SELECT 2`, []string{`SELECT "a;b"`, "SELECT 2"}},
		{"semicolon in dollar quote", "SELECT $$a;b$$; SELECT 2", []string{"SELECT $$a;b$$", "SELECT 2"}},
		{"semicolon in tagged dollar quote", "SELECT $f$a;$$b$f$; SELECT 2", []string{"SELECT $f$a;$$b$f$", "SELECT 2"}},
		{"semicolon in line comment", "SELECT 1 -- a;b\n; SELECT 2", []string{"SELECT 1 -- a;b", "SELECT 2"}},
		{"semicolon in nested block comment", "SELECT 1 /* a /* ; */ ; */; SELECT 2", []string{"SELECT 1 /* a /* ; */ ; */", "SELECT 2"}},
		{"semicolon in parentheses", "SELECT (1;2); SELECT 3", []string{"SELECT (1;2)", "SELECT 3"}},
		{"empty statements are dropped", ";;SELECT 1;;;SELECT 2;", []string{"SELECT 1", "SELECT 2"}},
		{"unterminated last statement still counts", "SELECT 1; SELECT 2", []string{"SELECT 1", "SELECT 2"}},
		{"only comments is no statement", "-- nothing\n/* here */", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, s := range Split(tc.src) {
				got = append(got, strings.TrimSpace(tc.src[s.Start:s.End]))
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("Split(%q) statements:\n got %q\nwant %q", tc.src, got, tc.want)
			}
		})
	}
}

// Words are lowercased keywords and identifiers at any depth; quoted
// identifiers keep their quotes so "vacuum" is never the VACUUM keyword.
func TestSplitWordsAndStrings(t *testing.T) {
	src := `ALTER TABLE "Vacuum" ADD COLUMN s text DEFAULT 'it''s' CHECK (s <> E'a\'b'); -- x`
	got := Split(src)
	if len(got) != 1 {
		t.Fatalf("got %d statements, want 1: %+v", len(got), got)
	}
	wantWords := []string{"alter", "table", `"Vacuum"`, "add", "column", "s", "text", "default", "check", "s"}
	if !slices.Equal(got[0].Words, wantWords) {
		t.Errorf("Words = %q, want %q", got[0].Words, wantWords)
	}
	wantStrings := []string{"it's", "a'b"}
	if !slices.Equal(got[0].Strings, wantStrings) {
		t.Errorf("Strings = %q, want %q", got[0].Strings, wantStrings)
	}
}

// A `$1` parameter and a `$` inside an identifier are not dollar quotes, and
// an e/E that continues an identifier does not open an escape string.
func TestSplitDollarAndEscapePrefixAreNotAlwaysQuotes(t *testing.T) {
	got := words("SELECT $1; SELECT a$b; SELECT name'x'")
	want := [][]string{{"select"}, {"select", "a$b"}, {"select", "name"}}
	if len(got) != len(want) {
		t.Fatalf("statements = %q, want %q", got, want)
	}
	for i := range want {
		if !slices.Equal(got[i], want[i]) {
			t.Errorf("statement %d words = %q, want %q", i, got[i], want[i])
		}
	}
}

// psql keeps a CREATE FUNCTION|PROCEDURE ... BEGIN ATOMIC ... END body (and
// any CASE ... END inside it) as one statement.
func TestSplitRoutineBodies(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want int
	}{
		{"create function begin atomic", "CREATE FUNCTION f() RETURNS int LANGUAGE sql BEGIN ATOMIC SELECT 1; SELECT 2; END; SELECT 3", 2},
		{"create or replace procedure", "CREATE OR REPLACE PROCEDURE p() LANGUAGE sql BEGIN ATOMIC SELECT 1; END; SELECT 2", 2},
		{"case end inside a body", "CREATE FUNCTION f() RETURNS int LANGUAGE sql BEGIN ATOMIC SELECT CASE WHEN true THEN 1 END; SELECT 2; END; SELECT 3", 2},
		{"begin in a plain statement does not open a body", "BEGIN; SELECT 1; COMMIT", 3},
		{"create table is not a routine", "CREATE TABLE begin (id int); SELECT 1", 2},
		{"create or replace view is not a routine", "CREATE OR REPLACE VIEW v AS SELECT 1; SELECT 2", 2},
		{"create or replace function without body", "CREATE OR REPLACE FUNCTION f() RETURNS int AS $$ SELECT 1 $$ LANGUAGE sql; SELECT 2", 2},
		{"unterminated body keeps the rest", "CREATE FUNCTION f() RETURNS int LANGUAGE sql BEGIN ATOMIC SELECT 1; SELECT 2;", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(Split(tc.src)); got != tc.want {
				t.Errorf("Split(%q) = %d statements, want %d", tc.src, got, tc.want)
			}
		})
	}
}

func TestBlockCommentEnd(t *testing.T) {
	cases := []struct {
		name string
		s    string
		i    int
		want int
	}{
		{"simple", "/* a */ x", 0, 7},
		{"nested", "/* a /* b */ c */ x", 0, 17},
		{"unterminated runs to the end", "/* a /* b */ c", 0, 14},
		{"starts mid string", "xx/**/yy", 2, 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := BlockCommentEnd(tc.s, tc.i); got != tc.want {
				t.Errorf("BlockCommentEnd(%q, %d) = %d, want %d", tc.s, tc.i, got, tc.want)
			}
		})
	}
}

func TestQuotedEnd(t *testing.T) {
	cases := []struct {
		name      string
		s         string
		q         byte
		backslash bool
		start     int
		want      int
	}{
		{"simple", `'abc' x`, '\'', false, 0, 5},
		{"doubled quote", `'a''b' x`, '\'', false, 0, 6},
		{"doubled quote at end of input", `'a''`, '\'', false, 0, 4},
		{"backslash is literal without E", `'a\' x`, '\'', false, 0, 4},
		{"backslash escapes quote with E", `'a\'b' x`, '\'', true, 0, 6},
		{"escaped backslash then close", `'a\\' x`, '\'', true, 0, 5},
		{"trailing backslash runs to the end", `'a\`, '\'', true, 0, 3},
		{"unterminated", `'abc`, '\'', false, 0, 4},
		{"quoted identifier", `"a""b" x`, '"', false, 0, 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := QuotedEnd(tc.s, tc.start, tc.q, tc.backslash); got != tc.want {
				t.Errorf("QuotedEnd(%q) = %d, want %d", tc.s, got, tc.want)
			}
		})
	}
}

func TestDollarQuoteEnd(t *testing.T) {
	cases := []struct {
		name string
		s    string
		want int
	}{
		{"anonymous", "$$ab$$ x", 6},
		{"tagged", "$t$a$$b$t$ x", 10},
		{"parameter is not a quote", "$1 x", 1},
		{"lone dollar", "$", 1},
		{"tag with a digit after the first character", "$a1$x$a1$ y", 9},
		{"tag with non-ASCII letters", "$é$x$é$", 9},
		{"tag may not start with a digit", "$1a$x$1a$", 1},
		{"unterminated body runs to the end", "$$abc", 5},
		{"tag closed by a space is not a quote", "$a b$", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DollarQuoteEnd(tc.s, 0); got != tc.want {
				t.Errorf("DollarQuoteEnd(%q, 0) = %d, want %d", tc.s, got, tc.want)
			}
		})
	}
}

func TestIdentifierClassification(t *testing.T) {
	for _, c := range []byte{'a', 'Z', '_', 0xc3} {
		if !IsIdentStart(c) || !IsIdentByte(c) {
			t.Errorf("%q should start and continue an identifier", c)
		}
	}
	for _, c := range []byte{'0', '9', '$'} {
		if IsIdentStart(c) {
			t.Errorf("%q must not start an identifier", c)
		}
		if !IsIdentByte(c) {
			t.Errorf("%q should continue an identifier", c)
		}
	}
	for _, c := range []byte{' ', '(', '\'', '-', '['} {
		if IsIdentByte(c) {
			t.Errorf("%q must not be part of an identifier", c)
		}
	}
	if IdentBefore("x$$", 0) {
		t.Error("IdentBefore(_, 0) must be false: nothing precedes the first byte")
	}
	if !IdentBefore("a$", 1) || IdentBefore(" $", 1) {
		t.Error("IdentBefore should be true only right after an identifier byte")
	}
}
