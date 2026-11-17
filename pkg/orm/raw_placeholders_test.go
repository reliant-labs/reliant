package orm

import (
	"testing"

	"github.com/jackc/pgx/v5"
)

// The raw path refuses only a statement that cannot run as postgres SQL:
// arguments, no $N, and a `?` that is not inside a literal, quoted
// identifier or comment. Everything else goes to the driver as written.
func TestUsesQuestionPlaceholders(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
		args  []any
		want  bool
	}{
		{"bun-style where", `SELECT body FROM notes WHERE id = ?`, []any{"a"}, true},
		{"bun-style values", `INSERT INTO notes (id, body) VALUES (?, ?)`, []any{"a", "b"}, true},
		{"bun-style indexed", `SELECT ?0, ?0`, []any{"a"}, true},
		{"jsonb operator beside a bun placeholder", `SELECT 1 FROM t WHERE tags ?| ?`, []any{[]string{"a"}}, true},
		{"postgres params", `SELECT body FROM notes WHERE id = $1`, []any{"a"}, false},
		{"jsonb operator with a param", `SELECT 1 FROM t WHERE data ? $1`, []any{"k"}, false},
		{"jsonb any-key with a param", `SELECT 1 FROM t WHERE data ?| $1`, []any{[]string{"k"}}, false},
		{"no args", `SELECT 1 FROM t WHERE data ? 'k'`, nil, false},
		{"no question mark", `SELECT now()`, []any{"a"}, false},
		{"question mark in a string", `SELECT 'why?' WHERE 1 = 1`, []any{"a"}, false},
		{"question mark in an escape string", `SELECT E'it\'s ?'`, []any{"a"}, false},
		{"question mark in a quoted identifier", `SELECT "a?b" FROM t`, []any{"a"}, false},
		{"question mark in a line comment", "SELECT 1 -- why?\n", []any{"a"}, false},
		{"question mark in a block comment", `SELECT 1 /* why? /* nested? */ */`, []any{"a"}, false},
		{"question mark in a dollar quote", `SELECT $tag$why?$tag$`, []any{"a"}, false},
		{"dollar in an identifier is not a param", `SELECT a$1 FROM t WHERE id = ?`, []any{"a"}, true},
		{"pgx named args", `SELECT 1 FROM t WHERE data ? 'k' AND id = @id`, []any{pgx.NamedArgs{"id": 1}}, false},
		{"pgx exec mode only", `SELECT 1 FROM t WHERE data ? 'k'`, []any{pgx.QueryExecModeSimpleProtocol}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := usesQuestionPlaceholders(tc.query, tc.args); got != tc.want {
				t.Errorf("usesQuestionPlaceholders(%q) = %v, want %v", tc.query, got, tc.want)
			}
		})
	}
}
