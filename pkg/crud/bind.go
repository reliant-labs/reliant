package crud

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"reflect"
	"strconv"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/feature"
	"github.com/uptrace/bun/schema"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/reliant-labs/forge/pkg/orm"
)

// ─── bind parameters for the by-PK statements ─────────────────────────────
//
// bun formats every value into the SQL text client-side and sends the
// statement with no arguments. Under pgx's default statement cache
// (QueryExecModeCacheStatement — what serverkit.OpenDB opens) every distinct
// TEXT is a distinct prepared statement: a Get of a new id, or an update
// carrying a new timestamp, paid an extra Parse/Describe round trip and
// pushed another entry through the connection's 512-slot statement cache,
// evicting statements that were worth keeping. The by-PK statements are the
// highest-cardinality text forge issues and every one of them is
// forge-owned, so they are the ones that must not vary.
//
// Get, Update, UpdateMasked and Delete therefore build their statement with
// the same bun builder as before, but put a `$N` marker where each value
// goes, and run it through orm.Context's raw passthrough with the values as
// real bind parameters. The text is fixed per statement SHAPE (the entity,
// and for a masked update the set of columns), so the cache holds one
// prepared statement per shape and every later call is a Bind/Execute of it,
// with a cached generic plan.
//
// Values are converted the way bun would write them: the column's own bun
// appender renders the literal, and the literal's text becomes a
// text-format parameter. Postgres parses a text parameter with the target
// column's input function, which is exactly what it does with the untyped
// literal bun would have inlined, so arrays, JSON, bytea, enums, times and
// custom bun appenders all keep their semantics. A literal that is not a
// plain quoted string, number, boolean or NULL (an expression a custom
// appender produced) is inlined as before, which keeps it correct at the
// cost of a varying text for that one value.
//
// Every other statement the repo runs (Create, Upsert, List, Count, …) goes
// through bun's executor and is bound by the connection orm.Client hands bun
// (pkg/orm/bind.go), which re-renders it with a binding dialect. These
// statements bind here instead because they are the hottest: rendered once,
// with no second render.
//
// Two things are deliberately left to bun:
//
//   - A model with bun model hooks (BeforeAppendModel, Before/AfterSelect,
//     Before/AfterUpdate, Before/AfterDelete) runs every statement through
//     bun's own executor, which is what calls them.
//   - bun QueryHooks (bunotel, a logging hook) do not see the bind-parameter
//     statements, which run beside bun rather than through it. The repo's
//     own span ("orm.<Op><Entity>") carries the statement template as
//     db.statement — without the values, which bunotel's attribute included.

// modelHookTypes are the bun model-hook interfaces whose presence keeps an
// entity on bun's executor.
var modelHookTypes = []reflect.Type{
	reflect.TypeFor[schema.BeforeAppendModelHook](),
	reflect.TypeFor[bun.BeforeSelectHook](),
	reflect.TypeFor[bun.AfterSelectHook](),
	reflect.TypeFor[bun.BeforeUpdateHook](),
	reflect.TypeFor[bun.AfterUpdateHook](),
	reflect.TypeFor[bun.BeforeDeleteHook](),
	reflect.TypeFor[bun.AfterDeleteHook](),
}

// hasModelHooks reports whether the model type implements a bun model hook.
func hasModelHooks(typ reflect.Type) bool {
	ptr := reflect.PointerTo(typ)
	for _, h := range modelHookTypes {
		if typ.Implements(h) || ptr.Implements(h) {
			return true
		}
	}
	return false
}

// binder collects the bind parameters of one statement. In bind mode each
// value becomes a `$N` marker (as a bun.Safe, so bun's formatter copies it
// verbatim); otherwise values pass through untouched and the statement is
// exactly the one bun would have built and run.
type binder struct {
	bind bool
	gen  schema.QueryGen
	args []any
	buf  []byte
}

func (r *Repo[M]) newBinder(db orm.Context) *binder {
	if !r.m.bindParams {
		return &binder{}
	}
	return &binder{bind: true, gen: schema.NewQueryGen(db.Bun().Dialect())}
}

// value is the argument to put in place of a `?` that stands for v.
func (b *binder) value(v any) any {
	if !b.bind {
		return v
	}
	b.buf = b.gen.Append(b.buf[:0], v)
	_, isBytes := v.([]byte)
	return b.param(isBytes)
}

// field is the argument for the column f of the struct value strct, rendered
// by f's own appender exactly as bun renders it in a SET clause — including
// DEFAULT for a nil pointer or a zero ,nullzero field on a dialect with
// feature.DefaultPlaceholder (postgres), which stays inline: a keyword is not
// a value.
func (b *binder) field(f *schema.Field, strct reflect.Value) any {
	if b.gen.HasFeature(feature.DefaultPlaceholder) {
		b.buf = f.AppendValueOrDefault(b.gen, b.buf[:0], strct)
	} else {
		b.buf = f.AppendValue(b.gen, b.buf[:0], strct)
	}
	return b.param(f.IndirectType == bytesType)
}

var bytesType = reflect.TypeFor[[]byte]()

// param turns the literal in b.buf into the next bind parameter, or inlines
// it when it is not a plain literal.
//
// isBytes says the value is a Go []byte, which bun writes as a hex bytea
// literal ('\x0102'). That one is bound as the decoded bytes rather than as
// text: lib/pq encodes a string parameter aimed at a bytea column as the
// raw bytes of the string, not through bytea's input function, so the text
// form would store the two characters `\x` instead of an empty value.
func (b *binder) param(isBytes bool) any {
	if isBytes {
		if raw, ok := byteaArg(b.buf); ok {
			b.args = append(b.args, raw)
			return marker(len(b.args))
		}
	}
	arg, ok := bindArg(b.buf)
	if !ok {
		return bun.Safe(string(b.buf))
	}
	b.args = append(b.args, arg)
	return marker(len(b.args))
}

// markers are the boxed `$N` markers for the first parameters, built once:
// a by-PK statement's markers then cost no allocation.
var markers = func() []any {
	m := make([]any, 65)
	for i := 1; i < len(m); i++ {
		m[i] = bun.Safe("$" + strconv.Itoa(i))
	}
	return m
}()

// marker is the bun.Safe `$n` for the n-th (1-based) parameter.
func marker(n int) any {
	if n < len(markers) {
		return markers[n]
	}
	return bun.Safe("$" + strconv.Itoa(n))
}

// setColumns adds cols to an UPDATE's SET clause with the values entity
// holds. In bind mode each is overridden with its parameter marker; bun
// still names the columns and owns the clause's shape.
func (b *binder) setColumns(q *bun.UpdateQuery, fields map[string]*schema.Field, entity reflect.Value, cols []string) {
	q.Column(cols...)
	if !b.bind {
		return
	}
	for _, c := range cols {
		q.Value(c, "?", b.field(fields[c], entity))
	}
}

// byteaArg decodes bun's hex bytea literal '\x…' into its bytes.
func byteaArg(lit []byte) ([]byte, bool) {
	if len(lit) < 4 || lit[0] != '\'' || lit[1] != '\\' || lit[2] != 'x' || lit[len(lit)-1] != '\'' {
		return nil, false
	}
	out := make([]byte, hex.DecodedLen(len(lit)-4))
	if _, err := hex.Decode(out, lit[3:len(lit)-1]); err != nil {
		return nil, false
	}
	return out, true
}

// bindArg converts a SQL literal bun rendered into the text-format parameter
// postgres parses the same way: nil for NULL, the token for a number or
// boolean, and the unquoted contents of a single-quoted string. ok is false
// for anything else.
func bindArg(lit []byte) (any, bool) {
	switch {
	case len(lit) == 0:
		return nil, false
	case string(lit) == "NULL":
		return nil, true
	case string(lit) == "TRUE" || string(lit) == "FALSE":
		return string(lit), true
	case lit[0] == '\'':
		if len(lit) < 2 || lit[len(lit)-1] != '\'' {
			return nil, false
		}
		inner := lit[1 : len(lit)-1]
		// Every quote inside must be a doubled one.
		for i := 0; i < len(inner); i++ {
			if inner[i] == '\'' {
				if i+1 >= len(inner) || inner[i+1] != '\'' {
					return nil, false
				}
				i++
			}
		}
		if bytes.IndexByte(inner, '\'') < 0 {
			return string(inner), true
		}
		return string(bytes.ReplaceAll(inner, []byte("''"), []byte("'"))), true
	case isNumeric(lit):
		return string(lit), true
	}
	return nil, false
}

// isNumeric matches the integer and float tokens bun writes:
// -?digits(.digits)?([eE][+-]?digits)?
func isNumeric(s []byte) bool {
	i := 0
	if i < len(s) && s[i] == '-' {
		i++
	}
	start := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == start {
		return false
	}
	if i < len(s) && s[i] == '.' {
		i++
		frac := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i == frac {
			return false
		}
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		exp := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i == exp {
			return false
		}
	}
	return i == len(s)
}

// sqlAppender is a bun query that renders its own SQL.
type sqlAppender interface {
	AppendQuery(gen schema.QueryGen, b []byte) ([]byte, error)
}

// render renders q's SQL with its parameter markers and records it on the
// span as the statement template.
func (b *binder) render(span trace.Span, q sqlAppender) (string, error) {
	query, err := q.AppendQuery(b.gen, nil)
	if err != nil {
		return "", err
	}
	span.SetAttributes(attribute.String("db.system", "postgresql"), attribute.String("db.statement", string(query)))
	return string(query), nil
}

// bunWrite is a bun UPDATE or DELETE.
type bunWrite interface {
	sqlAppender
	Exec(ctx context.Context, dest ...any) (sql.Result, error)
}

// write runs an UPDATE or DELETE built with b. returning says whether the
// statement has a RETURNING clause, whose single row is scanned into dest.
func (b *binder) write(ctx context.Context, span trace.Span, db orm.Context, q bunWrite, dest any, returning bool) (sql.Result, error) {
	if !b.bind {
		return q.Exec(ctx)
	}
	query, err := b.render(span, q)
	if err != nil {
		return nil, err
	}
	if !returning {
		return db.Exec(ctx, query, b.args...)
	}
	rows, err := db.Query(ctx, query, b.args...)
	if err != nil {
		return nil, err
	}
	n, err := scanFirst(ctx, db, rows, dest)
	if err != nil {
		return nil, err
	}
	return rowCount(n), nil
}

// get runs a SELECT built with b and scans its first row into dest. No row
// is sql.ErrNoRows, as from bun's Scan.
func (b *binder) get(ctx context.Context, span trace.Span, db orm.Context, q *bun.SelectQuery, dest any) error {
	if !b.bind {
		return q.Scan(ctx)
	}
	query, err := b.render(span, q)
	if err != nil {
		return err
	}
	rows, err := db.Query(ctx, query, b.args...)
	if err != nil {
		return err
	}
	n, err := scanFirst(ctx, db, rows, dest)
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// rowScanner is *bun.DB's single-row scan, which Client.Bun() promotes.
type rowScanner interface {
	ScanRow(ctx context.Context, rows *sql.Rows, dest ...any) error
}

// scanFirst scans the first row of rows (if any) into dest with bun's model
// scanner — so column mapping and scan hooks are bun's own — and closes
// rows. It returns how many rows there were, at most one being expected.
func scanFirst(ctx context.Context, db orm.Context, rows *sql.Rows, dest any) (n int64, err error) {
	defer func() {
		if cerr := rows.Close(); err == nil && cerr != nil {
			err = cerr
		}
	}()
	if !rows.Next() {
		return 0, rows.Err()
	}
	scanner, ok := db.Bun().(rowScanner)
	if !ok {
		scanner = db.Bun().NewSelect().DB()
	}
	if err := scanner.ScanRow(ctx, rows, dest); err != nil {
		return 0, err
	}
	n = 1
	for rows.Next() {
		n++
	}
	return n, rows.Err()
}

// rowCount is the sql.Result of a statement whose rows were scanned.
type rowCount int64

func (n rowCount) RowsAffected() (int64, error) { return int64(n), nil }
func (rowCount) LastInsertId() (int64, error) {
	return 0, errors.New("crud: LastInsertId is not supported")
}
