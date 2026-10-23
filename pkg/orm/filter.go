package orm

import "github.com/uptrace/bun"

// Filter convenience functions that return QueryOption.
// Each wraps WithWhere using the appropriate Operator.

func WhereEq(column string, value any) QueryOption {
	return WithWhere(column, Eq, value)
}

func WhereNotEq(column string, value any) QueryOption {
	return WithWhere(column, NotEq, value)
}

func WhereGt(column string, value any) QueryOption {
	return WithWhere(column, GreaterThan, value)
}

func WhereGte(column string, value any) QueryOption {
	return WithWhere(column, GreaterThanOrEq, value)
}

func WhereLt(column string, value any) QueryOption {
	return WithWhere(column, LessThan, value)
}

func WhereLte(column string, value any) QueryOption {
	return WithWhere(column, LessThanOrEq, value)
}

func WhereLike(column string, value any) QueryOption {
	return WithWhere(column, Like, value)
}

func WhereILike(column string, value any) QueryOption {
	return WithWhere(column, ILike, value)
}

func WhereIn(column string, values any) QueryOption {
	return WithWhere(column, In, values)
}

func WhereNotIn(column string, values any) QueryOption {
	return WithWhere(column, NotIn, values)
}

func WhereIsNull(column string) QueryOption {
	return WithWhere(column, IsNull, nil)
}

func WhereIsNotNull(column string) QueryOption {
	return WithWhere(column, IsNotNull, nil)
}

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
