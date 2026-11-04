package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/obsbackend/obstest"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

// mxAt is the n-th minute of the default range (the last hour before obsNow).
func mxAt(n int) time.Time { return obsNow.Add(-time.Hour).Add(time.Duration(n) * time.Minute) }

func mxPts(label string, vals ...float64) []obstest.FakePoint {
	var out []obstest.FakePoint
	for i, v := range vals {
		p := obstest.FakePoint{Bucket: mxAt(i), Value: v}
		if label != "" {
			p.Labels = []string{label}
		}
		out = append(out, p)
	}
	return out
}

func mxFixture() *obstest.FakeMetrics {
	calls := append(mxPts("List", 2, 2, 2), mxPts("Get", 1, 1, 1)...)
	return &obstest.FakeMetrics{
		StoredTraces: []string{"0af7651916cd43dd8448eb211c80319c"},
		Metrics: map[string]obstest.FakeMetric{
			"rpc.server.calls": {Kind: obsquery.CatalogSum, Unit: "{call}", Monotonic: true, Temporality: 2, Services: []string{"api"}, Series: 2,
				Attrs:        []obstest.FakeAttr{{Key: "rpc.method", Distinct: 2}, {Key: "status", Distinct: 3}, {Key: "k8s.pod.name", Resource: true, Distinct: 400}},
				DeclaredKeys: []string{"rpc.method", "status"}, Points: calls, LastSeen: mxAt(59),
				Reduced: []obstest.FakePoint{{Bucket: mxAt(0), Labels: []string{"List"}, Value: 2}, {Bucket: mxAt(0), Labels: []string{"Get"}, Value: 1}}},
			"rpc.server.errors": {Kind: obsquery.CatalogSum, Unit: "{call}", Monotonic: true, Temporality: 2, Services: []string{"api"}, Series: 1,
				DeclaredKeys: []string{"rpc.method"}, Points: mxPts("List", 1, 0, 1), LastSeen: mxAt(59)},
			"rpc.server.duration": {Kind: obsquery.CatalogHistogram, Unit: "ms", Temporality: 2, Services: []string{"api"}, Series: 1, LastSeen: mxAt(59),
				Attrs:     []obstest.FakeAttr{{Key: "rpc.method", Distinct: 2}},
				Exemplars: []obstest.FakeExemplar{{Time: mxAt(30), Value: 812.5, TraceID: "0af7651916cd43dd8448eb211c80319c", SpanID: "b7ad6b7169203331", Attrs: map[string]string{"http.route": "/v1/jobs"}}, {Time: mxAt(31), Value: 700, TraceID: strings.Repeat("b", 32), SpanID: "c1"}}},
			"heap": {Kind: obsquery.CatalogGauge, Unit: "By", Services: []string{"api", "worker"}, Series: 1, Points: mxPts("", 10, 20, 30), LastSeen: mxAt(59),
				Reduced: []obstest.FakePoint{{Bucket: mxAt(0), Value: 30}}},
		},
	}
}

// mxRun runs `forge obs <args>` in-process with deps over the real root command.
func mxRun(t *testing.T, deps obsDeps, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errb bytes.Buffer
	old := obsDepsOverride
	t.Cleanup(func() { obsDepsOverride = old })
	d := deps
	if d.Now == nil {
		d.Now = func() time.Time { return obsNow }
	}
	if d.Release == nil {
		d.Release = func(context.Context, string, time.Time) (*liveRelease, error) { return nil, nil }
	}
	obsDepsOverride = &d
	cmd := newTestRootCmd(t)
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(append([]string{"obs"}, args...))
	err = cmd.ExecuteContext(context.Background())
	return out.String(), errb.String(), err
}

func mxLocal(f *obstest.FakeMetrics) obsDeps {
	return obsDeps{
		Facts: func(context.Context, io.Writer, string) (obsbackend.Facts, error) { return devFacts(), nil },
		Local: func(env string) obsbackend.Backend { return obsbackend.Local{Env: env, Runner: f} },
	}
}

type mxCaller struct {
	replies map[string]string
	errs    map[string]error
	calls   []fakeCloudCall
}

func (c *mxCaller) Call(_ context.Context, procedure string, req, out any) error {
	b, _ := json.Marshal(req)
	c.calls = append(c.calls, fakeCloudCall{procedure, string(b)})
	if err := c.errs[procedure]; err != nil {
		return err
	}
	reply, ok := c.replies[procedure]
	if !ok {
		return fmt.Errorf("unexpected call %s", procedure)
	}
	return json.Unmarshal([]byte(reply), out)
}

func mxCloud(c obsbackend.Caller) obsDeps {
	return obsDeps{
		Facts: func(context.Context, io.Writer, string) (obsbackend.Facts, error) { return cloudFacts(), nil },
		Cloud: func(_ context.Context, env string) (obsbackend.Cloud, error) {
			return obsbackend.Cloud{Env: env, EnvID: "env-prod", OrgID: "org-1", Caller: c}, nil
		},
	}
}

const (
	cpQueryPanel  = "controlplane.v1.ObservabilityService/QueryPanel"
	cpListMetrics = "controlplane.v1.ObservabilityService/ListMetrics"
	cpGetMetric   = "controlplane.v1.ObservabilityService/GetMetric"
	cpExemplars   = "controlplane.v1.ObservabilityService/ListMetricExemplars"
)

// mxCPQueryPanel is the reply the control plane gives for panel p over f's
// data: its handler is obsquery.EvalMetricPanel over its own ClickHouse.
func mxCPQueryPanel(t *testing.T, f *obstest.FakeMetrics, p obsquery.Panel) string {
	t.Helper()
	runner := func(ctx context.Context, st obsquery.Stmt) (obsquery.Rows, error) {
		r, err := f.Run(ctx, st, obsbackend.Limits{})
		if err != nil {
			return nil, err
		}
		return obsbackend.RowsOf(r), nil
	}
	sc := obsquery.Scope{Database: obsquery.LocalDatabase, Local: true, Start: obsNow.Add(-time.Hour), End: obsNow}
	res, err := obsquery.EvalMetricPanel(context.Background(), runner, sc, p, obsquery.EvalOptions{Now: obsNow})
	if err != nil {
		t.Fatal(err)
	}
	return obstest.CPPanelResponse(p.Kind, res)
}

const mxCPCatalog = `{"metrics":[
 {"name":"rpc.server.calls","type":"METRIC_TYPE_SUM","unit":"{call}","monotonic":true,"temporality":"METRIC_TEMPORALITY_CUMULATIVE","services":["api"],"serviceCount":1,"lastSeen":"2026-10-22T11:59:00Z","seriesEstimate":"2","queryable":true,"seen":true},
 {"name":"rpc.server.errors","type":"METRIC_TYPE_SUM","unit":"{call}","monotonic":true,"temporality":"METRIC_TEMPORALITY_CUMULATIVE","services":["api"],"serviceCount":1,"lastSeen":"2026-10-22T11:59:00Z","seriesEstimate":"1","queryable":true,"seen":true},
 {"name":"rpc.server.duration","type":"METRIC_TYPE_HISTOGRAM","unit":"ms","temporality":"METRIC_TEMPORALITY_CUMULATIVE","services":["api"],"serviceCount":1,"lastSeen":"2026-10-22T11:59:00Z","seriesEstimate":"1","queryable":true,"seen":true},
 {"name":"heap","type":"METRIC_TYPE_GAUGE","unit":"By","services":["api","worker"],"serviceCount":2,"lastSeen":"2026-10-22T11:59:00Z","seriesEstimate":"1","queryable":true,"seen":true}]}`

// ── the flag grammar ────────────────────────────────────────────────────

// TestMetricsFlagGrammarIsWhatReliantEmits: every command the console's
// "copy as CLI" (reliant explorerState.cliCommand) emits builds the panel it
// was made from.
func TestMetricsFlagGrammarIsWhatReliantEmits(t *testing.T) {
	a := func(field, agg string, q float64, where ...obsquery.PanelPredicate) obsquery.PanelSeries {
		return obsquery.PanelSeries{Label: "A", Field: field, Agg: agg, Quantile: q, Where: where}
	}
	for name, c := range map[string]struct {
		metric  string
		flags   metricsPanelFlags
		filters []string
		want    obsquery.Panel
	}{
		"a one-series quantile": {
			metric: "rpc.server.duration", flags: metricsPanelFlags{agg: "quantile", quantile: 0.95},
			want: obsquery.Panel{ID: "explore", Kind: "line", Signal: "metrics", Series: []obsquery.PanelSeries{a("rpc.server.duration", "quantile", 0.95)}},
		},
		"a counter with a filter and a group-by": {
			metric: "rpc.server.calls", flags: metricsPanelFlags{agg: "rate", groupBy: []string{"service.name"}}, filters: []string{"service.name!=web"},
			want: obsquery.Panel{ID: "explore", Kind: "line", Signal: "metrics", GroupBy: []string{"service.name"},
				Where:  []obsquery.PanelPredicate{{Field: "service.name", Op: "neq", Value: "web"}},
				Series: []obsquery.PanelSeries{a("rpc.server.calls", "rate", 0)}},
		},
		"two queries, a per-series where, a formula, a top and a kind": {
			metric: "rpc.server.duration",
			flags: metricsPanelFlags{agg: "quantile", quantile: 0.95, queries: []string{"B=rpc.server.calls:rate"}, where: []string{"B:metric.attr.status=error"},
				groupBy: []string{"rpc.method"}, formula: "B / A * 100", kind: "table", top: 10},
			want: obsquery.Panel{ID: "explore", Kind: "table", Signal: "metrics", GroupBy: []string{"rpc.method"}, Formula: "B / A * 100", Limit: 10,
				Series: []obsquery.PanelSeries{a("rpc.server.duration", "quantile", 0.95),
					{Label: "B", Field: "rpc.server.calls", Agg: "rate", Where: []obsquery.PanelPredicate{{Field: "metric.attr.status", Op: "eq", Value: "error"}}}}},
		},
		"a quantile level inside a --query spec": {
			metric: "m", flags: metricsPanelFlags{agg: "avg", queries: []string{"P=rpc.server.duration:quantile:0.99"}},
			want: obsquery.Panel{ID: "explore", Kind: "line", Signal: "metrics", Series: []obsquery.PanelSeries{a("m", "avg", 0),
				{Label: "P", Field: "rpc.server.duration", Agg: "quantile", Quantile: 0.99}}},
		},
		"p95 is the quantile 0.95; a bare metric takes the kind's default": {
			metric: "m", flags: metricsPanelFlags{agg: "p95", queries: []string{"B=n"}},
			want: obsquery.Panel{ID: "explore", Kind: "line", Signal: "metrics", Series: []obsquery.PanelSeries{a("m", "quantile", 0.95), {Label: "B", Field: "n"}}},
		},
		"a bare key is the metric's attribute; resource keys are read off the resource": {
			metric: "m", flags: metricsPanelFlags{agg: "avg", where: []string{"A:status=error", "A:k8s.pod.name!=x"}}, filters: []string{"resource.attr.k8s.node.name=n1", "service.name=api"},
			want: obsquery.Panel{ID: "explore", Kind: "line", Signal: "metrics",
				Where: []obsquery.PanelPredicate{{Field: "resource.attr.k8s.node.name", Op: "eq", Value: "n1"}, {Field: "service.name", Op: "eq", Value: "api"}},
				Series: []obsquery.PanelSeries{a("m", "avg", 0,
					obsquery.PanelPredicate{Field: "metric.attr.status", Op: "eq", Value: "error"},
					obsquery.PanelPredicate{Field: "resource.attr.k8s.pod.name", Op: "neq", Value: "x"})}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := buildMetricsPanel(c.metric, c.flags, c.filters)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("panel\n got %+v\nwant %+v", got, c.want)
			}
		})
	}
}

func TestMetricsFlagGrammarRefusals(t *testing.T) {
	for name, c := range map[string]struct {
		flags metricsPanelFlags
		want  string
	}{
		"an unknown agg":                {metricsPanelFlags{agg: "median"}, "aggregation \"median\""},
		"quantile with no level":        {metricsPanelFlags{agg: "quantile"}, "needs a level"},
		"a level out of range":          {metricsPanelFlags{agg: "quantile", quantile: 1.5}, "(0, 1]"},
		"a level with another agg":      {metricsPanelFlags{agg: "avg", quantile: 0.5}, "quantile aggregation"},
		"a level with no agg":           {metricsPanelFlags{quantile: 0.5}, "needs the quantile aggregation"},
		"a query with no metric":        {metricsPanelFlags{queries: []string{"B="}}, "NAME=metric"},
		"a query with no name":          {metricsPanelFlags{queries: []string{"rpc.calls"}}, "NAME=metric"},
		"a query reusing A":             {metricsPanelFlags{queries: []string{"A=x"}}, "taken"},
		"a query naming a series twice": {metricsPanelFlags{queries: []string{"B=x", "B=y"}}, "taken"},
		"a bad query agg":               {metricsPanelFlags{queries: []string{"B=x:nope"}}, "aggregation \"nope\""},
		"a where for no series":         {metricsPanelFlags{where: []string{"Z:k=v"}}, "no series named Z"},
		"a where with no predicate":     {metricsPanelFlags{where: []string{"A"}}, "NAME:key=value"},
		"an unknown kind":               {metricsPanelFlags{kind: "pie"}, "line, number, table or bar"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := buildMetricsPanel("m", c.flags, nil)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
			if exitCodeForError(err) != 1 {
				t.Errorf("exit = %d, want 1", exitCodeForError(err))
			}
		})
	}
}

// ── metrics query ───────────────────────────────────────────────────────

func TestMetricsQueryJSONGoldenLocal(t *testing.T) {
	f := mxFixture()
	out, _, err := mxRun(t, mxLocal(f), "metrics", "query", "dev", "rpc.server.calls", "--group-by", "rpc.method", "--json")
	if err != nil {
		t.Fatal(err)
	}
	obsGolden(t, "metrics.query.json", out)
	doc := decode(t, out)
	if doc["schema"] != "forge.obs.metrics_query/v2" || doc["query_mode"] != "structured" {
		t.Errorf("head: %v %v", doc["schema"], doc["query_mode"])
	}
	for _, k := range []string{"meta", "series", "series_total", "warnings", "panel", "step_seconds"} {
		if _, ok := doc[k]; !ok {
			t.Errorf("v2 document lacks %q", k)
		}
	}
	if _, ok := doc["compare_series"]; ok {
		t.Error("compare_series appears only with --compare")
	}
	meta := doc["meta"].([]any)[0].(map[string]any)
	if meta["agg"] != "rate" || meta["type"] != "sum" || meta["value_unit"] != "{call}/s" {
		t.Errorf("the kind's default aggregation is resolved and reported: %v", meta)
	}
}

func TestMetricsQueryJSONGoldenCloud(t *testing.T) {
	f := mxFixture()
	panel := obsquery.Panel{ID: "explore", Kind: "line", Signal: "metrics", GroupBy: []string{"rpc.method"},
		Series: []obsquery.PanelSeries{{Label: "A", Field: "rpc.server.calls", Agg: "rate", MetricType: "sum"}}}
	mc := &mxCaller{replies: map[string]string{cpListMetrics: mxCPCatalog, cpQueryPanel: mxCPQueryPanel(t, f, panel)}}
	out, _, err := mxRun(t, mxCloud(mc), "metrics", "query", "prod", "rpc.server.calls", "--group-by", "rpc.method", "--json")
	if err != nil {
		t.Fatal(err)
	}
	obsGolden(t, "metrics.query.cloud.json", out)
	if sc := decode(t, out)["scope"].(map[string]any); sc["backend"] != "cloud" || sc["env_id"] != "env-prod" {
		t.Errorf("scope %v", sc)
	}
	var sent string
	for _, c := range mc.calls {
		if c.procedure == cpQueryPanel {
			sent = c.body
		}
	}
	for _, want := range []string{`"signal":"metrics"`, `"metricType":"sum"`, `"agg":"rate"`, `"groupBy":["rpc.method"]`, `"orgId":"org-1"`, `"envIds":["env-prod"]`} {
		if !strings.Contains(sent, want) {
			t.Errorf("QueryPanel request lacks %s:\n%s", want, sent)
		}
	}
}

// TestMetricsQueryParityLocalVsCloud: one fixture panel (two metrics, a
// formula, a group-by) through the local backend over the fake ClickHouse and
// through the cloud backend over a caller canned with the control plane
// evaluator's real output for the same data: the series are identical.
func TestMetricsQueryParityLocalVsCloud(t *testing.T) {
	f := mxFixture()
	args := []string{"metrics", "query", "%s", "rpc.server.calls", "--agg", "rate", "--query", "B=rpc.server.errors:rate",
		"--formula", "B / A * 100", "--group-by", "rpc.method", "--json"}
	with := func(env string) []string { return append([]string{args[0], args[1], env}, args[3:]...) }
	panel := obsquery.Panel{ID: "explore", Kind: "line", Signal: "metrics", GroupBy: []string{"rpc.method"}, Formula: "B / A * 100",
		Series: []obsquery.PanelSeries{
			{Label: "A", Field: "rpc.server.calls", Agg: "rate", MetricType: "sum"},
			{Label: "B", Field: "rpc.server.errors", Agg: "rate", MetricType: "sum"},
		}}
	// --agg rate on A alone is explicit, but metricType is the evaluator's to
	// look up: the cloud request carries the kinds the catalog reported.
	lout, _, err := mxRun(t, mxLocal(f), with("dev")...)
	if err != nil {
		t.Fatal(err)
	}
	mc := &mxCaller{replies: map[string]string{cpListMetrics: mxCPCatalog, cpQueryPanel: mxCPQueryPanel(t, f, panel)}}
	cout, _, err := mxRun(t, mxCloud(mc), with("prod")...)
	if err != nil {
		t.Fatal(err)
	}
	l, c := decode(t, lout), decode(t, cout)
	for _, k := range []string{"series", "meta", "series_total", "warnings", "step_seconds", "panel"} {
		if !reflect.DeepEqual(l[k], c[k]) {
			lj, _ := json.MarshalIndent(l[k], "", " ")
			cj, _ := json.MarshalIndent(c[k], "", " ")
			t.Errorf("%s differs between the backends\n--- local\n%s\n--- cloud\n%s", k, lj, cj)
		}
	}
	series := l["series"].([]any)
	if len(series) != 2 || series[0].(map[string]any)["label"] != "formula" {
		t.Fatalf("a formula per group (Get has no errors, a counter's missing point is 0): %v", series)
	}
	var got []float64
	for _, p := range series[0].(map[string]any)["points"].([]any) {
		got = append(got, p.(map[string]any)["value"].(float64))
	}
	if fmt.Sprint(got) != "[50 0 50]" {
		t.Errorf("B / A * 100 = %v", got)
	}
}

func TestMetricsQueryNumberTableAndKinds(t *testing.T) {
	f := mxFixture()
	out, _, err := mxRun(t, mxLocal(f), "metrics", "query", "dev", "heap", "--agg", "avg", "--kind", "number", "--json")
	if err != nil {
		t.Fatal(err)
	}
	s := decode(t, out)["series"].([]any)[0].(map[string]any)
	if s["reduced"] != float64(30) {
		t.Errorf("a number panel reads its range reduction: %v", s)
	}
	out, _, err = mxRun(t, mxLocal(f), "metrics", "query", "dev", "rpc.server.calls", "--kind", "table", "--group-by", "rpc.method", "--top", "5", "--json")
	if err != nil {
		t.Fatal(err)
	}
	doc := decode(t, out)
	if rows := doc["series"].([]any); len(rows) != 2 || rows[0].(map[string]any)["reduced"] != float64(2) {
		t.Errorf("a table over metrics is supported: %v", rows)
	}
	if doc["panel"].(map[string]any)["limit"] != float64(5) {
		t.Errorf("--top is the panel's limit: %v", doc["panel"])
	}
	human, _, err := mxRun(t, mxLocal(f), "metrics", "query", "dev", "rpc.server.calls", "--group-by", "rpc.method")
	if err != nil || !strings.Contains(human, "SERIES") || !strings.Contains(human, "List") || !strings.Contains(human, "rate") {
		t.Errorf("human output (%v):\n%s", err, human)
	}
}

func TestMetricsQueryExplainRunsNothing(t *testing.T) {
	f := mxFixture()
	out, _, err := mxRun(t, mxLocal(f), "metrics", "query", "dev", "rpc.server.calls", "--query", "B=rpc.server.errors", "--formula", "B / A", "--group-by", "rpc.method", "--compare", "24h", "--explain")
	if err != nil {
		t.Fatal(err)
	}
	if n := len(f.Ran()); n != 0 {
		t.Fatalf("--explain ran %d statements", n)
	}
	for _, want := range []string{"-- lookup of rpc.server.calls", "-- lookup of rpc.server.errors", "-- series A", "-- series B", "(compare)", "--param_"} {
		if !strings.Contains(out, want) {
			t.Errorf("explain output lacks %q:\n%s", want, out)
		}
	}
	mc := &mxCaller{}
	out2, _, err := mxRun(t, mxCloud(mc), "metrics", "query", "prod", "rpc.server.calls", "--agg", "rate", "--explain", "--json")
	if err != nil || len(mc.calls) != 0 {
		t.Fatalf("a cloud --explain calls nothing: %v %v", err, mc.calls)
	}
	var compiled bool
	for _, s := range decode(t, out2)["statements"].([]any) {
		compiled = compiled || strings.Contains(s.(map[string]any)["purpose"].(string), "compiled locally")
	}
	if !compiled {
		t.Errorf("statements %v", decode(t, out2)["statements"])
	}
}

func TestMetricsQueryCompare(t *testing.T) {
	f := mxFixture()
	out, _, err := mxRun(t, mxLocal(f), "metrics", "query", "dev", "heap", "--agg", "avg", "--compare", "24h", "--json")
	if err != nil {
		t.Fatal(err)
	}
	doc := decode(t, out)
	cmp := doc["compare"].(map[string]any)
	if cmp["offset_seconds"] != float64(86400) || cmp["against"] != "24h0m0s" || cmp["from"] != "2026-10-21T11:00:00Z" || cmp["to"] != "2026-10-21T12:00:00Z" {
		t.Errorf("compare = %v", cmp)
	}
	if cs := doc["compare_series"].([]any); len(cs) != 1 || len(cs[0].(map[string]any)["points"].([]any)) == 0 {
		t.Errorf("compare_series = %v", cs)
	}
	if n := len(f.Ran()); n < 2 {
		t.Errorf("the panel ran once over each range, %d statements ran", n)
	}

	// prev-release: the window that ended when the live release was promoted.
	deps := mxLocal(f)
	deps.Release = func(_ context.Context, _ string, at time.Time) (*liveRelease, error) {
		return &liveRelease{Version: "v1.4.2", PromotedAt: obsNow.Add(-30 * time.Minute)}, nil
	}
	out, _, err = mxRun(t, deps, "metrics", "query", "dev", "heap", "--agg", "avg", "--compare", "prev-release", "--json")
	if err != nil {
		t.Fatal(err)
	}
	cmp = decode(t, out)["compare"].(map[string]any)
	if cmp["offset_seconds"] != float64(1800) || cmp["against"] != "prev-release" || cmp["to"] != "2026-10-22T11:30:00Z" {
		t.Errorf("prev-release = %v", cmp)
	}
	// ...and with nothing promoted there is no before.
	out, _, err = mxRun(t, mxLocal(f), "metrics", "query", "dev", "heap", "--agg", "avg", "--compare", "prev-release", "--json")
	if exitCodeForError(err) != 1 || !strings.Contains(decode(t, out)["error"].(string), "no release was promoted") {
		t.Errorf("exit %d, %s", exitCodeForError(err), out)
	}
	_, _, err = mxRun(t, mxLocal(f), "metrics", "query", "dev", "heap", "--agg", "avg", "--compare", "soon")
	if exitCodeForError(err) != 1 || !strings.Contains(err.Error(), "--compare") {
		t.Errorf("a bad --compare: %v", err)
	}
}

// TestMetricsPanelOutRoundTrip: flags -> panel -> `forge obs query --panel`
// gives the same series as the flags did.
func TestMetricsPanelOutRoundTrip(t *testing.T) {
	f := mxFixture()
	flags := []string{"rpc.server.calls", "--query", "B=rpc.server.errors:rate", "--formula", "B / A * 100", "--group-by", "rpc.method"}
	path := filepath.Join(t.TempDir(), "panel.json")
	out, _, err := mxRun(t, mxLocal(f), append(append([]string{"metrics", "query", "dev"}, flags...), "--panel-out", path)...)
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Errorf("--panel-out FILE prints nothing: %q", out)
	}
	for _, st := range f.Ran() {
		if strings.Contains(st.SQL, "toStartOfInterval") {
			t.Errorf("--panel-out ran the panel: %s", st.SQL)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var panel map[string]any
	if err := json.Unmarshal(raw, &panel); err != nil {
		t.Fatal(err)
	}
	series := panel["series"].([]any)
	if panel["signal"] != "metrics" || panel["formula"] != "B / A * 100" || len(series) != 2 {
		t.Fatalf("panel %s", raw)
	}
	a := series[0].(map[string]any)
	if a["label"] != "A" || a["field"] != "rpc.server.calls" || a["metricType"] != "sum" || a["agg"] != "rate" {
		t.Errorf("the kind and default aggregation are written in: %v", a)
	}

	viaFlags, _, err := mxRun(t, mxLocal(f), append(append([]string{"metrics", "query", "dev"}, flags...), "--json")...)
	if err != nil {
		t.Fatal(err)
	}
	viaPanel, _, err := mxRun(t, mxLocal(f), "query", "dev", "--panel", path, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var want []any
	for _, s := range decode(t, viaFlags)["series"].([]any) {
		for _, p := range s.(map[string]any)["points"].([]any) {
			want = append(want, p.(map[string]any)["value"])
		}
	}
	rows := decode(t, viaPanel)["rows"].([]any)
	if len(rows) != len(want) || len(rows) == 0 {
		t.Fatalf("rows %d, points %d", len(rows), len(want))
	}
	for i, r := range rows {
		row := r.([]any)
		if got := row[len(row)-1]; got != want[i] {
			t.Errorf("point %d: the panel says %v, the flags say %v", i, got, want[i])
		}
	}
	// - is stdout, and a panel that is invalid is refused before it is written.
	out, _, err = mxRun(t, mxLocal(f), "metrics", "query", "dev", "heap", "--agg", "avg", "--panel-out", "-")
	if err != nil || !strings.Contains(out, `"signal": "metrics"`) {
		t.Errorf("--panel-out -: %v\n%s", err, out)
	}
	_, _, err = mxRun(t, mxLocal(f), "metrics", "query", "dev", "heap", "--agg", "avg", "--kind", "bar", "--panel-out", "-")
	if exitCodeForError(err) != 1 || !strings.Contains(err.Error(), "group_by") {
		t.Errorf("a bar with no group-by: %v", err)
	}
}

func TestMetricsQueryQueryPanelRunsMetricsPanelsThroughBothBackends(t *testing.T) {
	f := mxFixture()
	panelJSON := `{"id":"p","title":"P","kind":"line","signal":"metrics","groupBy":["rpc.method"],"series":[{"label":"A","agg":"rate","field":"rpc.server.calls","metricType":"sum"}]}`
	path := filepath.Join(t.TempDir(), "p.json")
	if err := os.WriteFile(path, []byte(panelJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	lout, _, err := mxRun(t, mxLocal(f), "query", "dev", "--panel", path, "--json")
	if err != nil {
		t.Fatalf("the local backend refuses no metrics panel now: %v", err)
	}
	p, _ := readPanelFile(path)
	mc := &mxCaller{replies: map[string]string{cpQueryPanel: mxCPQueryPanel(t, f, p)}}
	cout, _, err := mxRun(t, mxCloud(mc), "query", "prod", "--panel", path, "--json")
	if err != nil {
		t.Fatal(err)
	}
	if l, c := decode(t, lout)["rows"], decode(t, cout)["rows"]; !reflect.DeepEqual(l, c) || len(l.([]any)) != 6 {
		t.Errorf("obs query --panel differs between the backends\nlocal %v\ncloud %v", l, c)
	}
}

func TestMetricsQueryCloudErrors(t *testing.T) {
	args := []string{"metrics", "query", "prod", "rpc.server.calls", "--agg", "rate", "--json"}
	for name, c := range map[string]struct {
		err      error
		exit     int
		contains string
	}{
		"an older control plane":  {&cloud.Error{Code: cloud.CodeUnimplemented}, 2, "this control plane does not serve QueryPanel yet"},
		"a refused request":       {&cloud.Error{Code: cloud.CodeInvalidArgument, Message: "unknown metric"}, 1, ""},
		"no credential":           {&cloud.Error{Code: cloud.CodeUnauthenticated}, 2, ""},
		"a control plane outage":  {&cloud.Error{Code: cloud.CodeUnavailable}, 2, ""},
		"a transport failure":     {io.ErrUnexpectedEOF, 2, "could not be reached"},
		"a rate limit":            {&cloud.Error{Code: cloud.CodeResourceExhausted}, 2, ""},
		"the permission is wrong": {&cloud.Error{Code: cloud.CodePermissionDenied}, 2, ""},
	} {
		t.Run(name, func(t *testing.T) {
			out, _, err := mxRun(t, mxCloud(&mxCaller{errs: map[string]error{cpQueryPanel: c.err}}), args...)
			if exitCodeForError(err) != c.exit {
				t.Fatalf("exit = %d, want %d: %v", exitCodeForError(err), c.exit, err)
			}
			doc := decode(t, out)
			if doc["exit_code"] != float64(c.exit) || doc["ok"] != false || !strings.Contains(doc["error"].(string), c.contains) {
				t.Errorf("document %v", doc)
			}
		})
	}
}

// ── metrics list, describe, exemplars ───────────────────────────────────

func TestMetricsListGoldens(t *testing.T) {
	f := mxFixture()
	out, _, err := mxRun(t, mxLocal(f), "metrics", "list", "dev", "--json")
	if err != nil {
		t.Fatal(err)
	}
	obsGolden(t, "metrics.list.json", out)
	var calls map[string]any
	for _, m := range decode(t, out)["metrics"].([]any) {
		if m.(map[string]any)["name"] == "rpc.server.calls" {
			calls = m.(map[string]any)
		}
	}
	if calls["type"] != "sum" || calls["monotonic"] != true || calls["temporality"] != "cumulative" || calls["seen"] != true || calls["queryable"] != true || calls["series_estimate"] != float64(2) {
		t.Errorf("catalog entry %v", calls)
	}
	cout, _, err := mxRun(t, mxCloud(&mxCaller{replies: map[string]string{cpListMetrics: mxCPCatalog}}), "metrics", "list", "prod", "--json")
	if err != nil {
		t.Fatal(err)
	}
	obsGolden(t, "metrics.list.cloud.json", cout)
	human, _, err := mxRun(t, mxLocal(f), "metrics", "list", "dev")
	if err != nil || !strings.Contains(human, "counter") || !strings.Contains(human, "LAST SEEN") {
		t.Errorf("human: %v\n%s", err, human)
	}
}

func TestMetricsDescribeGoldens(t *testing.T) {
	f := mxFixture()
	out, _, err := mxRun(t, mxLocal(f), "metrics", "describe", "dev", "rpc.server.calls", "--json")
	if err != nil {
		t.Fatal(err)
	}
	obsGolden(t, "metrics.describe.json", out)
	doc := decode(t, out)
	if doc["schema"] != "forge.obs.metric/v1" || doc["interval_seconds"] != float64(60) {
		t.Errorf("%v %v", doc["schema"], doc["interval_seconds"])
	}
	if sug := fmt.Sprint(doc["suggested"]); !strings.Contains(sug, "forge obs metrics query dev rpc.server.calls --group-by") {
		t.Errorf("suggested = %s", sug)
	}
	reply := `{"info":{"name":"rpc.server.calls","type":"METRIC_TYPE_SUM","unit":"{call}","monotonic":true,"temporality":"METRIC_TEMPORALITY_CUMULATIVE","seen":true,"queryable":true,"seriesEstimate":"2"},
	 "intervalSeconds":60,"attributes":[{"key":"rpc.method","distinctEstimate":"2"},{"key":"status","distinctEstimate":"3"},{"key":"k8s.pod.name","resource":true,"distinctEstimate":"400","highCardinality":true}],"suggestedBreakdowns":["status","rpc.method"]}`
	cout, _, err := mxRun(t, mxCloud(&mxCaller{replies: map[string]string{cpGetMetric: reply}}), "metrics", "describe", "prod", "rpc.server.calls", "--json")
	if err != nil {
		t.Fatal(err)
	}
	obsGolden(t, "metrics.describe.cloud.json", cout)
	human, _, err := mxRun(t, mxLocal(f), "metrics", "describe", "dev", "rpc.server.calls")
	if err != nil || !strings.Contains(human, "break down by") || !strings.Contains(human, "exported every 60s") {
		t.Errorf("human: %v\n%s", err, human)
	}
	_, _, err = mxRun(t, mxLocal(f), "metrics", "describe", "dev", "nope")
	if exitCodeForError(err) != 1 {
		t.Errorf("an unknown metric exits 1: %v", err)
	}
}

func TestMetricsExemplarsGoldens(t *testing.T) {
	f := mxFixture()
	out, _, err := mxRun(t, mxLocal(f), "metrics", "exemplars", "dev", "rpc.server.duration", "--at", "2026-10-22T11:30:30Z", "--min-value", "500", "--json")
	if err != nil {
		t.Fatal(err)
	}
	obsGolden(t, "metrics.exemplars.json", out)
	doc := decode(t, out)
	ex := doc["exemplars"].([]any)
	if len(ex) != 2 || ex[0].(map[string]any)["trace_stored"] != true || ex[1].(map[string]any)["trace_stored"] != false {
		t.Fatalf("stored traces first: %v", ex)
	}
	sug := fmt.Sprint(doc["suggested"])
	if !strings.Contains(sug, "forge obs trace 0af7651916cd43dd8448eb211c80319c dev") || strings.Contains(sug, strings.Repeat("b", 32)) {
		t.Errorf("only a stored trace is suggested: %s", sug)
	}
	w := doc["window"].(map[string]any)
	if w["from"] != "2026-10-22T11:28:30Z" || w["to"] != "2026-10-22T11:32:30Z" {
		t.Errorf("--at is the window ±2m: %v", w)
	}
	reply := `{"exemplars":[{"time":"2026-10-22T11:30:00Z","value":812.5,"traceId":"0AF7651916CD43DD8448EB211C80319C","spanId":"b7ad6b7169203331","attributes":{"http.route":"/v1/jobs"},"traceStored":true},
	  {"time":"2026-10-22T11:31:00Z","value":700,"traceId":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","spanId":"c1","traceStored":false}]}`
	mc := &mxCaller{replies: map[string]string{cpExemplars: reply}}
	cout, _, err := mxRun(t, mxCloud(mc), "metrics", "exemplars", "prod", "rpc.server.duration", "--from", "2026-10-22T11:28:00Z", "--to", "2026-10-22T11:33:00Z", "--min-value", "500", "--json")
	if err != nil {
		t.Fatal(err)
	}
	obsGolden(t, "metrics.exemplars.cloud.json", cout)
	if body := mc.calls[0].body; !strings.Contains(body, `"minValue":500`) || !strings.Contains(body, `"metricName":"rpc.server.duration"`) {
		t.Errorf("request %s", body)
	}
	_, _, err = mxRun(t, mxLocal(f), "metrics", "exemplars", "dev", "rpc.server.duration", "--at", "yesterday")
	if exitCodeForError(err) != 1 {
		t.Errorf("a bad --at: %v", err)
	}
	_, _, err = mxRun(t, mxLocal(f), "metrics", "exemplars", "dev", "rpc.server.duration", "--at", "2026-10-22T11:30:30Z", "--from", "2026-10-22T11:00:00Z")
	if exitCodeForError(err) != 1 {
		t.Errorf("--at with --from: %v", err)
	}
}

// TestMetricsExemplarsGroup: --group narrows to one series of a grouped panel,
// the group the console sends when a grouped series' point is clicked, through
// both backends.
func TestMetricsExemplarsGroup(t *testing.T) {
	f := mxFixture()
	args := []string{"metrics", "exemplars", "dev", "rpc.server.duration", "--group", "rpc.method=List", "--group", "service.name=api", "--json"}
	if _, _, err := mxRun(t, mxLocal(f), args...); err != nil {
		t.Fatal(err)
	}
	var st obsquery.Stmt
	for _, s := range f.Ran() {
		if strings.Contains(s.SQL, "`Exemplars.TimeUnix`") {
			st = s
		}
	}
	vals := fmt.Sprint(st.Params)
	if !strings.Contains(vals, "List") || !strings.Contains(vals, "api") {
		t.Errorf("the exemplars statement does not narrow to the group: %s %v", st.SQL, st.Params)
	}

	mc := &mxCaller{replies: map[string]string{cpExemplars: `{"exemplars":[]}`}}
	args[2] = "prod"
	if _, _, err := mxRun(t, mxCloud(mc), args...); err != nil {
		t.Fatal(err)
	}
	if body := mc.calls[0].body; !strings.Contains(body, `"group":{"rpc.method":"List","service.name":"api"}`) {
		t.Errorf("request %s", body)
	}

	for _, bad := range [][]string{{"--group", "rpc.method"}, {"--group", "=List"}, {"--group", "a=1", "--group", "a=2"}} {
		_, _, err := mxRun(t, mxLocal(f), append([]string{"metrics", "exemplars", "dev", "rpc.server.duration"}, bad...)...)
		if exitCodeForError(err) != 1 {
			t.Errorf("%v: want exit 1, got %v", bad, err)
		}
	}
}

func TestMetricsNewVerbsExitTwoOnAnOlderControlPlane(t *testing.T) {
	for _, c := range []struct {
		proc string
		args []string
	}{
		{"ListMetrics", []string{"metrics", "list", "prod", "--json"}},
		{"GetMetric", []string{"metrics", "describe", "prod", "m", "--json"}},
		{"ListMetricExemplars", []string{"metrics", "exemplars", "prod", "m", "--json"}},
	} {
		t.Run(c.proc, func(t *testing.T) {
			mc := &mxCaller{errs: map[string]error{"controlplane.v1.ObservabilityService/" + c.proc: &cloud.Error{Code: cloud.CodeUnimplemented}}}
			out, _, err := mxRun(t, mxCloud(mc), c.args...)
			if exitCodeForError(err) != 2 {
				t.Fatalf("exit = %d: %v", exitCodeForError(err), err)
			}
			if msg := decode(t, out)["error"].(string); !strings.Contains(msg, "this control plane does not serve "+c.proc+" yet") {
				t.Errorf("error = %s", msg)
			}
		})
	}
}

func TestMetricsGroupLists(t *testing.T) {
	var verbs []string
	var find func(c *cobra.Command)
	find = func(c *cobra.Command) {
		if c.Name() == "metrics" && c.Parent() != nil && c.Parent().Name() == "obs" {
			for _, s := range c.Commands() {
				verbs = append(verbs, s.Name())
			}
		}
		for _, s := range c.Commands() {
			find(s)
		}
	}
	find(newTestRootCmd(t))
	for _, want := range []string{"list", "query", "describe", "exemplars", "declared"} {
		found := false
		for _, v := range verbs {
			found = found || v == want
		}
		if !found {
			t.Errorf("forge obs metrics lacks %q (has %v)", want, verbs)
		}
	}
}
