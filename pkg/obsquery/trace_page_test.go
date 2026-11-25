package obsquery

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// The page is selected before its columns are computed. Each page column is
// an aggregate over every span of its trace, and computing them for every
// trace of the window before the LIMIT kept ~100 cost a prod ListTraces
// 5-10s. So the statement's IN subquery (phase 1) keeps the page's trace ids
// by the cheap aggregates alone, holding every HAVING term, the cursor, the
// order and the limit; the outer aggregate (phase 2) computes the page columns
// over the spans of those ids, filtered by nothing else a span of a kept trace
// could fail. This holds in both orders, with and without a cursor and the
// filters.
func TestTracePageSelectsTheTracesBeforeTheirColumns(t *testing.T) {
	filtered := func(q TraceQuery) TraceQuery {
		q.Scope.Filters = []AttrFilter{{Key: "http.route", Value: "/x"}, {Key: "k8s.workload.name", Value: "api"}}
		q.ErrorsOnly, q.MinDurationMs, q.RootSpanName, q.UserID = true, 5, "Get", "u1"
		return q
	}
	withCursor := func(q TraceQuery) TraceQuery {
		var tok string
		if q.Order == OrderDurationDesc {
			tok = q.NextDurationCursor(t0.Add(time.Minute), "abc", 12.5)
		} else {
			tok = q.NextCursor(t0.Add(time.Minute), "abc")
		}
		c, err := DecodeTraceCursor(tok)
		if err != nil {
			t.Fatal(err)
		}
		q.Cursor = c
		return q
	}
	cases := map[string]TraceQuery{}
	for _, order := range []TraceOrder{OrderNewest, OrderDurationDesc} {
		base := TraceQuery{Scope: scope(), Limit: 7, Order: order}
		name := "newest"
		if order == OrderDurationDesc {
			name = "slowest"
		}
		cases[name] = base
		cases[name+"/cursor"] = withCursor(base)
		cases[name+"/filtered"] = filtered(base)
		cases[name+"/filtered/cursor"] = withCursor(filtered(base))
	}
	for name, q := range cases {
		st, err := q.Compile()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		assertParameterized(t, st)
		inner, outer := cutSubquery(t, st.SQL, "TraceId IN (SELECT trace_id FROM (")
		order := "ORDER BY " + q.pageOrder() + "\n"
		limit := "LIMIT " + strconv.Itoa(q.Limit+1)

		// Phase 2: the page columns, the order and the limit, over the
		// spans of the ids alone.
		if got := selectAliases(t, outer); len(got) != 25 || got[0] != "trace_id" || got[24] != "backend_versions" {
			t.Errorf("%s: outer columns = %v", name, got)
		}
		if !strings.HasSuffix(outer, "GROUP BY trace_id\n"+order+limit) {
			t.Errorf("%s: outer rows are not ordered and limited as the page:\n%s", name, outer)
		}
		for _, no := range []string{"HAVING", "TraceId IN (SELECT TraceId FROM"} {
			if strings.Contains(outer, no) {
				t.Errorf("%s: the outer aggregate repeats %q, which phase 1 already applied:\n%s", name, no, outer)
			}
		}

		// Phase 1: only the cheap aggregates, every filter, the cursor, the
		// order and the limit.
		if got := selectAliases(t, inner); strings.Join(got, ",") != "trace_id,start,dur_ms,root_name,error_count" {
			t.Errorf("%s: phase 1 columns = %v", name, got)
		}
		for _, no := range []string{"anyIf(", "groupUniqArray", "maxIf(", "count() "} {
			if strings.Contains(inner, no) {
				t.Errorf("%s: phase 1 computes the page column %q:\n%s", name, no, inner)
			}
		}
		if !strings.Contains(inner, order) || !strings.Contains(inner, limit+"\n") {
			t.Errorf("%s: phase 1 does not keep the page in its order:\n%s", name, inner)
		}
		var want []string
		if q.Cursor != nil {
			if q.Order == OrderDurationDesc {
				want = append(want, "("+traceDurNanoExpr+", start, trace_id) < (")
			} else {
				want = append(want, "(start, trace_id) < (fromUnixTimestamp64Nano(")
			}
		}
		if q.ErrorsOnly {
			want = append(want, "HAVING countIf(if(mapContains(SpanAttributes, ", "error_count > 0", "dur_ms >= {", "positionCaseInsensitive(root_name, {",
				"TraceId IN (SELECT TraceId FROM", "SpanAttributes['user.id']")
		}
		for _, w := range want {
			if !strings.Contains(inner, w) {
				t.Errorf("%s: phase 1 lacks %q:\n%s", name, w, inner)
			}
		}
		if q.Cursor != nil && !strings.Contains(inner, "HAVING") {
			t.Errorf("%s: the cursor is not applied in phase 1:\n%s", name, inner)
		}
		// The spans a kept trace's columns are computed over are the
		// one-pass statement's: the range and the resource filters.
		if q.Scope.Filters != nil && (!strings.Contains(outer, "AND multiIf(ResourceAttributes['k8s.workload.name']") || strings.Contains(outer, "mapContains(SpanAttributes")) {
			t.Errorf("%s: outer spans are not the range and the resource filters:\n%s", name, outer)
		}
	}
}

// cutSubquery returns the parenthesised subquery that opens at marker (inner,
// without the marker), and sql with it removed (outer).
func cutSubquery(t *testing.T, sql, marker string) (inner, outer string) {
	t.Helper()
	i := strings.Index(sql, marker)
	if i < 0 {
		t.Fatalf("no %q in:\n%s", marker, sql)
	}
	depth := 0
	for k := i + len("TraceId IN "); k < len(sql); k++ {
		switch sql[k] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				// The subquery is indented by four; undo it so it reads as
				// a statement of its own.
				inner = strings.ReplaceAll(sql[i+len(marker):k], "\n    ", "\n")
				return strings.TrimPrefix(inner, "\n"), sql[:i] + sql[k+1:]
			}
		}
	}
	t.Fatalf("unbalanced subquery in:\n%s", sql)
	return "", ""
}
