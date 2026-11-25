package orm

import (
	"context"
	"database/sql/driver"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ─── the raw path's placeholder style ──────────────────────────────────────
//
// Two placeholder styles meet in this package, one per path:
//
//   - Bun() — the query builders, NewRaw, and bun.IDB's own ExecContext /
//     QueryContext / QueryRowContext — is bun's: `?` (and `?0`, `?name`),
//     which bun formats before the statement runs.
//   - Exec / Query / QueryRow on a Client or Tx are database/sql: the text
//     goes to postgres as written, so placeholders are postgres's own `$1`,
//     `$2`, … . `?` is not rewritten here, on purpose: in postgres `?` is an
//     operator (jsonb `?`, `?|`, `?&`; geometric `?-`, `?|`, `?#`), so a raw
//     statement such as `data ? $1` must reach the server untouched.
//
// Because orm is described as a layer over bun, `?` on the raw path is an
// easy mistake, and postgres reports it as a bare 42601 syntax error naming
// neither cause nor fix. The raw path therefore refuses a statement that
// cannot be a postgres one — it has arguments, no `$N` parameter, and a `?`
// outside string literals, quoted identifiers and comments — with
// ErrQuestionPlaceholders, which says what to write instead. Every statement
// it refuses would have failed anyway: a statement with no `$N` takes no
// arguments, and the driver rejects the ones it was handed. So no statement
// that runs today is affected.

// rawPlaceholderErr is the error the raw path returns for query run with args,
// or nil when the statement may go to the driver as written.
func rawPlaceholderErr(method, query string, args []any) error {
	if !usesQuestionPlaceholders(query, args) {
		return nil
	}
	return fmt.Errorf("%s: %w", method, ErrQuestionPlaceholders)
}

// rejectedRow is the sole argument QueryRow sends with a statement it
// refuses. database/sql has no way to build a *sql.Row that holds an error,
// so the refusal travels as an argument the driver fails on before anything
// reaches the server, and Row.Scan returns it: pgx — the driver forge opens
// its pools with — takes it as a query rewriter whose rewrite fails; a driver
// that converts its arguments fails converting it as a driver.Valuer.
type rejectedRow struct{ err error }

func (r rejectedRow) RewriteQuery(context.Context, *pgx.Conn, string, []any) (string, []any, error) {
	return "", nil, r.err
}

func (r rejectedRow) Value() (driver.Value, error) { return nil, r.err }

// usesQuestionPlaceholders reports whether query, run with args, is written
// with bun's `?` placeholders: it has a value argument, no `$N` parameter,
// and a `?` outside string literals, quoted identifiers and comments (the
// tokenizer bind_sql.go binds bun's statements with).
func usesQuestionPlaceholders(query string, args []any) bool {
	if strings.IndexByte(query, '?') < 0 || !hasValueArg(args) {
		return false
	}
	l := lexer{s: []byte(query)}
	sawQuestion := false
	for {
		tok, ok := l.next()
		if !ok {
			return sawQuestion
		}
		switch tok.kind {
		case tokParam:
			return false // a $N parameter: the statement is postgres's
		case tokOp:
			if strings.IndexByte(query[tok.start:tok.end], '?') >= 0 {
				sawQuestion = true
			}
		}
	}
}

// hasValueArg reports whether args holds a value for the statement, rather
// than only pgx's per-query options, which the driver consumes itself (a
// pgx.NamedArgs rewrites @name parameters into $N).
func hasValueArg(args []any) bool {
	for _, a := range args {
		switch a.(type) {
		case pgx.QueryRewriter, pgx.QueryExecMode, pgx.QueryResultFormats, pgx.QueryResultFormatsByOID:
			return false
		}
	}
	return len(args) > 0
}
