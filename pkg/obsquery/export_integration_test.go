//go:build integration

package obsquery

import "strconv"

// CompileOnePass is the page statement as Compile built it before it selected
// the page first: one aggregate computing every page column for every
// candidate trace, then HAVING, ORDER BY and LIMIT. The integration test holds
// Compile to the same rows.
func (q TraceQuery) CompileOnePass() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeTraces); err != nil {
		return Stmt{}, err
	}
	b := newBuilder(q.Scope)
	p, err := q.traceParts(b, true, false)
	if err != nil {
		return Stmt{}, err
	}
	return b.stmt(tracePageCols + p.groupCols + "\n" +
		"FROM " + b.table(q.Scope, "otel_traces") + "\n" + where(p.conds()) + "\n" +
		"GROUP BY trace_id" + p.havingClause() + "\n" +
		"ORDER BY " + q.pageOrder() + "\n" +
		"LIMIT " + strconv.Itoa(q.Limit+1)), nil
}
