//go:build integration

package obsquery_test

// Scope.EnvIDColumn against a real ClickHouse: a database whose raw tables carry
// the materialized EnvId column (as control-plane tenant schema v26 adds it, after
// half the rows were stored, so those parts compute it on read) answers every
// env-narrowed statement with exactly the rows the map lookup gives on a database
// without the column, page by page down a cursor walk.
//
//	go test -tags integration -run EnvIdColumn ./pkg/obsquery/

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/obsquery"
)

// envIDColumnDDL is control-plane's migration 0026, per table.
func envIDColumnDDL(db, table string) string {
	return "ALTER TABLE " + db + "." + table + " ADD COLUMN IF NOT EXISTS `EnvId` LowCardinality(String) MATERIALIZED ResourceAttributes['reliant.env.id'] CODEC(ZSTD(1))"
}

func TestEnvIdColumnAnswersAsTheMapLookup(t *testing.T) {
	c := startClickHouse(t)
	ctx := context.Background()
	traces := strings.Split(tracePageFixture(t), "\n")
	logs := strings.Split(envLogFixture(t), "\n")

	// "tm": the older shape, the map lookup. "tc": the column, added between
	// two inserts, so one part predates it and one stores it.
	rumHostedDB(t, c, "tm")
	rumInsert(t, c, "tm", "otel_traces", strings.Join(traces, "\n"))
	rumInsert(t, c, "tm", "otel_logs", strings.Join(logs, "\n"))
	rumHostedDB(t, c, "tc")
	rumInsert(t, c, "tc", "otel_traces", strings.Join(traces[:len(traces)/2], "\n"))
	rumInsert(t, c, "tc", "otel_logs", strings.Join(logs[:len(logs)/2], "\n"))
	for _, table := range []string{"otel_traces", "otel_logs"} {
		if err := c.Exec(ctx, envIDColumnDDL("tc", table)); err != nil {
			t.Fatal(err)
		}
	}
	rumInsert(t, c, "tc", "otel_traces", strings.Join(traces[len(traces)/2:], "\n"))
	rumInsert(t, c, "tc", "otel_logs", strings.Join(logs[len(logs)/2:], "\n"))
	var stored, all uint64
	if err := c.QueryRow(ctx, "SELECT countIf(has(cols, 'EnvId')), count() FROM (SELECT name, groupArray(column) AS cols FROM system.parts_columns "+
		"WHERE database = 'tc' AND table = 'otel_traces' AND active GROUP BY name)").Scan(&stored, &all); err != nil {
		t.Fatal(err)
	}
	if stored != 1 || all != 2 {
		t.Fatalf("tc.otel_traces parts storing EnvId/all = %d/%d: want one part from before the column and one after", stored, all)
	}

	scope := func(db string, col bool, envs ...string) obsquery.Scope {
		s := rumScopeOf(db, envs...)
		s.Start = time.Date(2026, 10, 19, 10, 0, 5, 0, time.UTC)
		s.EnvIDColumn = col
		return s
	}
	// run compiles a statement for both databases and holds their rows equal.
	run := func(label string, compile func(obsquery.Scope) (obsquery.Stmt, error), envs ...string) []tracePageRow {
		t.Helper()
		lookup, err := compile(scope("tm", false, envs...))
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		column, err := compile(scope("tc", true, envs...))
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if len(envs) > 0 && (!strings.Contains(column.SQL, "EnvId") || strings.Contains(lookup.SQL, "EnvId")) {
			t.Fatalf("%s: the column statement must name EnvId and the lookup one must not:\n%s\n---\n%s", label, column.SQL, lookup.SQL)
		}
		wantCols, want := tracePageRows(ctx, t, c, lookup)
		gotCols, got := tracePageRows(ctx, t, c, column)
		if !reflect.DeepEqual(gotCols, wantCols) || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: rows with EnvId differ from the lookup's:\n got %v %v\nwant %v %v\n%s", label, gotCols, got, wantCols, want, column.SQL)
		}
		return got
	}

	envSets := map[string][]string{"one env": {"env-prod"}, "two envs": {"env-prod", "env-staging"}, "no env": {"env-none"}}
	nonEmpty := 0
	for setName, envs := range envSets {
		// Every page of the trace list, in both orders, with and without the
		// filters whose candidate sets carry the env predicate too.
		for qName, base := range map[string]obsquery.TraceQuery{
			"all": {}, "errors": {ErrorsOnly: true}, "user": {UserID: "u1"}, "browser": {Browser: "Chrome"}, "slow": {MinDurationMs: 800},
		} {
			for _, order := range []obsquery.TraceOrder{obsquery.OrderNewest, obsquery.OrderDurationDesc} {
				q := base
				q.Order, q.Limit = order, 4
				var cursor func(obsquery.TraceQuery) *obsquery.TraceCursor
				for page := 0; page < 40; page++ {
					label := fmt.Sprintf("%s/%s/%s page %d", setName, qName, order, page)
					var last tracePageRow
					rows := run(label, func(sc obsquery.Scope) (obsquery.Stmt, error) {
						qq := q
						qq.Scope = sc
						if cursor != nil {
							qq.Cursor = cursor(qq)
						}
						return qq.Compile()
					}, envs...)
					nonEmpty += len(rows)
					if len(rows) <= q.Limit {
						break
					}
					last = rows[q.Limit-1]
					// Each database's cursor is its own (the fingerprint names the
					// database); the position is the same row's.
					cursor = func(qq obsquery.TraceQuery) *obsquery.TraceCursor {
						tok := qq.NextCursor(last.start, last.id)
						if order == obsquery.OrderDurationDesc {
							tok = qq.NextDurationCursor(last.start, last.id, last.durMs)
						}
						cur, err := obsquery.DecodeTraceCursor(tok)
						if err != nil {
							t.Fatal(err)
						}
						return cur
					}
				}
			}
		}
		run(setName+"/RED", func(sc obsquery.Scope) (obsquery.Stmt, error) { return obsquery.TraceQuery{Scope: sc}.CompileRED() }, envs...)
		run(setName+"/RED groups", func(sc obsquery.Scope) (obsquery.Stmt, error) {
			return obsquery.TraceQuery{Scope: sc, GroupBy: []string{"service.name"}}.CompileREDGroups()
		}, envs...)
		run(setName+"/overview RED", func(sc obsquery.Scope) (obsquery.Stmt, error) { return obsquery.Overview{Scope: sc}.RED() }, envs...)
		run(setName+"/overview endpoints", func(sc obsquery.Scope) (obsquery.Stmt, error) { return obsquery.Overview{Scope: sc}.REDByEndpoint() }, envs...)
		run(setName+"/overview trend", func(sc obsquery.Scope) (obsquery.Stmt, error) { return obsquery.Overview{Scope: sc}.Trend(60) }, envs...)
		nonEmpty += len(run(setName+"/overview logs", func(sc obsquery.Scope) (obsquery.Stmt, error) { return obsquery.Overview{Scope: sc}.Logs() }, envs...))
		nonEmpty += len(run(setName+"/logs", func(sc obsquery.Scope) (obsquery.Stmt, error) {
			return obsquery.LogQuery{Scope: sc, Limit: 200}.Compile()
		}, envs...))
		run(setName+"/trace spans", func(sc obsquery.Scope) (obsquery.Stmt, error) {
			return obsquery.TraceSpans{Scope: sc, TraceID: fmt.Sprintf("%032x", 1*7919+1)}.Compile()
		}, envs...)
	}
	if nonEmpty == 0 {
		t.Fatal("every statement was empty: the fixture does not exercise the predicate")
	}
}

// envLogFixture is 60 log records over three environments, one with no
// reliant.env.id at all.
func envLogFixture(t *testing.T) string {
	t.Helper()
	base := time.Date(2026, 10, 19, 10, 0, 10, 0, time.UTC)
	var lines []string
	for i := 0; i < 60; i++ {
		res := map[string]string{"service.name": []string{"api", "worker"}[i%2]}
		switch i % 4 {
		case 0, 1:
			res["reliant.env.id"] = "env-prod"
		case 2:
			res["reliant.env.id"] = "env-staging"
		}
		raw, err := json.Marshal(map[string]any{
			"Timestamp": base.Add(time.Duration(i) * time.Second).Format("2006-01-02 15:04:05.000000000"), "ServiceName": res["service.name"],
			"SeverityText": []string{"INFO", "ERROR", "WARN"}[i%3], "Body": fmt.Sprintf("record %d", i), "ResourceAttributes": res,
		})
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(raw))
	}
	return strings.Join(lines, "\n")
}
