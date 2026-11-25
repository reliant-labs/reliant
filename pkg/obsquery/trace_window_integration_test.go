//go:build integration

package obsquery_test

// The windowed trace page against a real ClickHouse: on the two-phase test's
// fixture, at every page of every filter's cursor walk, a window
// WindowPageExact accepts returns exactly Compile's rows, columns and order.
// The windows include ones that cut through traces (a trace with spans on
// both sides of the window's start) and ones holding too few traces, so both
// verdicts are exercised.
//
//	go test -tags integration -run TracePageWindow ./pkg/obsquery/

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/obsquery"
)

func TestTracePageWindowMatchesCompile(t *testing.T) {
	c := startClickHouse(t)
	rumHostedDB(t, c, "tw")
	rumInsert(t, c, "tw", "otel_traces", tracePageFixture(t))

	base := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	sc := rumScopeOf("tw", "env-prod")
	sc.Start = base.Add(5 * time.Second)
	// The fixture's traces start a second apart from base, and a trace's
	// spans 1-4ms after its start: a window from 2ms past a start cuts
	// through the traces that began then.
	var froms []time.Time
	for s := 0; s <= 26; s += 3 {
		froms = append(froms, base.Add(time.Duration(s)*time.Second), base.Add(time.Duration(s)*time.Second+2*time.Millisecond))
	}
	withFilters := func(fs ...obsquery.AttrFilter) obsquery.Scope {
		s := sc
		s.Filters = fs
		return s
	}
	queries := map[string]obsquery.TraceQuery{
		"all":         {Scope: sc},
		"errors":      {Scope: sc, ErrorsOnly: true},
		"slow":        {Scope: sc, MinDurationMs: 800},
		"root name":   {Scope: sc, RootSpanName: "list"},
		"user":        {Scope: sc, UserID: "u1"},
		"browser":     {Scope: sc, Browser: "Chrome"},
		"span filter": {Scope: withFilters(obsquery.AttrFilter{Key: "http.route", Value: "/x"})},
		"resource":    {Scope: withFilters(obsquery.AttrFilter{Key: "k8s.workload.name", Value: "worker"})},
		"combined":    {Scope: withFilters(obsquery.AttrFilter{Key: "http.route", Value: "/x"}), ErrorsOnly: true, MinDurationMs: 50},
	}
	ctx := context.Background()
	exact, inexact, cutExact := 0, 0, 0
	for name, q := range queries {
		q.Limit = 3
		for page := 0; ; page++ {
			if page > 40 {
				t.Fatalf("%s: the cursor walk did not end", name)
			}
			full, err := q.Compile()
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			wantCols, want := tracePageRows(ctx, t, c, full)
			for _, from := range froms {
				st, err := q.CompileWindow(from)
				if err != nil {
					t.Fatalf("%s: window %v: %v", name, from, err)
				}
				gotCols, got := tracePageRows(ctx, t, c, st)
				starts := make([]time.Time, len(got))
				for i, r := range got {
					starts[i] = r.start
				}
				if !q.WindowPageExact(from, starts) {
					inexact++
					continue
				}
				exact++
				if from.Nanosecond() != 0 {
					cutExact++
				}
				if !reflect.DeepEqual(gotCols, wantCols) || !reflect.DeepEqual(got, want) {
					t.Fatalf("%s page %d, window from %v: accepted rows differ from Compile's:\n got %v\nwant %v\n%s",
						name, page, from.Format(time.StampMicro), got, want, st.SQL)
				}
			}
			if len(want) <= q.Limit {
				break
			}
			last := want[q.Limit-1]
			if q.Cursor, err = obsquery.DecodeTraceCursor(q.NextCursor(last.start, last.id)); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Logf("windows accepted %d, refused %d, accepted while cutting through traces %d", exact, inexact, cutExact)
	if exact == 0 || inexact == 0 || cutExact == 0 {
		t.Errorf("accepted %d, refused %d, accepted cutting through traces %d: the fixture does not exercise both verdicts", exact, inexact, cutExact)
	}
}
