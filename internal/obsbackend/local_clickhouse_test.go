package obsbackend_test

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/obsbackend/obstest"
	"github.com/reliant-labs/forge/pkg/obsquery"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// TestLocalBackendAgainstClickHouse runs every verb through the real
// clickhouse-client invocation against a real ClickHouse holding the
// exporter's tables.
func TestLocalBackendAgainstClickHouse(t *testing.T) {
	testkit.Slow(t, "boots a ClickHouse container and runs every obs verb against it")
	runner, write := obstest.Start(t)
	now := time.Now().UTC().Truncate(time.Second)
	obstest.Seed(t, write, now)
	l := obsbackend.Local{Env: "dev", Runner: runner}
	ctx := context.Background()
	n := obsbackend.Narrow{Range: obsbackend.Range{From: now.Add(-time.Hour), To: now.Add(time.Minute)}}
	const tid = "0af7651916cd43dd8448eb211c80319c"

	t.Run("logs: scoped to the env, severity, trace, query language", func(t *testing.T) {
		res, err := l.Logs(ctx, obsbackend.LogsRequest{Narrow: n, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Logs) != 3 {
			t.Fatalf("dev has 3 logs (prod's must be invisible), got %d: %+v", len(res.Logs), res.Logs)
		}
		if res.Logs[0].Body != "retrying job" || res.Logs[0].Time.After(now) {
			t.Errorf("newest first: %+v", res.Logs[0])
		}
		if res.Logs[2].Attributes["http.route"] != "/v1/jobs" || res.Logs[2].TraceID != tid {
			t.Errorf("attributes and trace id: %+v", res.Logs[2])
		}
		errs, _ := l.Logs(ctx, obsbackend.LogsRequest{Narrow: n, Severities: []string{"error"}, Limit: 10})
		if len(errs.Logs) != 1 || errs.Logs[0].Severity != "ERROR" {
			t.Errorf("--severity error: %+v", errs.Logs)
		}
		tr, _ := l.Logs(ctx, obsbackend.LogsRequest{Narrow: n, TraceID: tid, Query: "body:request OR body:query", Limit: 10})
		if len(tr.Logs) != 2 {
			t.Errorf("--trace with an OR query: %d logs", len(tr.Logs))
		}
		q, _ := l.Logs(ctx, obsbackend.LogsRequest{Narrow: n, Query: `service:worker "retrying"`, Limit: 10})
		if len(q.Logs) != 1 {
			t.Errorf("query language: %+v", q.Logs)
		}
		old, _ := l.Logs(ctx, obsbackend.LogsRequest{Narrow: n, Oldest: true, Limit: 10})
		if old.Logs[0].Body != "query failed: relation jobs does not exist" {
			t.Errorf("--oldest: %+v", old.Logs[0])
		}
	})

	t.Run("logs: keyset paging walks every row exactly once", func(t *testing.T) {
		var seen []string
		tok := ""
		for page := 0; page < 5; page++ {
			res, err := l.Logs(ctx, obsbackend.LogsRequest{Narrow: n, Limit: 2, PageToken: tok})
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range res.Logs {
				seen = append(seen, r.Body)
			}
			if tok = res.NextPageToken; tok == "" {
				break
			}
		}
		if len(seen) != 3 {
			t.Errorf("paged %d rows: %v", len(seen), seen)
		}
	})

	t.Run("traces: summary, errors, slow, route, env isolation", func(t *testing.T) {
		res, err := l.Traces(ctx, obsbackend.TracesRequest{Narrow: n, Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Traces) != 22 {
			t.Fatalf("dev has 22 traces (the prod one is invisible), got %d", len(res.Traces))
		}
		if s := res.Summary; s == nil || s.Traces != 22 || s.ErrorTraces != 1 || s.P99Ms < 90 {
			t.Errorf("summary = %+v", s)
		}
		errs, _ := l.Traces(ctx, obsbackend.TracesRequest{Narrow: n, ErrorsOnly: true, Limit: 10})
		if len(errs.Traces) != 1 || errs.Traces[0].TraceID != tid || errs.Traces[0].Spans != 14 || errs.Traces[0].Errors != 2 {
			t.Errorf("--errors: %+v", errs.Traces)
		}
		slow, _ := l.Traces(ctx, obsbackend.TracesRequest{Narrow: n, Slow: true, Limit: 2})
		if len(slow.Traces) != 2 || slow.Traces[0].TraceID != "bb" || slow.Traces[0].DurationMs < 900 || slow.SortedFrom != 22 {
			t.Errorf("--slow: %+v from %d", slow.Traces, slow.SortedFrom)
		}
		min, _ := l.Traces(ctx, obsbackend.TracesRequest{Narrow: n, MinDurationMs: 500, Limit: 10})
		if len(min.Traces) != 1 {
			t.Errorf("--min-duration 500ms: %d", len(min.Traces))
		}
		route, _ := l.Traces(ctx, obsbackend.TracesRequest{Narrow: n, Route: "healthz", Limit: 50})
		if len(route.Traces) != 20 {
			t.Errorf("--route healthz: %d", len(route.Traces))
		}
		page, _ := l.Traces(ctx, obsbackend.TracesRequest{Narrow: n, Limit: 5})
		if len(page.Traces) != 5 || page.NextPageToken == "" {
			t.Errorf("paging: %d token %q", len(page.Traces), page.NextPageToken)
		}
	})

	t.Run("trace: spans, exception event, correlated logs, tree", func(t *testing.T) {
		res, err := l.Trace(ctx, obsbackend.TraceRequest{Narrow: n, TraceID: strings.ToUpper(tid), LogLimit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Spans) != 14 || len(res.Logs) != 2 {
			t.Fatalf("spans %d logs %d", len(res.Spans), len(res.Logs))
		}
		errs := obsbackend.ErrorSpans(res.Spans)
		if len(errs) != 2 || exceptionType(errs[0]) != "*pgconn.PgError" {
			t.Errorf("error spans: %+v", errs)
		}
		tree := obsbackend.BuildTree(res.Spans, false)
		root := tree.Roots[0]
		var run *obsbackend.TreeNode
		for _, c := range root.Children {
			if c.Count == 12 {
				run = c
			}
		}
		if run == nil || run.Span.Name != "SELECT crews" || run.P50 != 2*time.Millisecond {
			var got []string
			for _, c := range root.Children {
				got = append(got, fmt.Sprintf("%s ×%d", c.Span.Name, c.Count))
			}
			t.Errorf("the 12 identical selects collapse into one ×12 node; root children = %v", got)
		}
		if _, err := l.Trace(ctx, obsbackend.TraceRequest{Narrow: n, TraceID: "deadbeef"}); err == nil || err.(*obsbackend.Error).ExitCode() != obsbackend.ExitWrong {
			t.Errorf("unknown trace: %v", err)
		}
	})

	t.Run("endpoints: RED per server span, sorted", func(t *testing.T) {
		res, err := l.Endpoints(ctx, obsbackend.EndpointsRequest{Narrow: n, Sort: "p95", Top: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Endpoints) != 3 || res.Endpoints[0].Name != "POST /v1/jobs" {
			t.Fatalf("slowest p95 first: %+v", res.Endpoints)
		}
		var health obsbackend.Endpoint
		for _, e := range res.Endpoints {
			if e.Name == "GET /healthz" {
				health = e
			}
		}
		if health.Requests != 20 || health.Errors != 0 {
			t.Errorf("healthz = %+v", health)
		}
		byErr, _ := l.Endpoints(ctx, obsbackend.EndpointsRequest{Narrow: n, Sort: "errors"})
		if byErr.Endpoints[0].Name != "GET /v1/jobs" || byErr.Endpoints[0].ErrorRatio != 1 {
			t.Errorf("sort errors: %+v", byErr.Endpoints[0])
		}
	})

	t.Run("metrics: catalog, a counter by attribute over the evaluator, a gauge, a refused percentile", func(t *testing.T) {
		list, err := l.MetricsList(ctx, obsbackend.MetricsListRequest{Narrow: n, Limit: 10})
		if err != nil || len(list.Metrics) != 2 {
			t.Fatalf("%+v %v", list, err)
		}
		filtered, _ := l.MetricsList(ctx, obsbackend.MetricsListRequest{Narrow: n, NameContains: "RPC", Limit: 10})
		if len(filtered.Metrics) != 1 || filtered.Metrics[0].Type != "sum" || filtered.Metrics[0].Unit != "{call}" || !filtered.Metrics[0].Monotonic || !filtered.Metrics[0].Seen {
			t.Errorf("--contains is case-insensitive: %+v", filtered.Metrics)
		}
		calls := obsquery.Panel{ID: "p", Kind: "line", Signal: "metrics", GroupBy: []string{"rpc.method"},
			Series: []obsquery.PanelSeries{{Label: "A", Field: "rpc.server.calls", Agg: "increase", MetricType: "sum"}}}
		q, err := l.MetricsQuery(ctx, obsbackend.MetricsQueryRequest{Narrow: n, Panel: calls, StepSeconds: 60})
		if err != nil {
			t.Fatal(err)
		}
		if len(q.Meta) != 1 || q.Meta[0].Type != "sum" || q.Meta[0].Agg != "increase" || len(q.Series) != 2 {
			t.Fatalf("res = %+v", q)
		}
		for _, s := range q.Series {
			if s.Labels["rpc.method"] == "" || len(s.Points) == 0 {
				t.Errorf("series = %+v", s)
			}
		}
		heap := obsquery.Panel{ID: "p", Kind: "line", Signal: "metrics", Series: []obsquery.PanelSeries{{Label: "A", Field: "heap", Agg: "avg", MetricType: "gauge"}}}
		g, err := l.MetricsQuery(ctx, obsbackend.MetricsQueryRequest{Narrow: n, Panel: heap})
		if err != nil || g.Meta[0].Type != "gauge" || g.Meta[0].Agg != "avg" || len(g.Series) != 1 {
			t.Errorf("%+v %v", g, err)
		}
		heap.Series[0].Agg = "quantile"
		heap.Series[0].Quantile = 0.95
		if _, err := l.MetricsQuery(ctx, obsbackend.MetricsQueryRequest{Narrow: n, Panel: heap}); err == nil {
			t.Error("a percentile of a gauge")
		}
		d, err := l.MetricsDescribe(ctx, obsbackend.MetricsDescribeRequest{Narrow: n, Name: "rpc.server.calls"})
		if err != nil || d.Info.Type != "sum" || len(d.Attributes) == 0 {
			t.Errorf("describe: %+v %v", d, err)
		}
	})

	t.Run("query: scope parameters are bound; writes are refused by the guard and by readonly", func(t *testing.T) {
		sql := `SELECT ServiceName, count() AS n FROM otel_logs
WHERE Timestamp >= {from:DateTime64(9)} AND Timestamp < {to:DateTime64(9)}
  AND (empty({services:Array(String)}) OR has({services:Array(String)}, ServiceName))
  AND ResourceAttributes['deployment.environment.name'] IN {envs:Array(String)}
GROUP BY ServiceName ORDER BY n DESC, ServiceName`
		res, err := l.Query(ctx, obsbackend.QueryRequest{Narrow: n, SQL: sql, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Rows) != 2 || obsbackend.AsString(res.Rows[0][0]) != "api" || obsbackend.AsInt(res.Rows[0][1]) != 2 {
			t.Errorf("rows = %v", res.Rows)
		}
		narrowed := n
		narrowed.Services = []string{"worker"}
		res, err = l.Query(ctx, obsbackend.QueryRequest{Narrow: narrowed, SQL: sql, Limit: 10})
		if err != nil || len(res.Rows) != 1 || obsbackend.AsString(res.Rows[0][0]) != "worker" {
			t.Errorf("--service binds {services}: %v %v", res.Rows, err)
		}
		cut, _ := l.Query(ctx, obsbackend.QueryRequest{Narrow: n, SQL: "SELECT number FROM numbers(50)", Limit: 5})
		if len(cut.Rows) != 5 || !cut.Truncated {
			t.Errorf("a long result is cut at --limit, flagged truncated: %d %v", len(cut.Rows), cut.Truncated)
		}
		user, err := l.Query(ctx, obsbackend.QueryRequest{Narrow: n, SQL: "SELECT {threshold:UInt32} AS t", Params: map[string]string{"threshold": "500"}, Limit: 5})
		if err != nil || obsbackend.AsInt(user.Rows[0][0]) != 500 {
			t.Errorf("--param: %v %v", user.Rows, err)
		}
		if _, err := l.Query(ctx, obsbackend.QueryRequest{Narrow: n, SQL: "DROP TABLE otel_logs", Limit: 5}); err == nil || err.(*obsbackend.Error).ExitCode() != obsbackend.ExitWrong {
			t.Errorf("the guard: %v", err)
		}
		// With the guard bypassed, the read-only setting still refuses a write.
		_, err = runner.Run(ctx, obsquery.Stmt{SQL: "INSERT INTO otel_logs (Body) VALUES ('x')"}, obsbackend.Limits{Timeout: 10 * time.Second})
		if err == nil || err.(*obsbackend.Error).ExitCode() != obsbackend.ExitWrong || !strings.Contains(err.Error(), "readonly") {
			t.Errorf("readonly=2 is the second layer: %v", err)
		}
		if _, err := l.Query(ctx, obsbackend.QueryRequest{Narrow: n, SQL: "SELECT * FROM default.nothing", Limit: 5}); err == nil || err.(*obsbackend.Error).ExitCode() != obsbackend.ExitWrong || !strings.Contains(err.Error(), "nothing") {
			t.Errorf("ClickHouse's own message reaches the caller: %v", err)
		}
	})

	t.Run("query: a panel runs through the compiler", func(t *testing.T) {
		p := obsquery.Panel{ID: "e", Kind: "table", Signal: "traces", GroupBy: []string{"span.name"},
			Where:  []obsquery.PanelPredicate{{Field: "span.kind", Op: "eq", Value: "server"}},
			Series: []obsquery.PanelSeries{{Label: "requests", Agg: "count"}}}
		res, err := l.Query(ctx, obsbackend.QueryRequest{Narrow: n, Panel: &p, Limit: 10})
		if err != nil || len(res.Rows) != 3 {
			t.Fatalf("%v %v", res.Rows, err)
		}
		if res.Columns[0].Name != "span.name" || obsbackend.AsString(res.Rows[0][0]) != "GET /healthz" || obsbackend.AsInt(res.Rows[0][1]) != 20 {
			t.Errorf("rows = %v columns = %v", res.Rows, res.Columns)
		}
	})
}

func exceptionType(s obsbackend.Span) string {
	for _, e := range s.Events {
		if e.Name == "exception" {
			return e.Attributes["exception.type"]
		}
	}
	return ""
}

// A stopped ClickStack is "could not look" with the remedy, not a crash.
func TestDockerRunnerAgainstAStoppedContainer(t *testing.T) {
	testkit.Slow(t, "execs docker")
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker unavailable")
	}
	r := obsbackend.DockerRunner{Env: "dev", Exec: func(ctx context.Context, script string, args []string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "docker", append([]string{"exec", "forge-obsbackend-no-such-container", "sh", "-c", script, "sh"}, args...)...)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return out, fmt.Errorf("%w: %s", err, stderr.String())
		}
		return out, nil
	}}
	_, err := r.Run(context.Background(), obsquery.Stmt{SQL: "SELECT 1"}, obsbackend.DefaultLimits())
	be, ok := err.(*obsbackend.Error)
	if !ok || be.ExitCode() != obsbackend.ExitUndetermined {
		t.Fatalf("err = %#v", err)
	}
	t.Logf("stopped container message: %s", be.Error())
}
