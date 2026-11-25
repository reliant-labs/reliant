package obsbackend_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/obsbackend/obstest"
	"github.com/reliant-labs/forge/pkg/observe/metrics"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

var (
	t0   = time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)
	t1   = t0.Add(time.Hour)
	late = t1.Add(2 * time.Hour) // "now": every bucket is complete
)

func narrow() obsbackend.Narrow {
	return obsbackend.Narrow{Range: obsbackend.Range{From: t0, To: t1}, Now: late}
}

func pts(vals ...float64) []obstest.FakePoint {
	var out []obstest.FakePoint
	for i, v := range vals {
		out = append(out, obstest.FakePoint{Bucket: t0.Add(time.Duration(i) * time.Minute), Value: v})
	}
	return out
}

func grouped(label string, vals ...float64) []obstest.FakePoint {
	out := pts(vals...)
	for i := range out {
		out[i].Labels = []string{label}
	}
	return out
}

func fixture() *obstest.FakeMetrics {
	calls := append(grouped("List", 2, 2, 2), grouped("Get", 1, 1, 1)...)
	errs := grouped("List", 1, 0, 1)
	return &obstest.FakeMetrics{Metrics: map[string]obstest.FakeMetric{
		"rpc.server.calls": {Kind: obsquery.CatalogSum, Unit: "{call}", Monotonic: true, Temporality: 2, Services: []string{"api"}, Series: 2,
			Attrs: []obstest.FakeAttr{{Key: "rpc.method", Distinct: 2}, {Key: "status", Distinct: 3}}, DeclaredKeys: []string{"rpc.method", "status"},
			Points: calls, Reduced: []obstest.FakePoint{{Bucket: t0, Labels: []string{"List"}, Value: 2}, {Bucket: t0, Labels: []string{"Get"}, Value: 1}}, LastSeen: t1},
		"rpc.server.errors": {Kind: obsquery.CatalogSum, Unit: "{call}", Monotonic: true, Temporality: 2, Services: []string{"api"}, Series: 1,
			DeclaredKeys: []string{"rpc.method"}, Points: errs, LastSeen: t1},
		"heap": {Kind: obsquery.CatalogGauge, Unit: "By", Services: []string{"api", "worker"}, Series: 1, Points: pts(10, 20, 30),
			Reduced: []obstest.FakePoint{{Bucket: t0, Value: 30}}, LastSeen: t1},
		"empty.series": {Kind: obsquery.CatalogGauge, Unit: "1", Series: 1, LastSeen: t1},
	}}
}

func panelAB() obsquery.Panel {
	return obsquery.Panel{ID: "explore", Kind: "line", Signal: "metrics", GroupBy: []string{"rpc.method"}, Formula: "B / A * 100",
		Series: []obsquery.PanelSeries{
			{Label: "A", Field: "rpc.server.calls", Agg: "rate", MetricType: "sum"},
			{Label: "B", Field: "rpc.server.errors", Agg: "rate", MetricType: "sum"},
		}}
}

func local(f *obstest.FakeMetrics) obsbackend.Local { return obsbackend.Local{Env: "dev", Runner: f} }

func TestLocalMetricsQueryRunsTheEvaluator(t *testing.T) {
	f := fixture()
	res, err := local(f).MetricsQuery(context.Background(), obsbackend.MetricsQueryRequest{Narrow: narrow(), Panel: panelAB()})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Meta) != 2 || res.Meta[0].Agg != "rate" || res.Meta[0].Type != "sum" || !res.Meta[0].Monotonic ||
		res.Meta[0].Temporality != "cumulative" || res.Meta[0].ValueUnit != "{call}/s" {
		t.Errorf("meta = %+v", res.Meta)
	}
	if len(res.Series) != 2 || res.Series[0].Label != "formula" || res.Series[0].Labels["rpc.method"] != "List" {
		t.Fatalf("a formula over each rpc.method (Get has no errors: 0, a counter's missing point): %+v", res.Series)
	}
	got := []float64{}
	for _, p := range res.Series[0].Points {
		got = append(got, p.Value)
	}
	if fmt.Sprint(got) != "[50 0 50]" {
		t.Errorf("B / A * 100 = %v, want [50 0 50]", got)
	}
	if res.StepSeconds < 1 {
		t.Errorf("step = %d", res.StepSeconds)
	}
	for _, s := range res.Statements {
		if len(s.Params) == 0 {
			continue
		}
		for _, v := range s.Params {
			if v != "" && strings.Contains(s.SQL, "'"+v+"'") {
				t.Errorf("a caller value is in the SQL text of %q: %s", s.Purpose, s.SQL)
			}
		}
	}
}

func TestLocalMetricsQueryExplainRunsNothing(t *testing.T) {
	f := fixture()
	n := narrow()
	n.Explain = true
	res, err := local(f).MetricsQuery(context.Background(), obsbackend.MetricsQueryRequest{Narrow: n, Panel: panelAB()})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Ran()) != 0 {
		t.Errorf("--explain ran %d statements", len(f.Ran()))
	}
	if len(res.Statements) < 4 {
		t.Errorf("explain statements = %d, want a lookup and a series per metric", len(res.Statements))
	}
}

func TestLocalMetricsQueryNumberTableAndRefusals(t *testing.T) {
	f := fixture()
	ctx := context.Background()
	num := obsquery.Panel{ID: "p", Kind: "number", Signal: "metrics", Series: []obsquery.PanelSeries{{Label: "A", Field: "heap", Agg: "avg", MetricType: "gauge"}}}
	res, err := local(f).MetricsQuery(ctx, obsbackend.MetricsQueryRequest{Narrow: narrow(), Panel: num})
	if err != nil || len(res.Series) != 1 || res.Series[0].Reduced == nil || *res.Series[0].Reduced != 30 {
		t.Fatalf("number: %+v %v", res, err)
	}
	tbl := obsquery.Panel{ID: "p", Kind: "table", Signal: "metrics", GroupBy: []string{"rpc.method"}, Limit: 5,
		Series: []obsquery.PanelSeries{{Label: "A", Field: "rpc.server.calls", Agg: "rate", MetricType: "sum"}}}
	res, err = local(f).MetricsQuery(ctx, obsbackend.MetricsQueryRequest{Narrow: narrow(), Panel: tbl})
	if err != nil || len(res.Series) != 2 || *res.Series[0].Reduced != 2 || res.Series[0].Labels["rpc.method"] != "List" {
		t.Fatalf("a table over metrics is supported locally: %+v %v", res, err)
	}

	for name, c := range map[string]struct {
		panel obsquery.Panel
		want  string
	}{
		"a rate of a gauge": {obsquery.Panel{Kind: "line", Signal: "metrics", Series: []obsquery.PanelSeries{{Label: "A", Field: "heap", Agg: "rate", MetricType: "gauge"}}}, "gauge"},
		"an unknown metric": {obsquery.Panel{Kind: "line", Signal: "metrics", Series: []obsquery.PanelSeries{{Label: "A", Field: "nope", Agg: "avg", MetricType: "gauge"}}}, `no metric "nope"`},
	} {
		_, err := local(f).MetricsQuery(ctx, obsbackend.MetricsQueryRequest{Narrow: narrow(), Panel: c.panel})
		var be *obsbackend.Error
		if !asBackendErr(err, &be) || be.Code != obsbackend.ExitWrong || !strings.Contains(be.Error(), c.want) {
			t.Errorf("%s: want exit 1 mentioning %q, got %v", name, c.want, err)
		}
	}
	// A refusal of the runner's own keeps its status: the stack being down is "could not look".
	down := &obstest.FakeMetrics{Err: obsbackend.Undetermined("the local ClickStack is not running")}
	_, err = local(down).MetricsQuery(ctx, obsbackend.MetricsQueryRequest{Narrow: narrow(), Panel: num})
	var be *obsbackend.Error
	if !asBackendErr(err, &be) || be.Code != obsbackend.ExitUndetermined {
		t.Errorf("a runner failure is exit 2: %v", err)
	}
}

func asBackendErr(err error, target **obsbackend.Error) bool {
	for err != nil {
		if be, ok := err.(*obsbackend.Error); ok {
			*target = be
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func TestMetricsCompareShiftsTheRangeAndOverlaysTheSeries(t *testing.T) {
	f := fixture()
	off := 24 * time.Hour
	res, err := local(f).MetricsQuery(context.Background(), obsbackend.MetricsQueryRequest{Narrow: narrow(), Panel: panelAB(), Compare: off})
	if err != nil {
		t.Fatal(err)
	}
	c := res.Compare
	if c == nil || c.Offset != off || !c.From.Equal(t0.Add(-off)) || !c.To.Equal(t1.Add(-off)) {
		t.Fatalf("compare = %+v", c)
	}
	// the fake ignores the range, so the compared read returns the same buckets
	// and moving them forward puts them exactly Offset after the current ones
	if len(c.Series) == 0 || !c.Series[0].Points[0].Time.Equal(res.Series[0].Points[0].Time.Add(off)) {
		t.Errorf("the compared points are moved forward to overlay the current range: %+v vs %+v", c.Series, res.Series)
	}
	if _, err := local(f).MetricsQuery(context.Background(), obsbackend.MetricsQueryRequest{Narrow: narrow(), Panel: panelAB(), Compare: -time.Hour}); err == nil {
		t.Error("a negative compare is refused")
	}
}

func TestLocalMetricsListMergesTheDeclaredSchema(t *testing.T) {
	f := fixture()
	l := local(f)
	l.Declared = func() []metrics.Schema {
		return []metrics.Schema{{Service: "api", Metrics: []metrics.Spec{
			{Name: "rpc.server.calls", Type: metrics.Counter, Unit: "{call}", Description: "authored", Stability: metrics.Stable,
				Attributes: []metrics.Attribute{{Key: "status", Values: []string{"ok", "error"}}}},
			{Name: "never.emitted", Type: metrics.Counter, Description: "typo?", Stability: metrics.Experimental},
		}}}
	}
	res, err := l.MetricsList(context.Background(), obsbackend.MetricsListRequest{Narrow: narrow(), Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]obsbackend.MetricInfo{}
	for _, m := range res.Metrics {
		by[m.Name] = m
	}
	calls := by["rpc.server.calls"]
	if !calls.Declared || !calls.Seen || calls.Stability != "stable" || calls.Description != "authored" || !calls.Monotonic ||
		calls.Temporality != "cumulative" || calls.Type != "sum" || len(calls.Services) != 1 || len(calls.DeclaredAttributes) != 1 {
		t.Errorf("a declared, seen metric: %+v", calls)
	}
	if never := by["never.emitted"]; !never.Declared || never.Seen {
		t.Errorf("declared and never seen: %+v", never)
	}
	if heap := by["heap"]; heap.Declared || !heap.Seen || heap.Queryable != true {
		t.Errorf("seen, not declared: %+v", heap)
	}
	filtered, _ := l.MetricsList(context.Background(), obsbackend.MetricsListRequest{Narrow: narrow(), NameContains: "NEVER", Limit: 50})
	if len(filtered.Metrics) != 1 || filtered.Metrics[0].Name != "never.emitted" {
		t.Errorf("--contains applies to the declared ones too: %+v", filtered.Metrics)
	}
}

// NoEnv drops the env predicate from the catalog, as it does from a platform
// dashboard's panel read: on the control plane the scope names no env, and
// locally the statement carries no env filter (#1548).
func TestMetricsListNoEnvDropsTheEnvPredicate(t *testing.T) {
	envIn := func(st []obsbackend.Statement) bool {
		for _, s := range st {
			if strings.Contains(s.SQL, "'dev'") {
				return true
			}
			for _, v := range s.Params {
				if v == "dev" {
					return true
				}
			}
		}
		return false
	}
	for _, noEnv := range []bool{false, true} {
		res, err := local(fixture()).MetricsList(context.Background(), obsbackend.MetricsListRequest{Narrow: obsbackend.Narrow{Range: narrow().Range, Explain: true}, Limit: 5, NoEnv: noEnv})
		if err != nil {
			t.Fatal(err)
		}
		if got := envIn(res.Statements); got == noEnv {
			t.Errorf("local NoEnv=%v: env filter present=%v\n%+v", noEnv, got, res.Statements)
		}

		rc := &recCaller{replies: map[string]string{procListMetrics: `{"metrics":[]}`}}
		if _, err := cloudWith(rc).MetricsList(context.Background(), obsbackend.MetricsListRequest{Narrow: narrow(), Limit: 5, NoEnv: noEnv}); err != nil {
			t.Fatal(err)
		}
		scope, _ := rc.calls[procListMetrics]["scope"].(map[string]any)
		if _, bound := scope["envIds"]; bound == noEnv {
			t.Errorf("cloud NoEnv=%v: scope %v", noEnv, scope)
		}
	}
}

func TestLocalMetricsDescribe(t *testing.T) {
	f := fixture()
	l := local(f)
	res, err := l.MetricsDescribe(context.Background(), obsbackend.MetricsDescribeRequest{Narrow: narrow(), Name: "rpc.server.calls"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Info.Type != "sum" || !res.Info.Monotonic || res.IntervalSeconds != 60 {
		t.Errorf("info %+v interval %d", res.Info, res.IntervalSeconds)
	}
	if len(res.Attributes) != 2 || res.Attributes[0].Key == "" {
		t.Errorf("attributes %+v", res.Attributes)
	}
	if len(res.SuggestedBreakdowns) == 0 {
		t.Error("a key with 2 to 50 values is a suggested breakdown")
	}
	if _, err := l.MetricsDescribe(context.Background(), obsbackend.MetricsDescribeRequest{Narrow: narrow(), Name: "nope"}); err == nil {
		t.Error("an unknown metric is an error")
	}
}

func TestLocalMetricsExemplarsListStoredTracesFirst(t *testing.T) {
	f := fixture()
	m := f.Metrics["heap"]
	m.Exemplars = []obstest.FakeExemplar{
		{Time: t0.Add(3 * time.Minute), Value: 900, TraceID: strings.Repeat("a", 32), SpanID: "s1"},
		{Time: t0.Add(2 * time.Minute), Value: 700, TraceID: strings.Repeat("b", 32), SpanID: "s2"},
	}
	f.Metrics["heap"] = m
	f.StoredTraces = []string{strings.Repeat("b", 32)}
	res, err := local(f).MetricsExemplars(context.Background(), obsbackend.MetricsExemplarsRequest{Narrow: narrow(), Name: "heap"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Exemplars) != 2 || !res.Exemplars[0].TraceStored || res.Exemplars[0].TraceID != strings.Repeat("b", 32) || res.Exemplars[1].TraceStored {
		t.Errorf("the stored trace sorts first: %+v", res.Exemplars)
	}
}

func TestLocalQueryPanelRunsAMetricsPanel(t *testing.T) {
	f := fixture()
	p := panelAB()
	res, err := local(f).Query(context.Background(), obsbackend.QueryRequest{Narrow: narrow(), Panel: &p, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, c := range res.Columns {
		names = append(names, c.Name)
	}
	if strings.Join(names, ",") != "series,rpc.method,time,value" || len(res.Rows) != 6 || res.Rows[0][0] != "formula" {
		t.Errorf("columns %v rows %v", names, res.Rows)
	}
}

// ── cloud ───────────────────────────────────────────────────────────────

type recCaller struct {
	replies map[string]string
	errs    map[string]error
	calls   map[string]map[string]any
}

func (r *recCaller) Call(_ context.Context, procedure string, req, out any) error {
	b, _ := json.Marshal(req)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if r.calls == nil {
		r.calls = map[string]map[string]any{}
	}
	r.calls[procedure] = m
	if err := r.errs[procedure]; err != nil {
		return err
	}
	reply, ok := r.replies[procedure]
	if !ok {
		return fmt.Errorf("unexpected %s", procedure)
	}
	return json.Unmarshal([]byte(reply), out)
}

const (
	procQueryPanel  = "controlplane.v1.ObservabilityService/QueryPanel"
	procListMetrics = "controlplane.v1.ObservabilityService/ListMetrics"
	procGetMetric   = "controlplane.v1.ObservabilityService/GetMetric"
	procExemplars   = "controlplane.v1.ObservabilityService/ListMetricExemplars"
)

func cloudWith(c obsbackend.Caller) obsbackend.Cloud {
	return obsbackend.Cloud{Env: "prod", EnvID: "env-1", OrgID: "org-1", Caller: c}
}

func TestCloudMetricsQuerySendsThePanelThroughQueryPanel(t *testing.T) {
	rc := &recCaller{replies: map[string]string{procQueryPanel: `{"kind":"line","stepSeconds":60,"seriesTotal":1,"warnings":["step_raised: 10s requested, 60s used"],
		"seriesMeta":[{"label":"A","metric":"rpc.server.calls","metricType":"METRIC_TYPE_SUM","monotonic":true,"temporality":"METRIC_TEMPORALITY_CUMULATIVE","aggregation":"rate","valueUnit":"{call}/s"}],
		"series":[{"label":"A","group":{"rpc.method":"List"},"points":[{"time":"2026-10-22T10:00:00Z","value":2},{"time":"2026-10-22T10:01:00Z","null":true},{"time":"2026-10-22T10:02:00Z","value":2,"partial":true}]}]}`}}
	p := obsquery.Panel{ID: "explore", Kind: "line", Signal: "metrics", GroupBy: []string{"rpc.method"},
		Series: []obsquery.PanelSeries{{Label: "A", Field: "rpc.server.calls", Agg: "rate", MetricType: "sum", Where: []obsquery.PanelPredicate{{Field: "metric.attr.status", Op: "eq", Value: "error"}}}}}
	res, err := cloudWith(rc).MetricsQuery(context.Background(), obsbackend.MetricsQueryRequest{Narrow: narrow(), Panel: p, StepSeconds: 10})
	if err != nil {
		t.Fatal(err)
	}
	req := rc.calls[procQueryPanel]
	panel := req["panel"].(map[string]any)
	if panel["signal"] != "metrics" || panel["kind"] != "line" || fmt.Sprint(panel["groupBy"]) != "[rpc.method]" || req["stepSeconds"] != float64(10) {
		t.Errorf("request %v", req)
	}
	ser := panel["series"].([]any)[0].(map[string]any)
	if ser["metricType"] != "sum" || ser["agg"] != "rate" || ser["field"] != "rpc.server.calls" {
		t.Errorf("series %v", ser)
	}
	if res.StepSeconds != 60 || len(res.Warnings) != 1 || res.SeriesTotal != 1 {
		t.Errorf("step %d warnings %v total %d", res.StepSeconds, res.Warnings, res.SeriesTotal)
	}
	m := res.Meta[0]
	if m.Agg != "rate" || m.Type != "sum" || !m.Monotonic || m.Temporality != "cumulative" || m.ValueUnit != "{call}/s" {
		t.Errorf("meta %+v", m)
	}
	pts := res.Series[0].Points
	if len(pts) != 3 || !pts[1].Null || !pts[2].Partial || pts[0].Value != 2 {
		t.Errorf("points %+v", pts)
	}
	n := narrow()
	n.Explain = true
	ex, err := cloudWith(&recCaller{}).MetricsQuery(context.Background(), obsbackend.MetricsQueryRequest{Narrow: n, Panel: p})
	if err != nil || len(ex.Statements) == 0 || !strings.Contains(ex.Statements[0].Purpose, "compiled locally") {
		t.Errorf("--explain compiles locally and calls nothing: %+v %v", ex, err)
	}
}

func TestCloudMetricsAnOlderControlPlaneIsExit2AndABadRequestExit1(t *testing.T) {
	p := panelAB()
	for name, c := range map[string]struct {
		err      error
		code     int
		contains string
	}{
		"unimplemented":   {&cloud.Error{Code: cloud.CodeUnimplemented}, obsbackend.ExitUndetermined, "this control plane does not serve QueryPanel yet"},
		"501":             {&cloud.Error{HTTPStatus: 501}, obsbackend.ExitUndetermined, "does not serve QueryPanel yet"},
		"invalid":         {&cloud.Error{Code: cloud.CodeInvalidArgument, Message: "bad"}, obsbackend.ExitWrong, ""},
		"unauthenticated": {&cloud.Error{Code: cloud.CodeUnauthenticated}, obsbackend.ExitUndetermined, ""},
	} {
		rc := &recCaller{errs: map[string]error{procQueryPanel: c.err}}
		_, err := cloudWith(rc).MetricsQuery(context.Background(), obsbackend.MetricsQueryRequest{Narrow: narrow(), Panel: p})
		var be *obsbackend.Error
		if !asBackendErr(err, &be) || be.Code != c.code || !strings.Contains(be.Error(), c.contains) {
			t.Errorf("%s: want exit %d %q, got %v", name, c.code, c.contains, err)
		}
	}
	for proc, call := range map[string]func(obsbackend.Cloud) error{
		"ListMetrics": func(c obsbackend.Cloud) error {
			_, err := c.MetricsList(context.Background(), obsbackend.MetricsListRequest{Narrow: narrow(), Limit: 5})
			return err
		},
		"GetMetric": func(c obsbackend.Cloud) error {
			_, err := c.MetricsDescribe(context.Background(), obsbackend.MetricsDescribeRequest{Narrow: narrow(), Name: "m"})
			return err
		},
		"ListMetricExemplars": func(c obsbackend.Cloud) error {
			_, err := c.MetricsExemplars(context.Background(), obsbackend.MetricsExemplarsRequest{Narrow: narrow(), Name: "m"})
			return err
		},
	} {
		rc := &recCaller{errs: map[string]error{"controlplane.v1.ObservabilityService/" + proc: &cloud.Error{Code: cloud.CodeUnimplemented}}}
		err := call(cloudWith(rc))
		var be *obsbackend.Error
		if !asBackendErr(err, &be) || be.Code != obsbackend.ExitUndetermined || !strings.Contains(be.Error(), "this control plane does not serve "+proc+" yet") {
			t.Errorf("%s: %v", proc, err)
		}
	}
}

func TestCloudMetricsCatalogDescribeAndExemplarsWire(t *testing.T) {
	rc := &recCaller{replies: map[string]string{
		procListMetrics: `{"metrics":[{"name":"rpc.server.calls","type":"METRIC_TYPE_SUM","unit":"{call}","monotonic":true,"temporality":"METRIC_TEMPORALITY_DELTA",
			"services":["api"],"serviceCount":3,"lastSeen":"2026-10-22T10:59:00Z","seriesEstimate":"12","queryable":true,"builtin":true,"declared":true,"seen":true,"stability":"stable",
			"declaredAttributes":[{"key":"status","values":["ok","error"]}]},
			{"name":"lat","type":"METRIC_TYPE_EXPONENTIAL_HISTOGRAM","seen":true}],"truncated":true}`,
		procGetMetric: `{"info":{"name":"lat","type":"METRIC_TYPE_HISTOGRAM","seen":true,"queryable":true},"intervalSeconds":60,"mixedTemporality":true,
			"attributes":[{"key":"route","distinctEstimate":"7","highCardinality":false},{"key":"k8s.pod.name","resource":true,"distinctEstimate":"900","highCardinality":true}],"suggestedBreakdowns":["route"]}`,
		procExemplars: `{"exemplars":[{"time":"2026-10-22T10:30:00Z","value":812.5,"traceId":"0AF7651916CD43DD8448EB211C80319C","spanId":"b7ad6b7169203331","attributes":{"http.route":"/x"},"traceStored":true},
			{"time":"2026-10-22T10:20:00Z","value":700,"traceId":"bb","traceStored":false}]}`,
	}}
	c := cloudWith(rc)
	l, err := c.MetricsList(context.Background(), obsbackend.MetricsListRequest{Narrow: narrow(), NameContains: "rpc", Limit: 7})
	if err != nil || !l.Truncated || len(l.Metrics) != 2 {
		t.Fatalf("%+v %v", l, err)
	}
	m := l.Metrics[0]
	if m.Type != "sum" || !m.Monotonic || m.Temporality != "delta" || m.ServiceCount != 3 || m.SeriesEstimate != 12 || !m.Declared || !m.Seen ||
		m.Stability != "stable" || len(m.DeclaredAttributes) != 1 || m.LastSeen.IsZero() || !m.Builtin {
		t.Errorf("catalog entry %+v", m)
	}
	if l.Metrics[1].Type != "exp_histogram" {
		t.Errorf("exp histogram: %+v", l.Metrics[1])
	}
	if rc.calls[procListMetrics]["nameContains"] != "rpc" || rc.calls[procListMetrics]["limit"] != float64(7) {
		t.Errorf("request %v", rc.calls[procListMetrics])
	}
	d, err := c.MetricsDescribe(context.Background(), obsbackend.MetricsDescribeRequest{Narrow: narrow(), Name: "lat"})
	if err != nil || d.IntervalSeconds != 60 || !d.MixedTemporality || len(d.Attributes) != 2 || d.Attributes[1].DistinctEstimate != 900 || !d.Attributes[1].HighCardinality || d.SuggestedBreakdowns[0] != "route" {
		t.Errorf("%+v %v", d, err)
	}
	if rc.calls[procGetMetric]["metricName"] != "lat" {
		t.Errorf("request %v", rc.calls[procGetMetric])
	}
	min := 500.0
	e, err := c.MetricsExemplars(context.Background(), obsbackend.MetricsExemplarsRequest{Narrow: narrow(), Name: "lat", From: t0.Add(10 * time.Minute), To: t0.Add(40 * time.Minute), MinValue: &min, Where: []obsquery.PanelPredicate{{Field: "metric.attr.route", Op: "eq", Value: "/x"}}})
	if err != nil || len(e.Exemplars) != 2 || e.Exemplars[0].TraceID != "0af7651916cd43dd8448eb211c80319c" || !e.Exemplars[0].TraceStored || e.Exemplars[1].TraceStored {
		t.Errorf("%+v %v", e, err)
	}
	req := rc.calls[procExemplars]
	if req["minValue"] != 500.0 || req["timeRange"] == nil || req["metricName"] != "lat" {
		t.Errorf("request %v", req)
	}
}

// ── parity ──────────────────────────────────────────────────────────────

// seriesJSON is what a verb prints for a result's series: the document the
// two backends must agree on.
func seriesJSON(t *testing.T, r obsbackend.MetricsQueryResult) string {
	t.Helper()
	type pt struct {
		Time    time.Time `json:"time"`
		Value   float64   `json:"value"`
		Partial bool      `json:"partial,omitempty"`
		Null    bool      `json:"null,omitempty"`
	}
	type ser struct {
		Label   string            `json:"label"`
		Labels  map[string]string `json:"labels,omitempty"`
		Reduced *float64          `json:"reduced,omitempty"`
		Points  []pt              `json:"points"`
	}
	out := struct {
		Meta        []obsbackend.MetricMeta `json:"meta"`
		Series      []ser                   `json:"series"`
		SeriesTotal int                     `json:"series_total"`
		Warnings    []string                `json:"warnings"`
		Step        int64                   `json:"step_seconds"`
	}{Meta: r.Meta, SeriesTotal: r.SeriesTotal, Warnings: append([]string{}, r.Warnings...), Step: r.StepSeconds}
	for _, s := range r.Series {
		x := ser{Label: s.Label, Labels: s.Labels, Reduced: s.Reduced, Points: []pt{}}
		for _, p := range s.Points {
			x.Points = append(x.Points, pt{p.Time, p.Value, p.Partial, p.Null})
		}
		out.Series = append(out.Series, x)
	}
	b, err := json.MarshalIndent(out, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// cpFor is the reply the control plane gives for panel p over the fixture's
// data: its QueryPanel handler is obsquery.EvalMetricPanel over its own runner,
// mapped field for field onto QueryPanelResponse (obstest.CPPanelResponse).
func cpFor(t *testing.T, f *obstest.FakeMetrics, p obsquery.Panel) string {
	t.Helper()
	runner := func(ctx context.Context, st obsquery.Stmt) (obsquery.Rows, error) {
		r, err := f.Run(ctx, st, obsbackend.Limits{})
		if err != nil {
			return nil, err
		}
		return obsbackend.RowsOf(r), nil
	}
	sc := obsquery.Scope{Database: obsquery.LocalDatabase, Local: true, Start: t0, End: t1}
	res, err := obsquery.EvalMetricPanel(context.Background(), runner, sc, p, obsquery.EvalOptions{Now: late})
	if err != nil {
		t.Fatal(err)
	}
	return obstest.CPPanelResponse(p.Kind, res)
}

func TestMetricsParityLocalAndCloudReturnTheSameSeries(t *testing.T) {
	f := fixture()
	num := obsquery.Panel{ID: "p", Kind: "number", Signal: "metrics", Series: []obsquery.PanelSeries{{Label: "A", Field: "heap", Agg: "avg", MetricType: "gauge"}}}
	tbl := obsquery.Panel{ID: "p", Kind: "table", Signal: "metrics", GroupBy: []string{"rpc.method"}, Limit: 5,
		Series: []obsquery.PanelSeries{{Label: "A", Field: "rpc.server.calls", Agg: "rate", MetricType: "sum"}}}
	for name, p := range map[string]obsquery.Panel{"two metrics, a formula and a group-by": panelAB(), "a number": num, "a table": tbl} {
		t.Run(name, func(t *testing.T) {
			lres, err := local(f).MetricsQuery(context.Background(), obsbackend.MetricsQueryRequest{Narrow: narrow(), Panel: p})
			if err != nil {
				t.Fatal(err)
			}
			rc := &recCaller{replies: map[string]string{procQueryPanel: cpFor(t, f, p)}}
			cres, err := cloudWith(rc).MetricsQuery(context.Background(), obsbackend.MetricsQueryRequest{Narrow: narrow(), Panel: p})
			if err != nil {
				t.Fatal(err)
			}
			if l, c := seriesJSON(t, lres), seriesJSON(t, cres); l != c {
				t.Errorf("the backends disagree\n--- local\n%s\n--- cloud\n%s", l, c)
			}
		})
	}
}

func TestMetricTypeNameCoversAllFiveKinds(t *testing.T) {
	for kind, want := range map[obsquery.MetricType]string{
		obsquery.MetricGauge: "gauge", obsquery.MetricSum: "sum", obsquery.MetricHistogram: "histogram",
		obsquery.MetricExpHistogram: "exp_histogram", obsquery.MetricSummary: "summary", obsquery.MetricUnknown: "",
	} {
		if got := obsbackend.MetricTypeName(kind); got != want {
			t.Errorf("MetricTypeName(%v) = %q, want %q", kind, got, want)
		}
	}
	for kind, want := range map[obsquery.CatalogKind]string{
		obsquery.CatalogGauge: "gauge", obsquery.CatalogSum: "sum", obsquery.CatalogHistogram: "histogram",
		obsquery.CatalogExpHistogram: "exp_histogram", obsquery.CatalogSummary: "summary",
	} {
		if got := obsbackend.MetricTypeName(obsquery.MetricType(kind)); got != want || kind.String() != want {
			t.Errorf("catalog kind %v: MetricTypeName = %q, String = %q, want %q", int(kind), got, kind.String(), want)
		}
	}
}
