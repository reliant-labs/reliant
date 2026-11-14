package orm

import (
	"bytes"
	"database/sql/driver"
	"encoding/hex"
	"reflect"
	"strconv"
	"sync"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/schema"
)

// ─── bind parameters for every statement bun builds ───────────────────────
//
// bun formats every value into the SQL text client-side and sends the
// statement with no arguments. Under pgx's default statement cache
// (QueryExecModeCacheStatement — what serverkit.OpenDB opens) every distinct
// TEXT is a distinct prepared statement, so each new id, timestamp or body
// paid a Parse/Describe round trip and a fresh plan, and pushed another entry
// through the connection's 512-slot cache. Measured in reliant: a message
// save as NewRaw was 35–80% slower than the same statement with bound $N,
// and an append through the builder cost 16x the bytes and 3x the
// allocations per call.
//
// So every statement a Client's bun handle runs is sent with its values as
// bind parameters: one text per statement SHAPE, prepared once per
// connection, then Bind/Execute with a cached plan.
//
// How. bun still renders the statement as it always has (its query hooks see
// that text). The connection a Client hands bun (bindConn) then renders the
// same query a second time with a binding dialect, which writes `$k` where
// bun wrote a value's literal and records the value. A value is recognised
// where bun writes one:
//
//   - every column value of a model (INSERT VALUES, UPDATE SET, WherePK),
//     through the field appenders forgeDialect wraps at table registration —
//     numbers included;
//   - every string, time, []byte and JSON value bun formats through the
//     dialect, which covers `?` arguments of those types in Where, Set,
//     ColumnExpr, NewRaw and the rest;
//   - any value wrapped in Arg, and every element of ArgList — the way to bind
//     an integer `?` argument, which bun formats without consulting the
//     dialect.
//
// Each value is rendered by bun's own appender and bound as that literal's
// text (bytea as its bytes), so postgres parses it with the target's input
// function exactly as it parsed the inline literal: arrays, JSON, enums,
// times and custom appenders keep their semantics. bind_sql.go then proves
// the bound text is the inline statement with only its value literals turned
// into parameters, and keeps a value inline wherever a parameter would not
// mean what the literal meant. Whenever that proof fails, the statement runs
// exactly as bun rendered it.
//
// What stays a literal, on purpose: text written into the SQL itself (a
// partial-index predicate the planner must see at plan time belongs there),
// NULL, TRUE/FALSE, DEFAULT, and values wrapped in Literal. InlineValues(ctx)
// turns binding off for every statement run with that ctx.

// forgeDialect is the postgres dialect every Client runs bun with. It is
// pgdialect, with its own table registry so that each registered table's
// column appenders can be wrapped (bindingAppender) before any statement
// renders them. Outside a binding render the wrapped appenders behave exactly
// like bun's.
type forgeDialect struct {
	*pgdialect.Dialect
	tables *schema.Tables
}

func newForgeDialect() *forgeDialect {
	d := &forgeDialect{Dialect: pgdialect.New()}
	d.tables = schema.NewTables(d)
	return d
}

// Tables is forgeDialect's own registry, so tables call its OnTable.
func (d *forgeDialect) Tables() *schema.Tables { return d.tables }

// OnTable runs pgdialect's per-table setup (array, hstore and sequence
// columns), then wraps every column appender for binding renders. It runs
// once per table, under the registry's lock, before the table is used.
func (d *forgeDialect) OnTable(table *schema.Table) {
	d.Dialect.OnTable(table)
	for _, f := range table.Fields {
		if f.Append != nil {
			f.Append = bindingAppender(f.Append, f.IndirectType == bytesType)
		}
	}
}

var bytesType = reflect.TypeFor[[]byte]()

// bindingAppender is a column appender that, in a binding render, renders the
// value with bun's own appender and binds the literal.
func bindingAppender(fn schema.AppenderFunc, isBytes bool) schema.AppenderFunc {
	return func(gen schema.QueryGen, b []byte, v reflect.Value) []byte {
		bd, ok := gen.Dialect().(*bindDialect)
		if !ok {
			return fn(gen, b, v)
		}
		st := bd.st
		st.scratch = fn(st.base, st.scratch[:0], v)
		return st.appendLiteral(b, st.scratch, isBytes)
	}
}

// bindDialect is the dialect of one binding render: forgeDialect, with the
// value appenders bun formats `?` arguments through turned into parameters.
type bindDialect struct {
	*forgeDialect
	st *bindState
}

func (d *bindDialect) AppendString(b []byte, s string) []byte {
	st := d.st
	st.scratch = d.forgeDialect.AppendString(st.scratch[:0], s)
	return st.appendLiteral(b, st.scratch, false)
}

func (d *bindDialect) AppendTime(b []byte, tm time.Time) []byte {
	st := d.st
	st.scratch = d.forgeDialect.AppendTime(st.scratch[:0], tm)
	return st.appendLiteral(b, st.scratch, false)
}

func (d *bindDialect) AppendBytes(b, bs []byte) []byte {
	st := d.st
	st.scratch = d.forgeDialect.AppendBytes(st.scratch[:0], bs)
	return st.appendLiteral(b, st.scratch, true)
}

func (d *bindDialect) AppendJSON(b, jsonb []byte) []byte {
	st := d.st
	st.scratch = d.forgeDialect.AppendJSON(st.scratch[:0], jsonb)
	return st.appendLiteral(b, st.scratch, false)
}

// paramClass is how a recorded value is bound.
type paramClass uint8

const (
	classText   paramClass = iota // a quoted literal: an untyped parameter, as the literal was untyped
	classBytes                    // a bytea literal: its bytes
	classNumber                   // a numeric literal: a parameter cast to the literal's type
)

// boundValue is one value the binding render replaced with `$k`.
type boundValue struct {
	litStart, litEnd int // the inline literal, in bindState.lits
	arg              any
	class            paramClass
	cast             string // classNumber: int4, int8 or numeric
}

// maxParams is postgres's limit on the parameters of one statement.
const maxParams = 65535

// bindState is the state of one binding render. Pooled.
type bindState struct {
	dialect bindDialect
	base    schema.QueryGen // forgeDialect's generator: renders literals, records nothing
	values  []boundValue
	lits    []byte
	scratch []byte
	text    []byte // the binding render
	buf     []byte // the statement sent
	sites   []paramSite
	tooMany bool
}

var bindStates = sync.Pool{New: func() any {
	st := &bindState{}
	st.dialect.st = st
	return st
}}

func getBindState(d *forgeDialect) *bindState {
	st := bindStates.Get().(*bindState)
	st.dialect.forgeDialect = d
	st.base = schema.NewQueryGen(d)
	return st
}

func (st *bindState) release() {
	// Large buffers are dropped rather than pinned in the pool.
	if cap(st.buf) > 64<<10 || cap(st.text) > 64<<10 || cap(st.lits) > 64<<10 {
		return
	}
	for i := range st.values {
		st.values[i].arg = nil
	}
	st.values, st.lits, st.buf, st.sites = st.values[:0], st.lits[:0], st.buf[:0], st.sites[:0]
	st.text = st.text[:0]
	st.scratch, st.tooMany = st.scratch[:0], false
	bindStates.Put(st)
}

// appendLiteral appends to b the parameter standing for the literal lit, or
// lit itself when it is not a plain literal (NULL, TRUE, DEFAULT, an
// expression a custom appender wrote).
func (st *bindState) appendLiteral(b, lit []byte, isBytes bool) []byte {
	v := boundValue{}
	switch {
	case st.tooMany:
		return append(b, lit...)
	case isBytes && isByteaLiteral(lit):
		raw := make([]byte, hex.DecodedLen(len(lit)-4))
		if _, err := hex.Decode(raw, lit[3:len(lit)-1]); err != nil {
			return append(b, lit...)
		}
		v.arg, v.class = raw, classBytes
	case len(lit) >= 2 && lit[0] == '\'':
		s, ok := unquoteLiteral(lit)
		if !ok {
			return append(b, lit...)
		}
		v.arg, v.class = s, classText
	case isNumeric(lit):
		v.arg, v.class, v.cast = string(lit), classNumber, numericCast(lit)
		// bun separates a negative number from a preceding '-' so the two
		// never read as a comment; the parameter keeps the same space, so
		// the bound text still lines up with bun's.
		if lit[0] == '-' && len(b) > 0 && b[len(b)-1] == '-' {
			b = append(b, ' ')
		}
	default:
		return append(b, lit...)
	}
	if len(st.values) == maxParams {
		st.tooMany = true
		return append(b, lit...)
	}
	v.litStart = len(st.lits)
	st.lits = append(st.lits, lit...)
	v.litEnd = len(st.lits)
	st.values = append(st.values, v)
	b = append(b, '$')
	return strconv.AppendInt(b, int64(len(st.values)), 10)
}

func isByteaLiteral(lit []byte) bool {
	return len(lit) >= 4 && lit[0] == '\'' && lit[1] == '\\' && lit[2] == 'x' && lit[len(lit)-1] == '\''
}

// unquoteLiteral is the value of a plain single-quoted literal, every inner
// quote doubled.
func unquoteLiteral(lit []byte) (string, bool) {
	if len(lit) < 2 || lit[0] != '\'' || lit[len(lit)-1] != '\'' {
		return "", false
	}
	inner := lit[1 : len(lit)-1]
	for i := 0; i < len(inner); i++ {
		if inner[i] == '\'' {
			if i+1 >= len(inner) || inner[i+1] != '\'' {
				return "", false
			}
			i++
		}
	}
	if bytes.IndexByte(inner, '\'') < 0 {
		return string(inner), true
	}
	return string(bytes.ReplaceAll(inner, []byte("''"), []byte("'"))), true
}

// isNumeric matches the integer and float tokens bun writes:
// -?digits(.digits)?([eE][+-]?digits)?
func isNumeric(s []byte) bool {
	i := 0
	if i < len(s) && s[i] == '-' {
		i++
	}
	start := i
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	if i == start {
		return false
	}
	if i < len(s) && s[i] == '.' {
		i++
		frac := i
		for i < len(s) && isDigit(s[i]) {
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
		for i < len(s) && isDigit(s[i]) {
			i++
		}
		if i == exp {
			return false
		}
	}
	return i == len(s)
}

// numericCast is the type postgres gives the numeric literal lit: int4 when
// it fits, then int8, else numeric (also for any fraction or exponent).
func numericCast(lit []byte) string {
	if bytes.ContainsAny(lit, ".eE") {
		return "numeric"
	}
	n, err := strconv.ParseInt(string(lit), 10, 64)
	switch {
	case err != nil:
		return "numeric"
	case n >= -1<<31 && n < 1<<31:
		return "int4"
	default:
		return "int8"
	}
}

// bind turns the binding render text into the statement to send: text with
// every value that may be a parameter numbered $1… in order, the others
// restored to their literals. inline is the statement bun rendered; ok is
// false unless text is provably that statement with values turned into
// parameters (see bind_sql.go), or when nothing would be bound.
func (st *bindState) bind(text []byte, inline string) (query string, args []any, ok bool) {
	if len(st.values) == 0 || st.tooMany {
		return "", nil, false
	}
	sites, ok := scanParams(text, st.sites[:0])
	st.sites = sites
	if !ok || len(sites) != len(st.values) {
		return "", nil, false
	}
	// The text, each parameter restored to its literal, is bun's statement.
	pos := 0
	at := 0
	for i := range sites {
		p := &sites[i]
		if paramIndex(text[p.start:p.end]) != i+1 {
			return "", nil, false
		}
		seg := text[pos:p.start]
		lit := st.lits[st.values[i].litStart:st.values[i].litEnd]
		if len(inline)-at < len(seg)+len(lit) || inline[at:at+len(seg)] != string(seg) ||
			inline[at+len(seg):at+len(seg)+len(lit)] != string(lit) {
			return "", nil, false
		}
		at += len(seg) + len(lit)
		pos = p.end
	}
	if inline[at:] != string(text[pos:]) {
		return "", nil, false
	}

	out := st.buf[:0]
	pos = 0
	for i := range sites {
		p := &sites[i]
		v := &st.values[i]
		out = append(out, text[pos:p.start]...)
		pos = p.end
		if keepInline(p, v.class) {
			out = append(out, st.lits[v.litStart:v.litEnd]...)
			continue
		}
		args = append(args, v.arg)
		n := strconv.Itoa(len(args))
		if v.class == classNumber {
			out = append(out, "($"...)
			out = append(out, n...)
			out = append(out, "::"...)
			out = append(out, v.cast...)
			out = append(out, ')')
			continue
		}
		out = append(out, '$')
		out = append(out, n...)
	}
	out = append(out, text[pos:]...)
	st.buf = out
	if len(args) == 0 {
		return "", nil, false
	}
	return string(out), args, true
}

// ─── explicit values ──────────────────────────────────────────────────────

// ArgValue is a `?` argument a binding render sends as a bind parameter
// whatever its type. See Arg.
type ArgValue struct{ v any }

// Arg marks v to be sent as a bind parameter. Statements a Client runs bind
// their string, time, []byte and JSON values on their own, and every column
// value of a model; an integer or float `?` argument is the one value bun
// formats without consulting the dialect, so on a hot statement wrap it:
//
//	db.Bun().NewSelect().Model(&rows).Where("seq > ?", orm.Arg(seq))
//
// # Slices are postgres arrays
//
// A Go slice or array — or a pointer to one — passed to Arg binds as a
// postgres ARRAY, the way pgx encodes a []T and the way a model's `,array`
// column binds:
//
//	q.Where("kind = ANY(?)", orm.Arg(kinds))      // kinds []string → text[]
//	q.Where("tags @> ?", orm.Arg([]string{"a"}))  // array containment
//
// The exceptions keep their own encoding: []byte (and any slice of a byte
// kind, json.RawMessage included) stays bytea or JSON exactly as bun writes
// it, and a value that renders itself (a schema.QueryAppender or a
// driver.Valuer) is left to do so. A nil slice is NULL.
//
// JSON is never inferred from a slice: say it with JSON(v), which binds v's
// json.Marshal text (`doc @> ?`, orm.JSON(filter)). For an IN list of
// one parameter per element, use ArgList.
//
// A bare slice passed as a `?` argument WITHOUT Arg is bun's, not forge's:
// bun writes it as JSON. Wrap it — Arg for an array, JSON for a document.
//
// Outside a binding render Arg renders exactly as the bound statement reads.
func Arg(v any) ArgValue { return ArgValue{v: v} }

// AppendQuery implements schema.QueryAppender.
func (a ArgValue) AppendQuery(gen schema.QueryGen, b []byte) ([]byte, error) {
	v := argValue(a.v)
	bd, ok := gen.Dialect().(*bindDialect)
	if !ok {
		return gen.Append(b, v), nil
	}
	st := bd.st
	st.scratch = st.base.Append(st.scratch[:0], v)
	_, isBytes := v.([]byte)
	return st.appendLiteral(b, st.scratch, isBytes), nil
}

// argValue is the value Arg renders for v: v as a postgres array when it is
// a slice or array (see Arg), else v itself.
func argValue(v any) any {
	if !isPGArrayArg(reflect.TypeOf(v)) {
		return v
	}
	if arr, ok := pgArray(v); ok {
		return arr
	}
	return v
}

var (
	queryAppenderType = reflect.TypeFor[schema.QueryAppender]()
	driverValuerType  = reflect.TypeFor[driver.Valuer]()
)

// isPGArrayArg reports whether a value of type t binds as a postgres array:
// a slice or array (or a pointer to one) whose elements are not bytes, and
// which does not render itself.
func isPGArrayArg(t reflect.Type) bool {
	if t == nil || rendersItself(t) {
		return false
	}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
		if rendersItself(t) {
			return false
		}
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Array:
		return t.Elem().Kind() != reflect.Uint8
	}
	return false
}

func rendersItself(t reflect.Type) bool {
	return t.Implements(queryAppenderType) || t.Implements(driverValuerType) ||
		reflect.PointerTo(t).Implements(queryAppenderType) || reflect.PointerTo(t).Implements(driverValuerType)
}

// pgArray is pgdialect.Array(v), or ok false for an element type pgdialect
// has no array encoding for (it panics on those), which then renders as bun
// would render it.
func pgArray(v any) (arr *pgdialect.ArrayValue, ok bool) {
	defer func() {
		if recover() != nil {
			arr, ok = nil, false
		}
	}()
	return pgdialect.Array(v), true
}

// LiteralValue is a value always written into the SQL text. See Literal.
type LiteralValue struct{ v any }

// Literal keeps v in the statement's text instead of binding it. Use it for
// a value the planner must see while planning — one a partial index's
// predicate is proved from — since a prepared statement's generic plan
// cannot see a parameter's value.
func Literal(v any) LiteralValue { return LiteralValue{v: v} }

// AppendQuery implements schema.QueryAppender.
func (l LiteralValue) AppendQuery(gen schema.QueryGen, b []byte) ([]byte, error) {
	if bd, ok := gen.Dialect().(*bindDialect); ok {
		return bd.st.base.Append(b, l.v), nil
	}
	return gen.Append(b, l.v), nil
}

// ArgListValue is a list of values bound one parameter each. See ArgList.
type ArgListValue struct{ values any }

// ArgList is bun.In with every element bound, integers included:
//
//	q.Where("id IN (?)", orm.ArgList(ids))
//
// The statement's text then varies only with the number of elements. A value
// that is not a slice or array renders as bun.In renders it.
func ArgList(values any) ArgListValue { return ArgListValue{values: values} }

// AppendQuery implements schema.QueryAppender.
func (l ArgListValue) AppendQuery(gen schema.QueryGen, b []byte) ([]byte, error) {
	bd, ok := gen.Dialect().(*bindDialect)
	v := reflect.ValueOf(l.values)
	if !ok || v.Kind() != reflect.Slice {
		return bun.In(l.values).AppendQuery(gen, b)
	}
	return bd.st.appendIn(b, v), nil
}

// appendIn is bun's appendIn (schema/append.go) with each element bound:
// NULL for an empty list, elements ", "-separated, a nested slice as a
// parenthesized tuple.
func (st *bindState) appendIn(b []byte, slice reflect.Value) []byte {
	if slice.Len() == 0 {
		return append(b, "NULL"...)
	}
	for i := 0; i < slice.Len(); i++ {
		if i > 0 {
			b = append(b, ", "...)
		}
		elem := slice.Index(i)
		if elem.Kind() == reflect.Interface {
			elem = elem.Elem()
		}
		if elem.Kind() == reflect.Slice && elem.Type() != bytesType {
			b = append(b, '(')
			b = st.appendIn(b, elem)
			b = append(b, ')')
			continue
		}
		st.scratch = st.base.AppendValue(st.scratch[:0], elem)
		b = st.appendLiteral(b, st.scratch, elem.IsValid() && elem.Type() == bytesType)
	}
	return b
}

var (
	_ schema.QueryAppender = ArgValue{}
	_ schema.QueryAppender = LiteralValue{}
	_ schema.QueryAppender = ArgListValue{}
)
