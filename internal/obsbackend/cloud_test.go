package obsbackend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

type fakeCaller struct {
	replies map[string]string
	errs    map[string]error
	reqs    map[string]map[string]any
}

func (f *fakeCaller) Call(_ context.Context, procedure string, req, out any) error {
	b, _ := json.Marshal(req)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if f.reqs == nil {
		f.reqs = map[string]map[string]any{}
	}
	f.reqs[procedure] = m
	if err := f.errs[procedure]; err != nil {
		return err
	}
	r, ok := f.replies[procedure]
	if !ok {
		return fmt.Errorf("unexpected %s", procedure)
	}
	return json.Unmarshal([]byte(r), out)
}

var t0c = time.Date(2026, 10, 22, 11, 0, 0, 0, time.UTC)

func narrowC() Narrow {
	return Narrow{Range: Range{From: t0c, To: t0c.Add(time.Hour)}, Services: []string{"api"}, Filters: []Filter{{Key: "http.route", Value: "/x", Not: true}}}
}

func cloudWith(f *fakeCaller) Cloud {
	return Cloud{Env: "prod", EnvID: "env-1", OrgID: "org-1", Caller: f}
}

func TestCloudScopeIsBoundToTheEnv(t *testing.T) {
	f := &fakeCaller{replies: map[string]string{procSearchLogs: `{}`}}
	if _, err := cloudWith(f).Logs(context.Background(), LogsRequest{Narrow: narrowC(), Query: "body:x", TraceID: "abc", Oldest: true, Limit: 5}); err != nil {
		t.Fatal(err)
	}
	req := f.reqs[procSearchLogs]
	scope := req["scope"].(map[string]any)
	if scope["orgId"] != "org-1" || fmt.Sprint(scope["envIds"]) != "[env-1]" || fmt.Sprint(scope["services"]) != "[api]" {
		t.Errorf("scope %v", scope)
	}
	if fmt.Sprint(scope["filters"]) != "[map[key:http.route op:ATTRIBUTE_OP_NEQ value:/x]]" {
		t.Errorf("filters %v", scope["filters"])
	}
	if req["order"] != "SORT_ORDER_OLDEST_FIRST" || req["pageSize"] != float64(5) || !strings.Contains(req["query"].(string), "abc") {
		t.Errorf("request %v", req)
	}
}

func TestCloudTracesSlowIsDurationOrder(t *testing.T) {
	f := &fakeCaller{replies: map[string]string{procListTraces: `{"traces":[{"traceId":"t1","durationMs":900,"spanCount":3,"errorCount":1}],"red":{"traceCount":"120","errorTraceCount":"6","p95Ms":40}}`}}
	res, err := cloudWith(f).Traces(context.Background(), TracesRequest{Narrow: narrowC(), Slow: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if f.reqs[procListTraces]["order"] != "TRACE_ORDER_DURATION_DESC" {
		t.Errorf("order %v", f.reqs[procListTraces]["order"])
	}
	if len(res.Traces) != 1 || res.Traces[0].Spans != 3 || res.Summary == nil || res.Summary.Traces != 120 || res.Summary.ErrorTraces != 6 {
		t.Errorf("result %+v %+v", res.Traces, res.Summary)
	}
}

func TestCloudTraceReadsSpansAndLogs(t *testing.T) {
	f := &fakeCaller{replies: map[string]string{
		procGetTrace:   `{"traceId":"0af7651916cd43dd8448eb211c80319c","spans":[{"spanId":"a","name":"GET /x","service":"api","durationNs":"1500000","statusCode":"Error","events":[{"name":"exception"}]}]}`,
		procSearchLogs: `{"logs":[{"body":"x"}],"nextPageToken":"more"}`,
	}}
	res, err := cloudWith(f).Trace(context.Background(), TraceRequest{Narrow: narrowC(), TraceID: "0AF7651916CD43DD8448EB211C80319C", LogLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Spans) != 1 || res.Spans[0].DurationNs != 1500000 || len(res.Spans[0].Events) != 1 || len(res.Logs) != 1 || !res.LogsTruncated {
		t.Errorf("%+v", res)
	}
	f.replies[procGetTrace] = `{"spans":[]}`
	if _, err := cloudWith(f).Trace(context.Background(), TraceRequest{Narrow: narrowC(), TraceID: "0af7651916cd43dd8448eb211c80319c"}); exitOf(err) != ExitWrong {
		t.Errorf("an empty trace: %v", err)
	}
}

func TestCloudEndpointsAndPanels(t *testing.T) {
	f := &fakeCaller{replies: map[string]string{procQueryPanel: `{"columns":["requests","errors","p50","p95","p99"],"rows":[
		{"group":{"service.name":"api","span.name":"GET /a"},"values":[100,1,2,5,9]},
		{"group":{"service.name":"api","span.name":"GET /b"},"values":[10,5,20,50,90]}]}`}}
	res, err := cloudWith(f).Endpoints(context.Background(), EndpointsRequest{Narrow: narrowC(), Sort: "errors", Top: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Endpoints) != 1 || res.Endpoints[0].Name != "GET /b" || res.Omitted != 1 || res.Endpoints[0].ErrorRatio != 0.5 {
		t.Errorf("%+v", res)
	}
	panel := f.reqs[procQueryPanel]["panel"].(map[string]any)
	if panel["kind"] != "table" || fmt.Sprint(panel["groupBy"]) != "[service.name span.name]" {
		t.Errorf("panel %v", panel)
	}
}

func TestCloudSQLKeepsItsColumnOrder(t *testing.T) {
	f := &fakeCaller{replies: map[string]string{procQueryPanel: `{"sqlColumns":[{"name":"n","numeric":true},{"name":"svc"},{"name":"m","numeric":true}],
		"columns":["n","m"],"rows":[{"group":{"svc":"api"},"values":[3,4]}]}`}}
	res, err := cloudWith(f).Query(context.Background(), QueryRequest{Narrow: narrowC(), SQL: "SELECT 1"})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(res.Rows) != "[[3 api 4]]" || res.Columns[1].Name != "svc" {
		t.Errorf("%+v", res)
	}
	if _, err := cloudWith(f).Query(context.Background(), QueryRequest{Narrow: narrowC(), SQL: "SELECT 1", Params: map[string]string{"x": "1"}}); exitOf(err) != ExitWrong {
		t.Errorf("--param on cloud: %v", err)
	}
}

func TestCloudMetrics(t *testing.T) {
	f := &fakeCaller{replies: map[string]string{
		procListMetrics: `{"metrics":[{"name":"http.server.duration","type":"METRIC_TYPE_HISTOGRAM"}],"truncated":true}`,
		procQueryMetric: `{"series":[{"labels":{"svc":"api"},"points":[{"time":"2026-10-22T11:00:00Z","value":2}]}],"stepSeconds":60,"metricType":"METRIC_TYPE_HISTOGRAM","aggregation":"METRIC_AGGREGATION_P95"}`,
	}}
	l, err := cloudWith(f).MetricsList(context.Background(), MetricsListRequest{Narrow: narrowC(), Limit: 1})
	if err != nil || l.Metrics[0].Type != "histogram" || !l.Truncated {
		t.Errorf("%+v %v", l, err)
	}
	q, err := cloudWith(f).MetricsQuery(context.Background(), MetricsQueryRequest{Narrow: narrowC(), Name: "http.server.duration", Agg: "p95"})
	if err != nil || q.Agg != "p95" || q.Type != "histogram" || len(q.Series) != 1 {
		t.Errorf("%+v %v", q, err)
	}
	if f.reqs[procQueryMetric]["aggregation"] != "METRIC_AGGREGATION_P95" {
		t.Errorf("agg %v", f.reqs[procQueryMetric]["aggregation"])
	}
}

// --explain on a cloud env compiles with the shared compiler and calls nothing.
func TestCloudExplainCompilesLocally(t *testing.T) {
	f := &fakeCaller{}
	n := narrowC()
	n.Explain = true
	res, err := cloudWith(f).Logs(context.Background(), LogsRequest{Narrow: n, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Statements) != 1 || !strings.Contains(res.Statements[0].Purpose, "compiled locally") || len(f.reqs) != 0 {
		t.Errorf("%+v %v", res, f.reqs)
	}
	_ = obsquery.LocalDatabase
}

func exitOf(err error) int {
	var be *Error
	if errors.As(err, &be) {
		return be.Code
	}
	return -1
}

func TestMapCloudError(t *testing.T) {
	for code, want := range map[string]int{
		"invalid_argument": ExitWrong, "not_found": ExitWrong,
		"unavailable": ExitUndetermined, "permission_denied": ExitUndetermined, "unauthenticated": ExitUndetermined,
		"resource_exhausted": ExitUndetermined, "failed_precondition": ExitUndetermined,
	} {
		if got := exitOf(MapCloudError(&cloud.Error{Code: code})); got != want {
			t.Errorf("%s: exit %d, want %d", code, got, want)
		}
	}
	if exitOf(MapCloudError(errors.New("dial tcp: refused"))) != ExitUndetermined {
		t.Error("a transport failure is undetermined")
	}
	if MapCloudError(nil) != nil {
		t.Error("nil")
	}
}
