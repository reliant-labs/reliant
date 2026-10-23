package orm

import (
	"fmt"
	"strings"

	"github.com/uptrace/bun"
)

// Operator represents a SQL comparison operator used by WithWhere and the
// Where* convenience helpers. Postgres semantics (ILIKE is native).
type Operator string

const (
	Eq              Operator = "="
	NotEq           Operator = "!="
	GreaterThan     Operator = ">"
	GreaterThanOrEq Operator = ">="
	LessThan        Operator = "<"
	LessThanOrEq    Operator = "<="
	Like            Operator = "LIKE"
	ILike           Operator = "ILIKE"
	In              Operator = "IN"
	NotIn           Operator = "NOT IN"
	IsNull          Operator = "IS NULL"
	IsNotNull       Operator = "IS NOT NULL"
)

// Order represents sort direction.
type Order string

const (
	Asc  Order = "ASC"
	Desc Order = "DESC"
)

// QueryOption is a composable mutation of a Bun SELECT query. The
// generated List/Count/Get ops and forge/pkg/crud build their filters,
// ordering, and pagination as a slice of these and apply them to a
// *bun.SelectQuery.
//
// Phase-2 note: pre-Bun this was func(*QueryBuilder) over forge's
// hand-rolled builder. The signature now targets *bun.SelectQuery — the
// engine is Bun, and there is no hand-rolled builder left.
type QueryOption func(*bun.SelectQuery)

// WithWhere adds a WHERE clause. Identifiers are bound via Bun's `?`
// placeholders with bun.Ident, so column names are safely quoted; the value
// goes through Arg (IN lists through ArgList), so it is sent as a bind
// parameter whatever its type (bind.go).
func WithWhere(column string, op Operator, value any) QueryOption {
	return func(q *bun.SelectQuery) {
		switch op {
		case IsNull:
			q.Where("? IS NULL", bun.Ident(column))
		case IsNotNull:
			q.Where("? IS NOT NULL", bun.Ident(column))
		case In:
			q.Where("? IN (?)", bun.Ident(column), ArgList(value))
		case NotIn:
			q.Where("? NOT IN (?)", bun.Ident(column), ArgList(value))
		case ILike:
			q.Where("? ILIKE ?", bun.Ident(column), Arg(value))
		case Like:
			q.Where("? LIKE ?", bun.Ident(column), Arg(value))
		default:
			q.Where("? "+string(op)+" ?", bun.Ident(column), Arg(value))
		}
	}
}

// WithOrderBy adds an ORDER BY clause. The clause may carry one or more
// comma-separated columns (validate user input with ValidateOrderBy
// first). order applies to the whole clause.
func WithOrderBy(clause string, order Order) QueryOption {
	return func(q *bun.SelectQuery) {
		for _, col := range strings.Split(clause, ",") {
			col = strings.TrimSpace(col)
			if col == "" {
				continue
			}
			// A column may already carry its own direction token
			// (validated by ValidateOrderBy); honor it, else apply order.
			if fields := strings.Fields(col); len(fields) == 2 {
				q.OrderExpr("? ?", bun.Ident(fields[0]), bun.Safe(strings.ToUpper(fields[1])))
				continue
			}
			q.OrderExpr("? ?", bun.Ident(col), bun.Safe(string(order)))
		}
	}
}

// WithLimit sets the LIMIT. The option keeps an int parameter (what callers
// and generated code pass); bun >= 1.3 takes int64, so convert at the call.
func WithLimit(limit int) QueryOption {
	return func(q *bun.SelectQuery) { q.Limit(int64(limit)) }
}

// WithOffset sets the OFFSET.
func WithOffset(offset int) QueryOption {
	return func(q *bun.SelectQuery) { q.Offset(int64(offset)) }
}

// ValidateOrderBy validates a comma-separated ORDER BY clause against a
// column allowlist.
//
// Two layers:
//
//  1. Shape: only identifier characters (letters, digits, underscores)
//     in column names, with an optional ASC/DESC direction token.
//  2. Allowlist: when allowedColumns is non-empty, every column must be
//     one of the declared columns. Shape validation alone is NOT enough:
//     an undeclared-but-identifier-shaped column (order_by=password_hash)
//     reaches the database where it can be a silent ordering no-op.
//
// Generated entity code exports its declared column list (db.<Entity>Columns)
// precisely so handlers can pass it here.
func ValidateOrderBy(clause string, allowedColumns []string) error {
	if clause == "" {
		return nil
	}
	allowed := make(map[string]bool, len(allowedColumns))
	for _, c := range allowedColumns {
		allowed[strings.ToLower(c)] = true
	}
	parts := strings.Split(clause, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return fmt.Errorf("empty order-by clause")
		}
		tokens := strings.Fields(part)
		if len(tokens) == 0 || len(tokens) > 2 {
			return fmt.Errorf("invalid order-by clause: %q", part)
		}
		col := tokens[0]
		for _, r := range col {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_') {
				return fmt.Errorf("invalid character %q in order-by column %q", string(r), col)
			}
		}
		if len(allowed) > 0 && !allowed[strings.ToLower(col)] {
			return fmt.Errorf("unknown order-by column %q (allowed: %s)", col, strings.Join(allowedColumns, ", "))
		}
		if len(tokens) == 2 {
			dir := strings.ToUpper(tokens[1])
			if dir != "ASC" && dir != "DESC" {
				return fmt.Errorf("invalid order-by direction %q (must be ASC or DESC)", tokens[1])
			}
		}
	}
	return nil
}
