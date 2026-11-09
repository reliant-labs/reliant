package dbbrowse

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Request bounds. A value past its cap is refused, not clamped, so a caller
// learns the cap it asked past.
const (
	DefaultLimit = 50
	MaxLimit     = 500
	// ExactCountCap is where an exact count stops counting: past it the
	// total is reported as "at least ExactCountCap" ([Page.TotalAtLeast]),
	// so a count never scans an unbounded table.
	ExactCountCap = 10_000
	// MaxFilters caps a request's filters, MaxInValues one in filter's
	// values, and MaxValueBytes one value's length.
	MaxFilters    = 32
	MaxInValues   = 100
	MaxValueBytes = 4 << 10
)

// Op is a filter operator.
type Op string

const (
	OpEq  Op = "eq"
	OpNeq Op = "neq" // SQL <>: a NULL is neither equal nor unequal, so it never matches
	OpLt  Op = "lt"
	OpLte Op = "lte"
	OpGt  Op = "gt"
	OpGte Op = "gte"
	// OpLike and OpIlike match the column's text form against a LIKE
	// pattern (% and _ wildcards, backslash escapes).
	OpLike    Op = "like"
	OpIlike   Op = "ilike"
	OpIsNull  Op = "is_null"
	OpNotNull Op = "not_null"
	// OpIn matches any of 1..MaxInValues values.
	OpIn Op = "in"
)

// CountMode says how [Run] reports a filtered table's size.
type CountMode string

const (
	// CountNone reports no total.
	CountNone CountMode = "none"
	// CountEstimated reports the planner's estimate: the table's
	// [Table.EstimatedRows] when the request has no filters and there is
	// one, else the row estimate EXPLAIN gives for the filtered query. It
	// executes nothing.
	CountEstimated CountMode = "estimated"
	// CountExact counts, up to [ExactCountCap].
	CountExact CountMode = "exact"
)

// Filter is one WHERE condition. Values holds one value for the comparison
// and like operators, none for is_null and not_null, and 1..MaxInValues for
// in. A value is text in the form Postgres reads for the column's type.
type Filter struct {
	Column string   `json:"column"`
	Op     Op       `json:"op"`
	Values []string `json:"values,omitempty"`
}

// Sort is one ORDER BY key.
type Sort struct {
	Column string `json:"column"`
	Desc   bool   `json:"desc,omitempty"`
}

// Request is one page of one table. Filters are ANDed.
type Request struct {
	// Table is schema-qualified, as [Table.QualifiedName] prints it.
	Table string `json:"table"`
	// Columns to return, in order; empty means every readable column.
	Columns []string `json:"columns,omitempty"`
	Filters []Filter `json:"filters,omitempty"`
	// Sort keys, most significant first. The primary key follows them as a
	// tiebreaker (for a table without one, every sortable column does), so a
	// page boundary is stable; with no Sort, the primary key ascending is
	// the order.
	Sort []Sort `json:"sort,omitempty"`
	// Limit is the page size: 0 means DefaultLimit, above MaxLimit is
	// refused. Offset is how many rows to skip.
	Limit  int `json:"limit,omitempty"`
	Offset int `json:"offset,omitempty"`
	// Count is CountNone when empty.
	Count CountMode `json:"count,omitempty"`
}

// Query is one compiled statement and the values of its $n parameters.
type Query struct {
	SQL  string
	Args []any
}

// Compiled is a [Request] checked against a [Database] and compiled. Its
// statements are reachable only through [Run], which executes them, and the
// read-only accessors, so what runs is always what [Compile] wrote.
type Compiled struct {
	// Table is the described table the request reads.
	Table *Table
	// Columns are the selected columns, in result order.
	Columns []Column
	Limit   int
	Offset  int
	Count   CountMode

	page  Query
	count *Query
	// estimate, when count is nil and Count is CountEstimated, is the total.
	estimate int64
}

// Page returns the page statement. It fetches Limit+1 rows: the extra row,
// never returned, is how [Run] knows there is a next page.
func (c *Compiled) Page() Query { return c.page.clone() }

// CountQuery returns the statement that counts, and false when the count
// needs none (CountNone, or an estimate the description already holds).
func (c *Compiled) CountQuery() (Query, bool) {
	if c.count == nil {
		return Query{}, false
	}
	return c.count.clone(), true
}

func (q Query) clone() Query {
	return Query{SQL: q.SQL, Args: append([]any(nil), q.Args...)}
}

// Compile checks req against d, which must be the [Describe] result for the
// role the query will run as, and compiles it. Every error wraps
// [ErrInvalidRequest].
func Compile(d *Database, req Request) (*Compiled, error) {
	t, err := d.Table(req.Table)
	if err != nil {
		return nil, err
	}
	c := &Compiled{Table: t, Limit: req.Limit, Offset: req.Offset, Count: req.Count}
	switch {
	case c.Limit < 0 || c.Limit > MaxLimit:
		return nil, invalid("limit must be between 1 and %d, got %d", MaxLimit, c.Limit)
	case c.Limit == 0:
		c.Limit = DefaultLimit
	}
	if c.Offset < 0 {
		return nil, invalid("offset must not be negative, got %d", c.Offset)
	}
	switch c.Count {
	case "":
		c.Count = CountNone
	case CountNone, CountEstimated, CountExact:
	default:
		return nil, invalid("count must be none, estimated or exact, got %q", c.Count)
	}

	// SELECT list.
	if len(req.Columns) == 0 {
		c.Columns = append([]Column(nil), t.Columns...)
	} else {
		seen := map[string]bool{}
		for _, name := range req.Columns {
			col, ok := t.Column(name)
			if !ok {
				return nil, fmt.Errorf("%w: %q in %s", ErrUnknownColumn, name, t.QualifiedName())
			}
			if seen[name] {
				return nil, invalid("column %q is selected twice", name)
			}
			seen[name] = true
			c.Columns = append(c.Columns, col)
		}
	}
	sel := make([]string, len(c.Columns))
	for i, col := range c.Columns {
		sel[i] = ident(col.Name)
	}
	from := pgx.Identifier{t.Schema, t.Name}.Sanitize()

	// WHERE.
	where, args, err := compileFilters(t, req.Filters)
	if err != nil {
		return nil, err
	}

	// ORDER BY.
	order, err := compileOrder(t, req.Sort)
	if err != nil {
		return nil, err
	}

	var b strings.Builder
	b.WriteString("SELECT ")
	b.WriteString(strings.Join(sel, ", "))
	b.WriteString(" FROM ")
	b.WriteString(from)
	b.WriteString(where)
	if order != "" {
		b.WriteString(" ORDER BY ")
		b.WriteString(order)
	}
	b.WriteString(" LIMIT ")
	b.WriteString(strconv.Itoa(c.Limit + 1))
	b.WriteString(" OFFSET ")
	b.WriteString(strconv.Itoa(c.Offset))
	c.page = Query{SQL: b.String(), Args: args}

	switch c.Count {
	case CountExact:
		c.count = &Query{
			SQL:  "SELECT count(*) FROM (SELECT 1 FROM " + from + where + " LIMIT " + strconv.Itoa(ExactCountCap+1) + ") AS capped",
			Args: args,
		}
	case CountEstimated:
		if len(req.Filters) == 0 && t.EstimatedRows >= 0 {
			c.estimate = t.EstimatedRows
		} else {
			// EXPLAIN without ANALYZE plans and executes nothing; the top
			// node's Plan Rows is the planner's estimate for the filter.
			c.count = &Query{SQL: "EXPLAIN (FORMAT JSON) SELECT 1 FROM " + from + where, Args: args}
		}
	}
	return c, nil
}

// compileFilters returns " WHERE ..." (empty for no filters) and its args.
func compileFilters(t *Table, filters []Filter) (string, []any, error) {
	if len(filters) == 0 {
		return "", nil, nil
	}
	if len(filters) > MaxFilters {
		return "", nil, invalid("at most %d filters, got %d", MaxFilters, len(filters))
	}
	var (
		conds []string
		args  []any
	)
	param := func(v string) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	for i, f := range filters {
		col, ok := t.Column(f.Column)
		if !ok {
			return "", nil, fmt.Errorf("%w: filter %d names %q, not a readable column of %s", ErrUnknownColumn, i, f.Column, t.QualifiedName())
		}
		want, known := arity[f.Op]
		if !known {
			return "", nil, fmt.Errorf("%w: filter %d: %q (want one of eq, neq, lt, lte, gt, gte, like, ilike, is_null, not_null, in)", ErrUnknownOp, i, f.Op)
		}
		switch {
		case want >= 0 && len(f.Values) != want:
			return "", nil, invalid("filter %d: %s takes %d value(s), got %d", i, f.Op, want, len(f.Values))
		case want < 0 && (len(f.Values) == 0 || len(f.Values) > MaxInValues):
			return "", nil, invalid("filter %d: in takes 1 to %d values, got %d", i, MaxInValues, len(f.Values))
		}
		for _, v := range f.Values {
			if err := checkValue(v); err != nil {
				return "", nil, invalid("filter %d: %v", i, err)
			}
		}
		// lhs is the column, or its text form where the column's own type
		// cannot answer the operator (see the package doc, "How a value is
		// typed").
		lhs := ident(col.Name)
		switch f.Op {
		case OpLt, OpLte, OpGt, OpGte:
			if !col.Sortable {
				return "", nil, invalid("filter %d: %s has type %s, which has no ordering; %s cannot compare it", i, col.Name, col.Type, f.Op)
			}
		case OpLike, OpIlike:
			lhs += "::text"
		case OpEq, OpNeq, OpIn:
			if !col.Sortable {
				lhs += "::text"
			}
		}
		switch f.Op {
		case OpIsNull:
			conds = append(conds, lhs+" IS NULL")
		case OpNotNull:
			conds = append(conds, lhs+" IS NOT NULL")
		case OpIn:
			ps := make([]string, len(f.Values))
			for j, v := range f.Values {
				ps[j] = param(v)
			}
			conds = append(conds, lhs+" IN ("+strings.Join(ps, ", ")+")")
		default:
			conds = append(conds, lhs+" "+sqlOp[f.Op]+" "+param(f.Values[0]))
		}
	}
	return " WHERE " + strings.Join(conds, " AND "), args, nil
}

// compileOrder returns the ORDER BY list: the request's keys, then the
// primary key (or, without one, every sortable column) as tiebreakers.
func compileOrder(t *Table, sorts []Sort) (string, error) {
	var keys []string
	used := map[string]bool{}
	for i, s := range sorts {
		col, ok := t.Column(s.Column)
		if !ok {
			return "", fmt.Errorf("%w: sort %d names %q, not a readable column of %s", ErrUnknownColumn, i, s.Column, t.QualifiedName())
		}
		if !col.Sortable {
			return "", invalid("sort %d: %s has type %s, which has no ordering", i, col.Name, col.Type)
		}
		if used[col.Name] {
			return "", invalid("sort %d: %s is sorted on twice", i, col.Name)
		}
		used[col.Name] = true
		k := ident(col.Name)
		if s.Desc {
			k += " DESC"
		}
		keys = append(keys, k)
	}
	// The key orders only when the role can read every column of it: a
	// hidden column in ORDER BY is a permission error, and part of a key is
	// not unique. Describe already leaves such a key out; a hand-built Table
	// gets the same rule here.
	tiebreak := t.PrimaryKey
	for _, name := range tiebreak {
		if col, ok := t.Column(name); !ok || !col.Sortable {
			tiebreak = nil
			break
		}
	}
	if len(tiebreak) == 0 {
		for _, col := range t.Columns {
			if col.Sortable {
				tiebreak = append(tiebreak, col.Name)
			}
		}
	}
	for _, name := range tiebreak {
		if !used[name] {
			used[name] = true
			keys = append(keys, ident(name))
		}
	}
	return strings.Join(keys, ", "), nil
}

// arity is each operator's value count; -1 is in's 1..MaxInValues.
var arity = map[Op]int{
	OpEq: 1, OpNeq: 1, OpLt: 1, OpLte: 1, OpGt: 1, OpGte: 1,
	OpLike: 1, OpIlike: 1, OpIsNull: 0, OpNotNull: 0, OpIn: -1,
}

var sqlOp = map[Op]string{
	OpEq: "=", OpNeq: "<>", OpLt: "<", OpLte: "<=", OpGt: ">", OpGte: ">=",
	OpLike: "LIKE", OpIlike: "ILIKE",
}

// checkValue refuses what Postgres cannot hold in a text parameter, before it
// costs a round trip: NUL bytes and invalid UTF-8.
func checkValue(v string) error {
	switch {
	case len(v) > MaxValueBytes:
		return fmt.Errorf("a value is %d bytes, the most is %d", len(v), MaxValueBytes)
	case strings.IndexByte(v, 0) >= 0:
		return fmt.Errorf("a value contains a NUL byte")
	case !utf8.ValidString(v):
		return fmt.Errorf("a value is not valid UTF-8")
	}
	return nil
}

// ident quotes one catalog name.
func ident(name string) string { return pgx.Identifier{name}.Sanitize() }

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidRequest, fmt.Sprintf(format, a...))
}
