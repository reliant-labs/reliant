package obsbackend

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/dashspec"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

// fakeRunner answers by substring of the statement and records what ran.
type fakeRunner struct {
	ran     []obsquery.Stmt
	limits  []Limits
	answers []fakeAnswer
	err     error
}

type fakeAnswer struct {
	contains string
	res      Result
}

func (f *fakeRunner) Run(_ context.Context, st obsquery.Stmt, lim Limits) (Result, error) {
	f.ran = append(f.ran, st)
	f.limits = append(f.limits, lim)
	if f.err != nil {
		return Result{}, f.err
	}
	for _, a := range f.answers {
		if strings.Contains(st.SQL, a.contains) {
			return a.res, nil
		}
	}
	return Result{}, nil
}

func num64(s string) json.Number { return json.Number(s) }

func rng() Narrow {
	return Narrow{Range: Range{From: time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC), To: time.Date(2026, 10, 22, 11, 0, 0, 0, time.UTC)}}
}

func logRow(ts, svc, body, trace string, hash uint64) []any {
	return []any{ts, "ERROR", num64("17"), svc, body, trace, "", map[string]any{"http.route": "/x"}, map[string]any{}, num64(fmt.Sprint(hash))}
}

func TestLogsPagesWithTheCompilersCursor(t *testing.T) {
	fr := &fakeRunner{answers: []fakeAnswer{{"otel_logs", Result{Rows: [][]any{
		logRow("2026-10-22 10:59:59.000000001", "api", "newest", "ab", 3),
		logRow("2026-10-22 10:59:58.000000000", "api", "older", "", 2),
		logRow("2026-10-22 10:59:57.000000000", "api", "the extra row", "", 1),
	}}}}}
	l := Local{Env: "dev", Runner: fr}
	res, err := l.Logs(context.Background(), LogsRequest{Narrow: rng(), Query: "service:api", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Logs) != 2 || res.Logs[0].Body != "newest" || res.Logs[0].Attributes["http.route"] != "/x" || res.Logs[0].Hash != 3 {
		t.Fatalf("logs = %+v", res.Logs)
	}
	if res.NextPageToken == "" {
		t.Fatal("the extra row means a next page")
	}
	cur, err := obsquery.DecodeLogCursor(res.NextPageToken)
	if err != nil || cur.Hash != 2 || cur.TsNano != time.Date(2026, 10, 22, 10, 59, 58, 0, time.UTC).UnixNano() {
		t.Errorf("cursor must resume after the last RETURNED row: %+v %v", cur, err)
	}
	sql := fr.ran[0].SQL
	if !strings.Contains(sql, "default.otel_logs") || !strings.Contains(sql, "LIMIT 3") {
		t.Errorf("sql:\n%s", sql)
	}
	if fr.limits[0].Break || fr.limits[0].MaxRows == 0 {
		t.Errorf("a structured query throws on overflow: %+v", fr.limits[0])
	}
}

func TestLogsTraceNarrowsInsideTheQueryAndOrIsContained(t *testing.T) {
	if got := WithTrace("", "ABC"); got != "trace_id:abc" {
		t.Errorf("got %q", got)
	}
	if got := WithTrace("body:a OR body:b", "ABC"); got != "trace_id:abc (body:a OR body:b)" {
		t.Errorf("an OR must not escape the trace: %q", got)
	}
	fr := &fakeRunner{}
	if _, err := (Local{Env: "dev", Runner: fr}).Logs(context.Background(), LogsRequest{Narrow: rng(), Query: "body:a OR body:b", TraceID: "ABC", Limit: 5}); err != nil {
		t.Fatal(err)
	}
	if got := fr.ran[0].Params; !hasValue(got, "abc") {
		t.Errorf("the trace id is a bound parameter: %v", got)
	}
}

func hasValue(m map[string]string, v string) bool {
	for _, x := range m {
		if x == v {
			return true
		}
	}
	return false
}

func TestExplainCompilesWithoutRunning(t *testing.T) {
	fr := &fakeRunner{}
	l := Local{Env: "dev", Runner: fr}
	n := rng()
	n.Explain = true
	for name, run := range map[string]func() ([]Statement, error){
		"logs": func() ([]Statement, error) {
			r, e := l.Logs(context.Background(), LogsRequest{Narrow: n, Limit: 5})
			return r.Statements, e
		},
		"traces": func() ([]Statement, error) {
			r, e := l.Traces(context.Background(), TracesRequest{Narrow: n, Limit: 5})
			return r.Statements, e
		},
		"trace": func() ([]Statement, error) {
			r, e := l.Trace(context.Background(), TraceRequest{Narrow: n, TraceID: "ab", LogLimit: 3})
			return r.Statements, e
		},
		"endpoints": func() ([]Statement, error) {
			r, e := l.Endpoints(context.Background(), EndpointsRequest{Narrow: n})
			return r.Statements, e
		},
		"metrics": func() ([]Statement, error) {
			r, e := l.MetricsList(context.Background(), MetricsListRequest{Narrow: n, Limit: 5})
			return r.Statements, e
		},
		"query": func() ([]Statement, error) {
			r, e := l.Query(context.Background(), QueryRequest{Narrow: n, SQL: "SELECT 1", Limit: 5})
			return r.Statements, e
		},
		"metric": func() ([]Statement, error) {
			r, e := l.MetricsQuery(context.Background(), MetricsQueryRequest{Narrow: n, Panel: obsquery.Panel{Kind: "line", Signal: "metrics",
				Series: []obsquery.PanelSeries{{Label: "A", Field: "m", Agg: "rate", MetricType: "sum"}}}})
			return r.Statements, e
		},
		"metric describe": func() ([]Statement, error) {
			r, e := l.MetricsDescribe(context.Background(), MetricsDescribeRequest{Narrow: n, Name: "m"})
			return r.Statements, e
		},
		"metric exemplars": func() ([]Statement, error) {
			r, e := l.MetricsExemplars(context.Background(), MetricsExemplarsRequest{Narrow: n, Name: "m", From: n.Range.From, To: n.Range.To})
			return r.Statements, e
		},
	} {
		stmts, err := run()
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if len(stmts) == 0 || stmts[0].SQL == "" || stmts[0].Purpose == "" {
			t.Errorf("%s: explain returned no statement: %+v", name, stmts)
		}
	}
	if len(fr.ran) != 0 {
		t.Errorf("--explain ran %d statements", len(fr.ran))
	}
}

func TestTracesSlowSortsTheNewestWindowAndHasNoNextPage(t *testing.T) {
	row := func(id string, dur float64, errs int) []any {
		return []any{id, "2026-10-22 10:30:00.000000000", num64(fmt.Sprint(dur)), "api", "GET /x", num64("1"), num64("3"), num64(fmt.Sprint(errs)), num64("1"), []any{"api"}, ""}
	}
	fr := &fakeRunner{answers: []fakeAnswer{
		{"quantilesExactWeighted", Result{Rows: [][]any{{num64("3"), num64("1"), []any{num64("10"), num64("90"), num64("100")}}}}},
		{"GROUP BY trace_id", Result{Rows: [][]any{row("aa", 10, 0), row("bb", 250, 1), row("cc", 40, 0)}}},
	}}
	res, err := (Local{Env: "dev", Runner: fr}).Traces(context.Background(), TracesRequest{Narrow: rng(), Slow: true, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Traces) != 2 || res.Traces[0].TraceID != "bb" || res.Traces[1].TraceID != "cc" {
		t.Errorf("slowest first, cut to the limit: %+v", res.Traces)
	}
	if res.SortedFrom != 3 || res.NextPageToken != "" {
		t.Errorf("sorted_from %d token %q", res.SortedFrom, res.NextPageToken)
	}
	if !strings.Contains(fr.ran[0].SQL, fmt.Sprintf("LIMIT %d", SlowWindow+1)) {
		t.Errorf("--slow reads the compiler's maximum page:\n%s", fr.ran[0].SQL)
	}
	if s := res.Summary; s == nil || s.Traces != 3 || s.ErrorTraces != 1 || s.P95Ms != 90 {
		t.Errorf("summary = %+v", s)
	}
	if _, err := (Local{Env: "dev", Runner: fr}).Traces(context.Background(), TracesRequest{Narrow: rng(), Slow: true, Limit: 2, PageToken: "x"}); err == nil {
		t.Error("--slow has no next page")
	}
}

func TestTraceNotFoundIsExit1WithARemedy(t *testing.T) {
	_, err := (Local{Env: "dev", Runner: &fakeRunner{}}).Trace(context.Background(), TraceRequest{Narrow: rng(), TraceID: "0af7"})
	be, ok := err.(*Error)
	if !ok || be.ExitCode() != ExitWrong || !strings.Contains(be.Error(), "no trace 0af7") || len(be.Suggested) == 0 {
		t.Errorf("err = %#v", err)
	}
	if _, err := (Local{Env: "dev", Runner: &fakeRunner{}}).Trace(context.Background(), TraceRequest{Narrow: rng(), TraceID: "not hex!"}); err == nil || err.(*Error).ExitCode() != ExitWrong {
		t.Errorf("a malformed id is the caller's fault: %v", err)
	}
}

func spanRow(id, parent, name, kind, status string, durNs int64, events ...string) []any {
	var evNames, evTimes, evAttrs []any
	for _, e := range events {
		evNames = append(evNames, e)
		evTimes = append(evTimes, "2026-10-22 10:30:00.000000000")
		evAttrs = append(evAttrs, map[string]any{"exception.type": "*pgconn.PgError", "exception.message": "relation \"x\" does not exist"})
	}
	return []any{"2026-10-22 10:30:00.000000000", id, parent, name, kind, "api", num64(fmt.Sprint(durNs)), status, "", map[string]any{"db.system": "postgresql"},
		map[string]any{}, evTimes, evNames, evAttrs, []any{}, []any{}}
}

func TestTraceReadsSpansEventsAndCorrelatedLogs(t *testing.T) {
	fr := &fakeRunner{answers: []fakeAnswer{
		{"otel_traces", Result{Rows: [][]any{
			spanRow("s1", "", "GET /x", "SPAN_KIND_SERVER", "STATUS_CODE_ERROR", 5_000_000, "exception"),
			spanRow("s2", "s1", "SELECT", "Client", "Unset", 1_000_000),
		}}},
		{"otel_logs", Result{Rows: [][]any{logRow("2026-10-22 10:30:00.000000000", "api", "boom", "ab", 1)}}},
	}}
	res, err := (Local{Env: "dev", Runner: fr}).Trace(context.Background(), TraceRequest{Narrow: rng(), TraceID: "AB", LogLimit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if res.TraceID != "ab" || len(res.Spans) != 2 || len(res.Logs) != 1 {
		t.Fatalf("res = %+v", res)
	}
	s := res.Spans[0]
	if s.Kind != "server" || s.StatusCode != "error" || s.DurationNs != 5_000_000 || len(s.Events) != 1 || s.Events[0].Attributes["exception.type"] != "*pgconn.PgError" {
		t.Errorf("span = %+v", s)
	}
	if res.Spans[1].Kind != "client" || res.Spans[1].StatusCode != "unset" {
		t.Errorf("enum spellings are normalised: %+v", res.Spans[1])
	}
}

func TestEndpointsRanksAndCaps(t *testing.T) {
	row := func(svc, name string, req, errs, p50, p95, p99 float64) []any {
		return []any{[]any{svc, name}, num64(fmt.Sprint(req)), num64(fmt.Sprint(errs)), num64(fmt.Sprint(p50)), num64(fmt.Sprint(p95)), num64(fmt.Sprint(p99))}
	}
	fr := &fakeRunner{answers: []fakeAnswer{{"otel_traces", Result{Rows: [][]any{
		row("api", "GET /busy", 1000, 10, 5, 20, 40),
		row("api", "GET /slow", 100, 1, 50, 900, 1200),
		row("api", "POST /flaky", 200, 100, 8, 30, 60),
	}}}}}
	l := Local{Env: "dev", Runner: fr}
	n := rng() // one hour
	for sort, want := range map[string]string{"": "GET /busy", "p95": "GET /slow", "errors": "POST /flaky"} {
		res, err := l.Endpoints(context.Background(), EndpointsRequest{Narrow: n, Sort: sort, Top: 2})
		if err != nil {
			t.Fatal(err)
		}
		if res.Endpoints[0].Name != want || len(res.Endpoints) != 2 || res.Omitted != 1 {
			t.Errorf("sort %q: first %s len %d omitted %d, want %s 2 1", sort, res.Endpoints[0].Name, len(res.Endpoints), res.Omitted, want)
		}
	}
	res, _ := l.Endpoints(context.Background(), EndpointsRequest{Narrow: n})
	if e := res.Endpoints[0]; e.ErrorRatio != 0.01 || e.RatePerSecond != 1000.0/3600 {
		t.Errorf("ratio %v rate %v", e.ErrorRatio, e.RatePerSecond)
	}
	if _, err := l.Endpoints(context.Background(), EndpointsRequest{Narrow: n, Sort: "bogus"}); err == nil {
		t.Error("an unknown sort is refused")
	}
	if sql := fr.ran[0].SQL; !strings.Contains(sql, "default.otel_traces") || !strings.Contains(sql, "quantileExactWeightedIf") {
		t.Errorf("sql:\n%s", sql)
	}
}

func TestQuerySQLBindsTheScopeAndRefusesWrites(t *testing.T) {
	fr := &fakeRunner{answers: []fakeAnswer{{"otel_logs", Result{Columns: []Column{{Name: "n", Type: "UInt64"}}, Rows: [][]any{{num64("1")}, {num64("2")}, {num64("3")}}}}}}
	l := Local{Env: "dev", Runner: fr}
	n := rng()
	n.Services = []string{"api", "wo'rker"}
	sql := "SELECT count() AS n FROM otel_logs WHERE Timestamp >= {from:DateTime64(9)} AND has({services:Array(String)}, ServiceName)"
	res, err := l.Query(context.Background(), QueryRequest{Narrow: n, SQL: sql, Limit: 2, Params: map[string]string{"threshold": "500"}})
	if err != nil {
		t.Fatal(err)
	}
	p := fr.ran[0].Params
	if p[dashspec.SQLParamServices] != `['api', 'wo\'rker']` || p["threshold"] != "500" || p[dashspec.SQLParamEnvs] != `['dev']` {
		t.Errorf("params = %v", p)
	}
	if p[dashspec.SQLParamFrom] != "1792663200.000000000" {
		t.Errorf("from = %s", p[dashspec.SQLParamFrom])
	}
	if fr.ran[0].SQL != sql {
		t.Errorf("the statement runs as written")
	}
	if len(res.Rows) != 2 || !res.Truncated {
		t.Errorf("limit 2 of 3 rows: %+v", res)
	}
	if lim := fr.limits[0]; !lim.Break || lim.MaxRows != 3 {
		t.Errorf("raw SQL reads one past the limit and breaks instead of throwing: %+v", lim)
	}

	for _, bad := range []string{"DROP TABLE otel_logs", "SELECT 1; SELECT 2", "INSERT INTO otel_logs SELECT * FROM otel_logs", "SELECT * FROM system.users", "SELECT 1 SETTINGS readonly=0"} {
		ran := len(fr.ran)
		_, err := l.Query(context.Background(), QueryRequest{Narrow: rng(), SQL: bad, Limit: 5})
		be, ok := err.(*Error)
		if !ok || be.ExitCode() != ExitWrong {
			t.Errorf("%q: want exit 1, got %#v", bad, err)
		}
		if len(fr.ran) != ran {
			t.Errorf("%q reached ClickHouse", bad)
		}
	}
	if _, err := l.Query(context.Background(), QueryRequest{Narrow: rng(), SQL: "SELECT 1", Params: map[string]string{"from": "x"}, Limit: 5}); err == nil || !strings.Contains(err.Error(), "scope parameter") {
		t.Errorf("a --param may not shadow a scope parameter: %v", err)
	}
	if _, err := l.Query(context.Background(), QueryRequest{Narrow: rng(), Limit: 5}); err == nil {
		t.Error("neither --sql nor --panel")
	}
}

// An alert rule's query names its env as {reliant_env_id:String}. Locally the
// scope is one environment whose rows carry the empty reliant.env.id, so the
// parameter is bound to it (as {env_ids} is) and the rule runs as written; over
// no environment the statement is refused before ClickHouse sees it, as the
// control plane refuses it; and a --param may not shadow it.
func TestQuerySQLBindsTheEnvIDOfTheOneEnvironment(t *testing.T) {
	const rule = "SELECT count() AS value FROM otel_logs WHERE ResourceAttributes['reliant.env.id'] = {reliant_env_id:String}"
	fr := &fakeRunner{answers: []fakeAnswer{{"otel_logs", Result{Columns: []Column{{Name: "value", Type: "UInt64"}}, Rows: [][]any{{num64("4")}}}}}}
	if _, err := (Local{Env: "dev", Runner: fr}).Query(context.Background(), QueryRequest{Narrow: rng(), SQL: rule, Limit: 5}); err != nil {
		t.Fatal(err)
	}
	p := fr.ran[0].Params
	if v, ok := p[obsquery.EnvIDParam]; !ok || v != "" || p[dashspec.SQLParamEnvIDs] != "['']" {
		t.Errorf("reliant_env_id = %q (bound %v), env_ids = %s; want both the local rows' empty id", v, ok, p[dashspec.SQLParamEnvIDs])
	}
	if fr.ran[0].SQL != rule {
		t.Errorf("the statement runs as written")
	}

	ran := len(fr.ran)
	_, err := (Local{Runner: fr}).Query(context.Background(), QueryRequest{Narrow: rng(), SQL: rule, Limit: 5})
	if be, ok := err.(*Error); !ok || be.ExitCode() != ExitWrong || !strings.Contains(err.Error(), "choose a single environment") {
		t.Errorf("no environment: want exit 1 asking for a single environment, got %#v", err)
	}
	if len(fr.ran) != ran {
		t.Error("a statement with an unbound reliant_env_id reached ClickHouse")
	}

	_, err = (Local{Env: "dev", Runner: fr}).Query(context.Background(), QueryRequest{Narrow: rng(), SQL: rule, Params: map[string]string{obsquery.EnvIDParam: "x"}, Limit: 5})
	if err == nil || !strings.Contains(err.Error(), "scope parameter") {
		t.Errorf("a --param may not shadow reliant_env_id: %v", err)
	}
}

func TestQueryPanelCompilesAndFlattens(t *testing.T) {
	p := obsquery.Panel{ID: "e", Kind: "table", Signal: "traces", GroupBy: []string{"service.name"},
		Series: []obsquery.PanelSeries{{Label: "requests", Agg: "count"}, {Label: "p95", Agg: "quantile", Quantile: 0.95, Field: "span.duration"}}}
	fr := &fakeRunner{answers: []fakeAnswer{{"otel_traces", Result{Rows: [][]any{{[]any{"api"}, num64("10"), num64("4.5")}}}}}}
	res, err := (Local{Env: "dev", Runner: fr}).Query(context.Background(), QueryRequest{Narrow: rng(), Panel: &p, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, c := range res.Columns {
		names = append(names, c.Name)
	}
	if strings.Join(names, ",") != "service.name,requests,p95" {
		t.Errorf("columns = %v", names)
	}
	if len(res.Rows) != 1 || res.Rows[0][0] != "api" || AsFloat(res.Rows[0][1]) != 10 {
		t.Errorf("rows = %v", res.Rows)
	}

	for name, bad := range map[string]obsquery.Panel{
		"issues":  {Kind: "issues"},
		"invalid": {Kind: "table", Signal: "traces"},
	} {
		bad := bad
		if _, err := (Local{Env: "dev", Runner: fr}).Query(context.Background(), QueryRequest{Narrow: rng(), Panel: &bad, Limit: 5}); err == nil || err.(*Error).ExitCode() != ExitWrong {
			t.Errorf("%s: want exit 1, got %v", name, err)
		}
	}
	sqlPanel := obsquery.Panel{Kind: "sql", SQL: "SELECT 1 AS one"}
	fr2 := &fakeRunner{answers: []fakeAnswer{{"SELECT 1", Result{Columns: []Column{{Name: "one"}}, Rows: [][]any{{num64("1")}}}}}}
	if r, err := (Local{Env: "dev", Runner: fr2}).Query(context.Background(), QueryRequest{Narrow: rng(), Panel: &sqlPanel, Limit: 5}); err != nil || len(r.Rows) != 1 {
		t.Errorf("a sql panel runs as raw SQL: %v %v", r, err)
	}
}

func TestStatementPerPurposeIsRunnableAsQuerySQL(t *testing.T) {
	n := rng()
	n.Explain = true
	res, err := (Local{Env: "dev", Runner: &fakeRunner{}}).Endpoints(context.Background(), EndpointsRequest{Narrow: n})
	if err != nil {
		t.Fatal(err)
	}
	st := res.Statements[0]
	if st.SQL == "" || len(st.Params) == 0 {
		t.Fatalf("the starting point carries its parameters: %+v", st)
	}
	for k := range st.Params {
		if !strings.Contains(st.SQL, "{"+k+":") {
			t.Errorf("parameter %s is bound but not used", k)
		}
	}
}
