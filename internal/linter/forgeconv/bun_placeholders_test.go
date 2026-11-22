package forgeconv

import (
	"fmt"
	"path/filepath"
	"slices"
	"testing"
)

func bunFindings(t *testing.T, dir string) []string {
	t.Helper()
	res, err := LintBunPlaceholders(filepath.Join("testdata", "bun_placeholders", dir))
	if err != nil {
		t.Fatalf("LintBunPlaceholders: %v", err)
	}
	var got []string
	for _, f := range res.Findings {
		if f.Severity != SeverityError {
			t.Errorf("%s at %s:%d is %s; a misbound placeholder is an error", f.Rule, f.File, f.Line, f.Severity)
		}
		got = append(got, fmt.Sprintf("%s %s:%d", f.Rule, f.File, f.Line))
	}
	return got
}

// Each bun-formatted call written in the other placeholder dialect is
// reported where its SQL is written — including a query held in a named
// constant, a builder reached through a local, and an orm.QueryOption's
// *bun.SelectQuery parameter.
func TestLintBunPlaceholders_Bad(t *testing.T) {
	t.Parallel()
	got := bunFindings(t, "bad")
	want := []string{
		"forgeconv-bun-dollar-placeholder store.go:14",    // $1 in a const passed to NewRaw
		"forgeconv-bun-dollar-placeholder store.go:14",    // $2
		"forgeconv-bun-dollar-placeholder store.go:22",    // db.Bun().ExecContext
		"forgeconv-bun-literal-question-mark store.go:28", // jsonb ? 'key' in a builder
		"forgeconv-bun-literal-question-mark store.go:34", // jsonb ?| through a local
		"forgeconv-bun-literal-question-mark store.go:40", // ? in a comment, QueryOption param
		"forgeconv-bun-literal-question-mark store.go:45", // ? in a string literal
		"forgeconv-bun-dollar-placeholder store.go:49",    // bun.SafeQuery
	}
	if !slices.Equal(got, want) {
		t.Fatalf("findings:\n got  %q\n want %q", got, want)
	}
}

// Correct bun SQL, escaped operators, the raw passthrough's native $N,
// argument-less queries, dollar-quoted bodies and non-bun Where methods are
// all clean.
func TestLintBunPlaceholders_Good(t *testing.T) {
	t.Parallel()
	if got := bunFindings(t, "good"); len(got) != 0 {
		t.Fatalf("clean fixture reported: %q", got)
	}
}

func TestJSONBQuestionOperator(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"a ?| b":        true,
		"a ?& b":        true,
		"a ? 'k'":       true,
		"a ?\n  'k'":    true,
		"a = ?":         false,
		"? ?":           false,
		"(?, ?)":        false,
		"x = ? AND y":   false,
		"coalesce(?,1)": false,
	}
	for q, want := range cases {
		i := 0
		for i < len(q) && q[i] != '?' {
			i++
		}
		if _, got := jsonbQuestionOperator(q, i); got != want {
			t.Errorf("jsonbQuestionOperator(%q) = %v, want %v", q, got, want)
		}
	}
}
