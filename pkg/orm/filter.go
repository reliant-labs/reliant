package orm

import (
	"strings"

	"github.com/uptrace/bun"
)

// Filter convenience functions that return QueryOption.
// Each wraps WithWhere using the appropriate Operator.

// WhereEq matches rows whose column equals value.
func WhereEq(column string, value any) QueryOption {
	return WithWhere(column, Eq, value)
}

// WhereNotEq matches rows whose column differs from value (SQL !=, so a
// NULL column never matches).
func WhereNotEq(column string, value any) QueryOption {
	return WithWhere(column, NotEq, value)
}

// WhereGt matches rows whose column is greater than value.
func WhereGt(column string, value any) QueryOption {
	return WithWhere(column, GreaterThan, value)
}

// WhereGte matches rows whose column is at least value.
func WhereGte(column string, value any) QueryOption {
	return WithWhere(column, GreaterThanOrEq, value)
}

// WhereLt matches rows whose column is less than value.
func WhereLt(column string, value any) QueryOption {
	return WithWhere(column, LessThan, value)
}

// WhereLte matches rows whose column is at most value.
func WhereLte(column string, value any) QueryOption {
	return WithWhere(column, LessThanOrEq, value)
}

// WhereLike matches rows whose column matches the LIKE pattern value. The
// pattern's wildcards are live; use WherePrefix or EscapeLike to match text
// literally.
func WhereLike(column string, value any) QueryOption {
	return WithWhere(column, Like, value)
}

// WhereILike is WhereLike, case-insensitive (ILIKE).
func WhereILike(column string, value any) QueryOption {
	return WithWhere(column, ILike, value)
}

// WhereIn matches rows whose column is one of values, a slice.
func WhereIn(column string, values any) QueryOption {
	return WithWhere(column, In, values)
}

// WhereNotIn matches rows whose column is none of values, a slice.
func WhereNotIn(column string, values any) QueryOption {
	return WithWhere(column, NotIn, values)
}

// WhereIsNull matches rows whose column is NULL.
func WhereIsNull(column string) QueryOption {
	return WithWhere(column, IsNull, nil)
}

// WhereIsNotNull matches rows whose column is not NULL.
func WhereIsNotNull(column string) QueryOption {
	return WithWhere(column, IsNotNull, nil)
}

// WherePrefix matches the rows whose column starts with prefix, taken
// literally: `column LIKE '<escaped prefix>%' ESCAPE '\'`. The %, _ and \ in
// prefix match themselves (EscapeLike), so a key prefix "weekly_" does not
// also match "weeklyX". It is the mapping for a List request field declared
// `(forge.v1.filter) = {op: LIST_FILTER_OP_PREFIX}`.
func WherePrefix(column, prefix string) QueryOption {
	return func(q *bun.SelectQuery) {
		q.Where(`? LIKE ? ESCAPE '\'`, bun.Ident(column), Arg(EscapeLike(prefix)+"%"))
	}
}

// likeEscaper backslash-escapes LIKE's two wildcards and its escape
// character itself.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// EscapeLike returns s with LIKE's wildcards (% and _) and the escape
// character (\) escaped, for a pattern that must match s literally under
// `ESCAPE '\'`.
func EscapeLike(s string) string { return likeEscaper.Replace(s) }

// WhereILikeAny matches value case-insensitively against ANY of the
// given columns: (c1 ILIKE ? OR c2 ILIKE ? ...), AND-ed with the other
// WHERE clauses. This is the canonical mapping for a `search` filter
// field: it spans the entity's declared string columns instead of
// inventing a phantom `search` column.
//
// The OR group is grouped with WhereGroup so it composes correctly with
// surrounding AND clauses (soft-delete, pagination cursor).
func WhereILikeAny(columns []string, value any) QueryOption {
	return func(q *bun.SelectQuery) {
		if len(columns) == 0 {
			return
		}
		q.WhereGroup(" AND ", func(sq *bun.SelectQuery) *bun.SelectQuery {
			for _, col := range columns {
				sq = sq.WhereOr("? ILIKE ?", bun.Ident(col), Arg(value))
			}
			return sq
		})
	}
}
