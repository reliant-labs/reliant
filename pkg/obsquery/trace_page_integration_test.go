//go:build integration

package obsquery_test

// The two-phase trace page against a real ClickHouse: on a fixture of more
// traces than a page holds, every page of every filter and order holds
// exactly the rows, columns and order of the one-pass statement it replaced,
// down to the last page of a cursor walk.
//
//	go test -tags integration -run TracePageTwoPhase ./pkg/obsquery/

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"

	"github.com/reliant-labs/forge/pkg/obsquery"
)

func TestTracePageTwoPhaseMatchesOnePass(t *testing.T) {
	c := startClickHouse(t)
	rumHostedDB(t, c, "tp")
	rumInsert(t, c, "tp", "otel_traces", tracePageFixture(t))

	sc := rumScopeOf("tp", "env-prod")
	// Spans before the range start: a trace straddling it is aggregated over
	// its in-range spans alone, in both forms.
	sc.Start = time.Date(2026, 10, 19, 10, 0, 5, 0, time.UTC)
	withFilters := func(fs ...obsquery.AttrFilter) obsquery.Scope {
		s := sc
		s.Filters = fs
		return s
	}
	apiOnly := sc
	apiOnly.Services = []string{"api"}
	queries := map[string]obsquery.TraceQuery{
		"all":           {Scope: sc},
		"errors":        {Scope: sc, ErrorsOnly: true},
		"slow":          {Scope: sc, MinDurationMs: 800},
		"root name":     {Scope: sc, RootSpanName: "list"},
		"user":          {Scope: sc, UserID: "u1"},
		"browser":       {Scope: sc, Browser: "Chrome"},
		"backend build": {Scope: sc, BackendVersion: "v1.7.91"},
		"span filter":   {Scope: withFilters(obsquery.AttrFilter{Key: "http.route", Value: "/x"})},
		"resource":      {Scope: withFilters(obsquery.AttrFilter{Key: "k8s.workload.name", Value: "worker"})},
		"service":       {Scope: apiOnly},
		"combined":      {Scope: withFilters(obsquery.AttrFilter{Key: "http.route", Value: "/x"}), ErrorsOnly: true, MinDurationMs: 50},
	}
	ctx := context.Background()
	multiPage := 0
	for name, q := range queries {
		for _, order := range []obsquery.TraceOrder{obsquery.OrderNewest, obsquery.OrderDurationDesc} {
			q := q
			q.Order, q.Limit = order, 4
			label := fmt.Sprintf("%s/%s", name, map[obsquery.TraceOrder]string{obsquery.OrderNewest: "newest", obsquery.OrderDurationDesc: "slowest"}[order])
			seen := 0
			for page := 0; ; page++ {
				if page > 40 {
					t.Fatalf("%s: the cursor walk did not end", label)
				}
				two, err := q.Compile()
				if err != nil {
					t.Fatalf("%s: %v", label, err)
				}
				one, err := q.CompileOnePass()
				if err != nil {
					t.Fatalf("%s: %v", label, err)
				}
				gotCols, got := tracePageRows(ctx, t, c, two)
				wantCols, want := tracePageRows(ctx, t, c, one)
				if !reflect.DeepEqual(gotCols, wantCols) {
					t.Fatalf("%s page %d: columns %v, one-pass %v", label, page, gotCols, wantCols)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("%s page %d: two-phase rows differ from one-pass:\n got %v\nwant %v\n%s", label, page, got, want, two.SQL)
				}
				seen += min(len(got), q.Limit)
				if len(got) <= q.Limit {
					break
				}
				// The next page continues from the last row the page returns.
				last := got[q.Limit-1]
				start, id := last.start, last.id
				if order == obsquery.OrderDurationDesc {
					tok := q.NextDurationCursor(start, id, last.durMs)
					q.Cursor, err = obsquery.DecodeTraceCursor(tok)
				} else {
					q.Cursor, err = obsquery.DecodeTraceCursor(q.NextCursor(start, id))
				}
				if err != nil {
					t.Fatal(err)
				}
				if page == 1 {
					multiPage++
				}
			}
			if name == "all" && seen < 3*q.Limit {
				t.Errorf("%s: the fixture holds %d traces, want more than three pages", label, seen)
			}
		}
	}
	if multiPage < len(queries) {
		t.Errorf("only %d of %d order/filter walks reached a third page; the fixture does not exercise the cursor", multiPage, 2*len(queries))
	}
}

type tracePageRow struct {
	id    string
	start time.Time
	durMs float64
	cells []string
}

// tracePageRows runs st and returns its column names and every row, each cell
// rendered as text.
func tracePageRows(ctx context.Context, t *testing.T, c clickhouse.Conn, st obsquery.Stmt) ([]string, []tracePageRow) {
	t.Helper()
	rows, err := c.Query(paramCtx(ctx, st), st.SQL)
	if err != nil {
		t.Fatalf("%v\n%s", err, st.SQL)
	}
	defer rows.Close()
	cols := rows.Columns()
	types := rows.ColumnTypes()
	var out []tracePageRow
	for rows.Next() {
		vals := make([]any, len(types))
		for i, ct := range types {
			vals[i] = reflect.New(ct.ScanType()).Interface()
		}
		if err := rows.Scan(vals...); err != nil {
			t.Fatal(err)
		}
		r := tracePageRow{}
		for i, v := range vals {
			x := reflect.ValueOf(v).Elem().Interface()
			r.cells = append(r.cells, fmt.Sprintf("%v", x))
			switch cols[i] {
			case "trace_id":
				r.id = x.(string)
			case "start":
				r.start = x.(time.Time)
			case "dur_ms":
				r.durMs = x.(float64)
			}
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return cols, out
}

// tracePageFixture is 72 traces with every shape the page columns and filters
// read: browser and server spans, users and sessions, builds and locations,
// errors, two root names, orphans, spans before the range, another
// environment's traces, and ties on start and on duration so the keyset's
// tie-breaks decide the order.
func tracePageFixture(t *testing.T) string {
	t.Helper()
	base := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	var lines []string
	span := func(tr int, n int, parent string, at time.Time, dur time.Duration, svc, name, status string, res, attrs map[string]string) string {
		id := fmt.Sprintf("%016x", tr*100+n+1)
		raw, err := json.Marshal(map[string]any{
			"Timestamp": at.Format("2006-01-02 15:04:05.000000000"), "TraceId": fmt.Sprintf("%032x", tr*7919+1), "SpanId": id,
			"ParentSpanId": parent, "SpanName": name, "SpanKind": "Server", "ServiceName": svc,
			"ResourceAttributes": res, "SpanAttributes": attrs, "Duration": dur.Nanoseconds(), "StatusCode": status,
		})
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(raw))
		return id
	}
	for tr := 0; tr < 72; tr++ {
		env := "env-prod"
		if tr%9 == 4 {
			env = "env-staging"
		}
		// Three traces start at each instant; every seventh lasts exactly 500ms.
		start := base.Add(time.Duration(tr/3) * time.Second)
		if tr%11 == 0 {
			start = base.Add(-2 * time.Second) // begins before the range
		}
		dur := time.Duration(1+(tr*37)%1900) * time.Millisecond
		if tr%7 == 0 {
			dur = 500 * time.Millisecond
		}
		name := "/svc.v1.Svc/Get"
		if tr%3 == 1 {
			name = "/svc.v1.Svc/List"
		}
		root := ""
		if tr%4 == 0 {
			// A browser started it.
			ua := "Chrome"
			if tr%8 == 4 {
				ua = "Firefox"
			}
			res := map[string]string{
				"service.name": "reliant-web", "reliant.env.id": env, "rum.sessionId": fmt.Sprintf("%032x", tr%5+1),
				"user_agent.name": ua, "user_agent.version": "130", "os.name": "macOS", "os.version": "15",
				"service.version": fmt.Sprintf("f%d", tr%2), "geo.country.iso_code": "DE", "geo.region.name": "Berlin", "geo.locality.name": "Berlin",
			}
			root = span(tr, 0, "", start, dur, "reliant-web", "documentFetch", "Unset", res, map[string]string{"userId": fmt.Sprintf("web-%d", tr%3)})
		}
		parent := root
		if tr%13 == 6 {
			parent = "ffffffffffffffff" // an orphaned trace: no parentless span
		}
		children := 1 + tr%4
		for n := 1; n <= children; n++ {
			svc, workload := "api", "api"
			if n%2 == 0 {
				svc, workload = "worker", "worker"
			}
			status := "Unset"
			if tr%5 == 0 && n == children {
				status = "Error"
			}
			res := map[string]string{"service.name": svc, "reliant.env.id": env, "k8s.deployment.name": workload,
				"service.version": fmt.Sprintf("v1.7.9%d", (tr+n)%3)}
			attrs := map[string]string{"http.route": []string{"/x", "/y"}[(tr+n)%2]}
			if (tr+n)%3 == 0 {
				attrs["user.id"] = fmt.Sprintf("u%d", tr%4)
				attrs["session.id"] = fmt.Sprintf("%032x", tr%5+1)
			}
			at := start.Add(time.Duration(n) * time.Millisecond)
			d := dur - time.Duration(2*n)*time.Millisecond
			if d <= 0 {
				d = time.Millisecond
			}
			id := span(tr, n, parent, at, d, svc, name, status, res, attrs)
			if parent == "" {
				parent = id
			}
		}
	}
	return strings.Join(lines, "\n")
}
