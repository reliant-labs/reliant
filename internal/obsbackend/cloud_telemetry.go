package obsbackend

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/obsquery"
)

// ── logs ────────────────────────────────────────────────────────────────

type wireLogRecord struct {
	Timestamp      time.Time         `json:"timestamp"`
	SeverityText   string            `json:"severityText"`
	SeverityNumber int               `json:"severityNumber"`
	Service        string            `json:"service"`
	Body           string            `json:"body"`
	TraceID        string            `json:"traceId"`
	SpanID         string            `json:"spanId"`
	Attributes     map[string]string `json:"attributes"`
}

// recordHash identifies a record for follow mode's de-duplication (the local
// backend reads ClickHouse's own row hash; the control plane sends none).
func (r wireLogRecord) recordHash() uint64 {
	h := fnv.New64a()
	for _, s := range []string{r.Timestamp.UTC().Format(time.RFC3339Nano), r.Service, r.TraceID, r.SpanID, r.Body} {
		_, _ = h.Write([]byte(s))
		_, _ = h.Write([]byte{0})
	}
	return h.Sum64()
}

func (r wireLogRecord) record() LogRecord {
	return LogRecord{
		Time: r.Timestamp.UTC(), Severity: r.SeverityText, SeverityNumber: r.SeverityNumber,
		Service: r.Service, Body: r.Body, TraceID: r.TraceID, SpanID: r.SpanID,
		Attributes: r.Attributes, Hash: r.recordHash(),
	}
}

// Logs implements Backend over SearchLogs.
func (c Cloud) Logs(ctx context.Context, req LogsRequest) (LogsResult, error) {
	if req.Explain {
		res, err := c.explainer().Logs(ctx, req)
		res.Statements = explained(res.Statements)
		return res, err
	}
	order := "SORT_ORDER_NEWEST_FIRST"
	if req.Oldest {
		order = "SORT_ORDER_OLDEST_FIRST"
	}
	in := struct {
		Scope      wireScope `json:"scope"`
		Query      string    `json:"query,omitempty"`
		Severities []string  `json:"severities,omitempty"`
		Order      string    `json:"order"`
		PageSize   int       `json:"pageSize,omitempty"`
		PageToken  string    `json:"pageToken,omitempty"`
	}{c.scope(req.Narrow), WithTrace(req.Query, req.TraceID), req.Severities, order, req.Limit, req.PageToken}
	var out struct {
		Logs          []wireLogRecord `json:"logs"`
		NextPageToken string          `json:"nextPageToken"`
	}
	if err := c.call(ctx, procSearchLogs, in, &out); err != nil {
		return LogsResult{}, err
	}
	res := LogsResult{NextPageToken: out.NextPageToken}
	for _, r := range out.Logs {
		res.Logs = append(res.Logs, r.record())
	}
	return res, nil
}

// ── traces ──────────────────────────────────────────────────────────────

type wireTraceSummary struct {
	TraceID        string    `json:"traceId"`
	StartTime      time.Time `json:"startTime"`
	DurationMs     float64   `json:"durationMs"`
	RootService    string    `json:"rootService"`
	RootSpanName   string    `json:"rootSpanName"`
	SpanCount      int       `json:"spanCount"`
	ErrorCount     int       `json:"errorCount"`
	Services       []string  `json:"services"`
	RootDurationMs float64   `json:"rootDurationMs"`
}

// wireRED is TraceRedSummary. int64 fields arrive as JSON strings (protojson).
type wireRED struct {
	TraceCount      jsonInt `json:"traceCount"`
	ErrorTraceCount jsonInt `json:"errorTraceCount"`
	P50Ms           float64 `json:"p50Ms"`
	P95Ms           float64 `json:"p95Ms"`
	P99Ms           float64 `json:"p99Ms"`
}

// Traces implements Backend over ListTraces. --slow is the control plane's
// DURATION_DESC order: the slowest first under span weighting, across every
// matching trace, not only the newest SlowWindow.
func (c Cloud) Traces(ctx context.Context, req TracesRequest) (TracesResult, error) {
	if req.Explain {
		res, err := c.explainer().Traces(ctx, req)
		res.Statements = explained(res.Statements)
		return res, err
	}
	in := struct {
		Scope         wireScope `json:"scope"`
		ErrorsOnly    bool      `json:"errorsOnly,omitempty"`
		MinDurationMs float64   `json:"minDurationMs,omitempty"`
		RootSpanName  string    `json:"rootSpanName,omitempty"`
		PageSize      int       `json:"pageSize,omitempty"`
		PageToken     string    `json:"pageToken,omitempty"`
		Order         string    `json:"order,omitempty"`
	}{Scope: c.scope(req.Narrow), ErrorsOnly: req.ErrorsOnly, MinDurationMs: req.MinDurationMs,
		RootSpanName: req.Route, PageSize: req.Limit, PageToken: req.PageToken}
	if req.Slow {
		in.Order = "TRACE_ORDER_DURATION_DESC"
	}
	var out struct {
		Traces        []wireTraceSummary `json:"traces"`
		NextPageToken string             `json:"nextPageToken"`
		RED           *wireRED           `json:"red"`
	}
	if err := c.call(ctx, procListTraces, in, &out); err != nil {
		return TracesResult{}, err
	}
	res := TracesResult{NextPageToken: out.NextPageToken}
	for _, t := range out.Traces {
		res.Traces = append(res.Traces, TraceSummary{
			TraceID: t.TraceID, Start: t.StartTime.UTC(), DurationMs: t.DurationMs, RootService: t.RootService,
			RootName: t.RootSpanName, RootDurationMs: t.RootDurationMs, Spans: t.SpanCount, Errors: t.ErrorCount,
			Weight: 1, Services: t.Services,
		})
	}
	if r := out.RED; r != nil {
		res.Summary = &REDSummary{Traces: uint64(r.TraceCount), ErrorTraces: uint64(r.ErrorTraceCount), P50Ms: r.P50Ms, P95Ms: r.P95Ms, P99Ms: r.P99Ms}
	}
	return res, nil
}

type wireSpanEvent struct {
	Time       time.Time         `json:"time"`
	Name       string            `json:"name"`
	Attributes map[string]string `json:"attributes"`
}

type wireSpan struct {
	SpanID        string            `json:"spanId"`
	ParentSpanID  string            `json:"parentSpanId"`
	Name          string            `json:"name"`
	Kind          string            `json:"kind"`
	Service       string            `json:"service"`
	StartTime     time.Time         `json:"startTime"`
	DurationNs    jsonInt           `json:"durationNs"`
	StatusCode    string            `json:"statusCode"`
	StatusMessage string            `json:"statusMessage"`
	Attributes    map[string]string `json:"attributes"`
	Events        []wireSpanEvent   `json:"events"`
}

// Trace implements Backend over GetTrace, and SearchLogs for the logs that
// carry the trace id.
func (c Cloud) Trace(ctx context.Context, req TraceRequest) (TraceResult, error) {
	if req.Explain {
		res, err := c.explainer().Trace(ctx, req)
		res.Statements = explained(res.Statements)
		return res, err
	}
	id, err := obsquery.NormalizeTraceID(req.TraceID)
	if err != nil {
		return TraceResult{}, compileErr(err)
	}
	in := struct {
		Scope   wireScope `json:"scope"`
		TraceID string    `json:"traceId"`
	}{c.scope(req.Narrow), id}
	var out struct {
		TraceID   string     `json:"traceId"`
		Spans     []wireSpan `json:"spans"`
		Truncated bool       `json:"truncated"`
	}
	if err := c.call(ctx, procGetTrace, in, &out); err != nil {
		return TraceResult{}, err
	}
	if len(out.Spans) == 0 {
		return TraceResult{}, Wrong("trace %s: no spans in %s on env %s", id, rangeText(req.Range), c.Env).
			With(Cmd("forge", "obs", "traces", c.Env, "--since", "24h"))
	}
	res := TraceResult{TraceID: id, SpansTruncated: out.Truncated}
	for _, s := range out.Spans {
		sp := Span{
			// Kind and status are spelled as the local backend spells them
			// ("server", "error"): the control plane answers "Server",
			// "Error", and a reader comparing one backend's output with the
			// other's must not see two vocabularies.
			SpanID: s.SpanID, ParentID: s.ParentSpanID, Name: s.Name, Kind: spanEnum(s.Kind, "SPAN_KIND_"), Service: s.Service,
			Start: s.StartTime.UTC(), DurationNs: int64(s.DurationNs), StatusCode: spanEnum(s.StatusCode, "STATUS_CODE_"),
			StatusMessage: s.StatusMessage, Attributes: s.Attributes,
		}
		for _, e := range s.Events {
			sp.Events = append(sp.Events, SpanEvent{Name: e.Name, Time: e.Time.UTC(), Attributes: e.Attributes})
		}
		res.Spans = append(res.Spans, sp)
	}
	if req.LogLimit > 0 {
		logs, err := c.Logs(ctx, LogsRequest{Narrow: Narrow{Range: req.Range, Services: req.Services, Filters: req.Filters},
			TraceID: id, Oldest: true, Limit: req.LogLimit})
		if err != nil {
			return res, err
		}
		res.Logs, res.LogsTruncated = logs.Logs, logs.NextPageToken != ""
	}
	return res, nil
}

// ── endpoints and panels ────────────────────────────────────────────────

type wirePredicate struct {
	Field  string   `json:"field"`
	Op     string   `json:"op"`
	Value  string   `json:"value,omitempty"`
	Values []string `json:"values,omitempty"`
}

type wirePanelSeries struct {
	Label      string          `json:"label,omitempty"`
	Agg        string          `json:"agg,omitempty"`
	Quantile   float64         `json:"quantile,omitempty"`
	Field      string          `json:"field,omitempty"`
	MetricType string          `json:"metricType,omitempty"`
	Where      []wirePredicate `json:"where,omitempty"`
}

type wirePanel struct {
	ID       string            `json:"id,omitempty"`
	Kind     string            `json:"kind"`
	Signal   string            `json:"signal,omitempty"`
	Series   []wirePanelSeries `json:"series,omitempty"`
	Ratio    bool              `json:"ratio,omitempty"`
	Where    []wirePredicate   `json:"where,omitempty"`
	GroupBy  []string          `json:"groupBy,omitempty"`
	Formula  string            `json:"formula,omitempty"`
	Limit    int               `json:"limit,omitempty"`
	SQL      string            `json:"sql,omitempty"`
	HasError bool              `json:"hasError,omitempty"`
	Funnel   *wireFunnel       `json:"funnel,omitempty"`
}

type wireFunnel struct {
	Steps  []string `json:"steps"`
	Window string   `json:"window,omitempty"`
	By     string   `json:"by,omitempty"`
}

func wirePreds(ps []obsquery.PanelPredicate) []wirePredicate {
	var out []wirePredicate
	for _, p := range ps {
		out = append(out, wirePredicate{Field: p.Field, Op: p.Op, Value: p.Value, Values: p.Values})
	}
	return out
}

// panelWire is p as the control plane's Panel message, for the verbs that
// print a table: a funnel has no table form yet, so they refuse it.
func panelWire(p obsquery.Panel) (wirePanel, error) {
	if p.Funnel != nil {
		return wirePanel{}, Wrong("a funnel panel cannot be sent from the CLI yet; run it in the console")
	}
	return panelWireAny(p)
}

// panelWireAny is p as the control plane's Panel message, a funnel included.
func panelWireAny(p obsquery.Panel) (wirePanel, error) {
	w := wirePanel{ID: p.ID, Kind: p.Kind, Signal: p.Signal, Ratio: p.Ratio, Where: wirePreds(p.Where),
		GroupBy: p.GroupBy, Formula: p.Formula, Limit: p.Limit, SQL: p.SQL, HasError: p.HasError}
	if f := p.Funnel; f != nil {
		w.Funnel = &wireFunnel{Steps: f.Steps, Window: f.Window, By: f.By}
	}
	for _, s := range p.Series {
		w.Series = append(w.Series, wirePanelSeries{Label: s.Label, Agg: s.Agg, Quantile: s.Quantile,
			Field: s.Field, MetricType: s.MetricType, Where: wirePreds(s.Where)})
	}
	return w, nil
}

type wireTimePoint struct {
	Time    time.Time `json:"time"`
	Value   float64   `json:"value"`
	Partial bool      `json:"partial"`
	Null    bool      `json:"null"`
}

type wirePanelSeriesResult struct {
	Label  string            `json:"label"`
	Group  map[string]string `json:"group"`
	Points []wireTimePoint   `json:"points"`
	Value  float64           `json:"value"`
}

// wireSeriesMeta is PanelSeriesMeta: what one entry of a metrics panel's series
// measures.
type wireSeriesMeta struct {
	Label       string  `json:"label"`
	Metric      string  `json:"metric"`
	MetricType  string  `json:"metricType"`
	Monotonic   bool    `json:"monotonic"`
	Temporality string  `json:"temporality"`
	Aggregation string  `json:"aggregation"`
	Quantile    float64 `json:"quantile"`
	ValueUnit   string  `json:"valueUnit"`
}

type wirePanelResponse struct {
	Columns []string `json:"columns"`
	Rows    []struct {
		Group               map[string]string `json:"group"`
		Values              []float64         `json:"values"`
		MissingValueIndexes []int             `json:"missingValueIndexes"`
	} `json:"rows"`
	Series []wirePanelSeriesResult `json:"series"`
	// The console kinds and the heatmap are read as counts only: the audit asks
	// whether there is anything to draw, not what.
	FunnelSteps []struct {
		Count jsonInt `json:"count"`
	} `json:"funnelSteps"`
	Issues     []json.RawMessage `json:"issues"`
	Sessions   []json.RawMessage `json:"sessions"`
	Heatmap    []json.RawMessage `json:"heatmap"`
	SQLColumns []struct {
		Name    string `json:"name"`
		Type    string `json:"type"`
		Numeric bool   `json:"numeric"`
		Time    bool   `json:"time"`
	} `json:"sqlColumns"`
	Truncated   bool `json:"truncated"`
	CompiledSQL []struct {
		SQL    string            `json:"sql"`
		Params map[string]string `json:"params"`
	} `json:"compiledSql"`
	StepSeconds int64            `json:"stepSeconds"`
	SeriesMeta  []wireSeriesMeta `json:"seriesMeta"`
	SeriesTotal int              `json:"seriesTotal"`
	Warnings    []string         `json:"warnings"`
	// Sent by control planes that report a sql panel's bucket; absent on older
	// ones, and never required.
	BucketSeconds  jsonInt `json:"bucketSeconds"`
	BucketTimezone string  `json:"bucketTimezone"`
}

func (c Cloud) queryPanel(ctx context.Context, n Narrow, p obsquery.Panel, step int64) (wirePanelResponse, error) {
	panel, err := panelWire(p)
	if err != nil {
		return wirePanelResponse{}, err
	}
	return c.queryPanelScoped(ctx, c.scope(n), panel, step, n.Explain)
}

func (c Cloud) queryPanelScoped(ctx context.Context, scope wireScope, panel wirePanel, step int64, explain bool) (wirePanelResponse, error) {
	in := struct {
		Scope       wireScope `json:"scope"`
		Panel       wirePanel `json:"panel"`
		StepSeconds int64     `json:"stepSeconds,omitempty"`
		Explain     bool      `json:"explain,omitempty"`
	}{scope, panel, step, explain}
	var out wirePanelResponse
	err := c.call(ctx, procQueryPanel, in, &out)
	return out, err
}

func compiledStatements(purpose string, r wirePanelResponse) []Statement {
	var st []Statement
	for _, s := range r.CompiledSQL {
		st = append(st, Statement{Purpose: purpose, SQL: s.SQL, Params: s.Params})
	}
	return st
}

// Endpoints implements Backend: the endpoints table panel, run by QueryPanel.
// The control plane weights spans for tail sampling, so counts are real
// request counts, not stored spans.
func (c Cloud) Endpoints(ctx context.Context, req EndpointsRequest) (EndpointsResult, error) {
	switch req.Sort {
	case "", "requests", "p95", "errors", "rate":
	default:
		return EndpointsResult{}, Wrong("--sort %q: use requests, p95, errors or rate", req.Sort)
	}
	r, err := c.queryPanel(ctx, req.Narrow, endpointsPanel(), 0)
	if err != nil {
		return EndpointsResult{}, err
	}
	res := EndpointsResult{Statements: compiledStatements("endpoints", r)}
	if req.Explain {
		return res, nil
	}
	secs := req.Range.To.Sub(req.Range.From).Seconds()
	for _, row := range r.Rows {
		if len(row.Values) < 5 {
			return res, Undetermined("the control plane returned %d values per endpoint, want 5", len(row.Values))
		}
		v := func(i int) float64 { return finite(row.Values[i]) }
		e := Endpoint{Service: row.Group["service.name"], Name: row.Group["span.name"],
			Requests: v(0), Errors: v(1), P50Ms: v(2), P95Ms: v(3), P99Ms: v(4)}
		if e.Requests > 0 {
			e.ErrorRatio = e.Errors / e.Requests
		}
		if secs > 0 {
			e.RatePerSecond = e.Requests / secs
		}
		res.Endpoints = append(res.Endpoints, e)
	}
	rankEndpoints(&res, req)
	return res, nil
}

// rankEndpoints sorts by req.Sort and applies req.Top.
func rankEndpoints(res *EndpointsResult, req EndpointsRequest) {
	key := map[string]func(Endpoint) float64{
		"":         func(e Endpoint) float64 { return e.Requests },
		"requests": func(e Endpoint) float64 { return e.Requests },
		"rate":     func(e Endpoint) float64 { return e.RatePerSecond },
		"p95":      func(e Endpoint) float64 { return e.P95Ms },
		"errors":   func(e Endpoint) float64 { return e.ErrorRatio },
	}[req.Sort]
	sort.SliceStable(res.Endpoints, func(i, j int) bool { return key(res.Endpoints[i]) > key(res.Endpoints[j]) })
	if req.Top > 0 && len(res.Endpoints) > req.Top {
		res.Omitted = len(res.Endpoints) - req.Top
		res.Endpoints = res.Endpoints[:req.Top]
	}
}

// Query implements Backend: a sql panel (raw SQL bound to the scope) or the
// given panel, run by QueryPanel.
func (c Cloud) Query(ctx context.Context, req QueryRequest) (QueryResult, error) {
	if (req.SQL == "") == (req.Panel == nil) {
		return QueryResult{}, Wrong("give exactly one of --sql and --panel")
	}
	if len(req.Params) > 0 {
		return QueryResult{}, Wrong("--param binds a parameter on the local backend only; the control plane binds only the scope parameters (%s)", ReservedParamList())
	}
	p := obsquery.Panel{ID: "cli", Kind: "sql", SQL: req.SQL}
	if req.Panel != nil {
		p = *req.Panel
	}
	if p.Signal == "metrics" && p.Kind != "sql" {
		return c.metricsPanelTable(ctx, req, p)
	}
	r, err := c.queryPanel(ctx, req.Narrow, p, req.StepSeconds)
	if err != nil {
		return QueryResult{}, err
	}
	res := QueryResult{Statements: compiledStatements("panel "+p.Kind, r), Truncated: r.Truncated}
	if req.Explain {
		return res, nil
	}
	res.Columns, res.Rows = flattenCloudPanel(r)
	if req.Limit > 0 && len(res.Rows) > req.Limit {
		res.Rows, res.Truncated = res.Rows[:req.Limit], true
	}
	return res, nil
}

// metricsPanelTable runs a metrics panel through MetricsQuery and flattens it
// the way the local backend does, so `forge obs query --panel` reads the same
// table on both.
func (c Cloud) metricsPanelTable(ctx context.Context, req QueryRequest, p obsquery.Panel) (QueryResult, error) {
	mr, err := c.MetricsQuery(ctx, MetricsQueryRequest{Narrow: req.Narrow, Panel: p, StepSeconds: req.StepSeconds})
	res := QueryResult{Statements: mr.Statements, Truncated: mr.Truncated}
	if err != nil || req.Explain {
		return res, err
	}
	res.Columns, res.Rows = flattenMetrics(mr)
	if req.Limit > 0 && len(res.Rows) > req.Limit {
		res.Rows, res.Truncated = res.Rows[:req.Limit], true
	}
	return res, nil
}

// flattenCloudPanel turns a QueryPanel response into a table: a sql panel
// that came back as a table in its own column order (the control plane splits
// a row into its non-numeric group and its numeric values); a table panel's
// group columns then its values; a series panel (and a sql panel whose first
// column is a time) one row per point.
func flattenCloudPanel(r wirePanelResponse) ([]Column, [][]any) {
	switch {
	case len(r.SQLColumns) > 0 && len(r.Series) == 0:
		var cols []Column
		for _, c := range r.SQLColumns {
			cols = append(cols, Column{Name: c.Name, Type: c.Type})
		}
		var rows [][]any
		for _, row := range r.Rows {
			out := make([]any, 0, len(cols))
			next := 0
			for _, c := range r.SQLColumns {
				if !c.Numeric {
					out = append(out, row.Group[c.Name])
					continue
				}
				var v any
				if next < len(row.Values) {
					v = finite(row.Values[next])
				}
				next++
				out = append(out, v)
			}
			rows = append(rows, out)
		}
		return cols, rows
	case len(r.Rows) > 0:
		groups := groupKeys(len(r.Rows), func(i int) map[string]string { return r.Rows[i].Group })
		var cols []Column
		for _, g := range groups {
			cols = append(cols, Column{Name: g, Type: "String"})
		}
		for _, c := range r.Columns {
			cols = append(cols, Column{Name: c, Type: "Float64"})
		}
		var rows [][]any
		for _, row := range r.Rows {
			out := make([]any, 0, len(cols))
			for _, g := range groups {
				out = append(out, row.Group[g])
			}
			for _, v := range row.Values {
				out = append(out, finite(v))
			}
			rows = append(rows, out)
		}
		return cols, rows
	}
	groups := groupKeys(len(r.Series), func(i int) map[string]string { return r.Series[i].Group })
	cols := []Column{{Name: "series", Type: "String"}}
	for _, g := range groups {
		cols = append(cols, Column{Name: g, Type: "String"})
	}
	cols = append(cols, Column{Name: "time", Type: "DateTime"}, Column{Name: "value", Type: "Float64"})
	var rows [][]any
	for _, s := range r.Series {
		prefix := []any{s.Label}
		for _, g := range groups {
			prefix = append(prefix, s.Group[g])
		}
		if len(s.Points) == 0 {
			rows = append(rows, append(append([]any{}, prefix...), "", finite(s.Value)))
			continue
		}
		for _, p := range s.Points {
			rows = append(rows, append(append([]any{}, prefix...), p.Time.UTC().Format(time.RFC3339), finite(p.Value)))
		}
	}
	return cols, rows
}

func groupKeys(n int, at func(int) map[string]string) []string {
	set := map[string]bool{}
	for i := 0; i < n; i++ {
		for k := range at(i) {
			set[k] = true
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ── metrics ─────────────────────────────────────────────────────────────

var wireMetricTypes = map[string]string{
	"METRIC_TYPE_GAUGE": "gauge", "METRIC_TYPE_SUM": "sum", "METRIC_TYPE_HISTOGRAM": "histogram",
	"METRIC_TYPE_EXPONENTIAL_HISTOGRAM": "exp_histogram", "METRIC_TYPE_SUMMARY": "summary",
}

var wireTemporalities = map[string]string{
	"METRIC_TEMPORALITY_DELTA": "delta", "METRIC_TEMPORALITY_CUMULATIVE": "cumulative",
}

// wireMetricInfo is MetricInfo; int64 fields arrive as strings (protojson).
type wireMetricInfo struct {
	Name           string    `json:"name"`
	Type           string    `json:"type"`
	Unit           string    `json:"unit"`
	Description    string    `json:"description"`
	Monotonic      bool      `json:"monotonic"`
	Temporality    string    `json:"temporality"`
	Services       []string  `json:"services"`
	ServiceCount   int       `json:"serviceCount"`
	LastSeen       time.Time `json:"lastSeen"`
	SeriesEstimate jsonInt   `json:"seriesEstimate"`
	Queryable      bool      `json:"queryable"`
	Builtin        bool      `json:"builtin"`
	Declared       bool      `json:"declared"`
	Seen           bool      `json:"seen"`
	Stability      string    `json:"stability"`
	DeclaredAttrs  []struct {
		Key    string   `json:"key"`
		Values []string `json:"values"`
	} `json:"declaredAttributes"`
}

func (w wireMetricInfo) info() MetricInfo {
	mi := MetricInfo{
		Name: w.Name, Unit: w.Unit, Description: w.Description, Type: wireMetricTypes[w.Type], Monotonic: w.Monotonic,
		Temporality: wireTemporalities[w.Temporality], Services: w.Services, ServiceCount: w.ServiceCount,
		SeriesEstimate: int64(w.SeriesEstimate), Queryable: w.Queryable, Builtin: w.Builtin, Declared: w.Declared,
		Seen: w.Seen, Stability: w.Stability,
	}
	if !w.LastSeen.IsZero() {
		mi.LastSeen = w.LastSeen.UTC()
	}
	for _, a := range w.DeclaredAttrs {
		mi.DeclaredAttributes = append(mi.DeclaredAttributes, MetricAttributeSpec{Key: a.Key, Values: a.Values})
	}
	return mi
}

// MetricsList implements Backend over ListMetrics.
func (c Cloud) MetricsList(ctx context.Context, req MetricsListRequest) (MetricsListResult, error) {
	if req.Explain {
		res, err := c.explainer().MetricsList(ctx, req)
		res.Statements = explained(res.Statements)
		return res, err
	}
	in := struct {
		Scope        wireScope `json:"scope"`
		NameContains string    `json:"nameContains,omitempty"`
		Limit        int       `json:"limit,omitempty"`
	}{c.scope(req.Narrow), req.NameContains, req.Limit}
	if req.NoEnv {
		in.Scope.EnvIDs = nil
	}
	var out struct {
		Metrics   []wireMetricInfo `json:"metrics"`
		Truncated bool             `json:"truncated"`
	}
	if err := c.call(ctx, procListMetrics, in, &out); err != nil {
		return MetricsListResult{}, err
	}
	res := MetricsListResult{Truncated: out.Truncated}
	for _, m := range out.Metrics {
		res.Metrics = append(res.Metrics, m.info())
	}
	return res, nil
}

// MetricsQuery implements Backend over QueryPanel: the control plane runs the
// same obsquery.EvalMetricPanel the local backend does, so the same panel over
// the same data returns the same series.
func (c Cloud) MetricsQuery(ctx context.Context, req MetricsQueryRequest) (MetricsQueryResult, error) {
	if req.Explain {
		res, err := c.explainer().MetricsQuery(ctx, req)
		res.Statements = explained(res.Statements)
		return res, err
	}
	return metricsWithCompare(req, func(r MetricsQueryRequest) (MetricsQueryResult, error) { return c.queryMetricPanel(ctx, r) })
}

func (c Cloud) queryMetricPanel(ctx context.Context, req MetricsQueryRequest) (MetricsQueryResult, error) {
	p := req.Panel
	p.Signal = "metrics"
	r, err := c.queryPanel(ctx, req.Narrow, p, req.StepSeconds)
	if err != nil {
		return MetricsQueryResult{}, err
	}
	res := MetricsQueryResult{
		SeriesTotal: r.SeriesTotal, Truncated: r.Truncated, StepSeconds: r.StepSeconds, Warnings: r.Warnings,
		Statements: compiledStatements("metric panel", r),
	}
	for _, m := range r.SeriesMeta {
		agg, mi := m.Aggregation, MetricMeta{
			Label: m.Label, Metric: m.Metric, Type: wireMetricTypes[m.MetricType], Monotonic: m.Monotonic,
			Temporality: wireTemporalities[m.Temporality], Quantile: m.Quantile, ValueUnit: m.ValueUnit,
		}
		mi.Agg = agg
		res.Meta = append(res.Meta, mi)
	}
	switch p.Kind {
	case "table", "bar":
		// One entry per (group, column): a column is a panel series, in order.
		for ci, col := range r.Columns {
			for _, row := range r.Rows {
				if ci >= len(row.Values) {
					continue
				}
				v := finite(row.Values[ci])
				res.Series = append(res.Series, MetricSeries{Label: col, Labels: row.Group, Reduced: &v})
			}
		}
	case "number":
		for _, s := range r.Series {
			v := finite(s.Value)
			ms := MetricSeries{Label: s.Label, Labels: s.Group, Reduced: &v}
			for _, pt := range s.Points {
				ms.Points = append(ms.Points, MetricPoint{Time: pt.Time.UTC(), Value: finite(pt.Value), Null: pt.Null, Partial: pt.Partial})
			}
			res.Series = append(res.Series, ms)
		}
	default:
		for _, s := range r.Series {
			ms := MetricSeries{Label: s.Label, Labels: s.Group}
			for _, pt := range s.Points {
				ms.Points = append(ms.Points, MetricPoint{Time: pt.Time.UTC(), Value: finite(pt.Value), Null: pt.Null, Partial: pt.Partial})
			}
			res.Series = append(res.Series, ms)
		}
	}
	var cut bool
	if res.Series, cut = capMetricSeries(res.Series, req.MaxSeries); cut {
		res.Truncated = true
	}
	return res, nil
}

// MetricsDescribe implements Backend over GetMetric.
func (c Cloud) MetricsDescribe(ctx context.Context, req MetricsDescribeRequest) (MetricsDescribeResult, error) {
	if req.Explain {
		res, err := c.explainer().MetricsDescribe(ctx, req)
		res.Statements = explained(res.Statements)
		return res, err
	}
	in := struct {
		Scope      wireScope `json:"scope"`
		MetricName string    `json:"metricName"`
	}{c.scope(req.Narrow), req.Name}
	var out struct {
		Info             wireMetricInfo `json:"info"`
		IntervalSeconds  int            `json:"intervalSeconds"`
		MixedTemporality bool           `json:"mixedTemporality"`
		Attributes       []struct {
			Key              string   `json:"key"`
			Resource         bool     `json:"resource"`
			DistinctEstimate jsonInt  `json:"distinctEstimate"`
			Enumerated       []string `json:"enumerated"`
			HighCardinality  bool     `json:"highCardinality"`
		} `json:"attributes"`
		SuggestedBreakdowns []string `json:"suggestedBreakdowns"`
	}
	if err := c.call(ctx, procGetMetric, in, &out); err != nil {
		return MetricsDescribeResult{}, err
	}
	res := MetricsDescribeResult{
		Info: out.Info.info(), IntervalSeconds: int64(out.IntervalSeconds), MixedTemporality: out.MixedTemporality,
		SuggestedBreakdowns: out.SuggestedBreakdowns,
	}
	if res.Info.Name == "" {
		res.Info.Name = req.Name
	}
	for _, a := range out.Attributes {
		res.Attributes = append(res.Attributes, MetricAttribute{Key: a.Key, Resource: a.Resource, DistinctEstimate: int64(a.DistinctEstimate), Enumerated: a.Enumerated, HighCardinality: a.HighCardinality})
	}
	return res, nil
}

// MetricsExemplars implements Backend over ListMetricExemplars.
func (c Cloud) MetricsExemplars(ctx context.Context, req MetricsExemplarsRequest) (MetricsExemplarsResult, error) {
	if req.Explain {
		res, err := c.explainer().MetricsExemplars(ctx, req)
		res.Statements = explained(res.Statements)
		return res, err
	}
	in := struct {
		Scope      wireScope         `json:"scope"`
		MetricName string            `json:"metricName"`
		Where      []wirePredicate   `json:"where,omitempty"`
		Group      map[string]string `json:"group,omitempty"`
		TimeRange  *wireTimeRange    `json:"timeRange,omitempty"`
		MinValue   *float64          `json:"minValue,omitempty"`
		Limit      int               `json:"limit,omitempty"`
	}{Scope: c.scope(req.Narrow), MetricName: req.Name, Where: wirePreds(req.Where), Group: req.Group, MinValue: req.MinValue, Limit: req.Limit}
	if !req.From.IsZero() {
		in.TimeRange = &wireTimeRange{Start: req.From.UTC(), End: req.To.UTC()}
	}
	var out struct {
		Exemplars []struct {
			Time        time.Time         `json:"time"`
			Value       float64           `json:"value"`
			TraceID     string            `json:"traceId"`
			SpanID      string            `json:"spanId"`
			Attributes  map[string]string `json:"attributes"`
			TraceStored bool              `json:"traceStored"`
		} `json:"exemplars"`
	}
	if err := c.call(ctx, procListMetricExemplars, in, &out); err != nil {
		return MetricsExemplarsResult{}, err
	}
	var res MetricsExemplarsResult
	for _, e := range out.Exemplars {
		res.Exemplars = append(res.Exemplars, MetricExemplar{Time: e.Time.UTC(), Value: finite(e.Value), TraceID: strings.ToLower(e.TraceID), SpanID: e.SpanID, Attributes: e.Attributes, TraceStored: e.TraceStored})
	}
	return res, nil
}

// jsonInt reads an int64 field, which protojson writes as a string.
type jsonInt int64

func (j *jsonInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*j = 0
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		f, ferr := strconv.ParseFloat(s, 64)
		if ferr != nil || math.IsNaN(f) {
			return err
		}
		n = int64(f)
	}
	*j = jsonInt(n)
	return nil
}

// spanEnum is an OTLP enum value as the local ClickStack's rows spell it:
// lower case, without its STATUS_CODE_ / SPAN_KIND_ prefix.
func spanEnum(v, prefix string) string {
	return strings.ToLower(strings.TrimPrefix(strings.ToUpper(v), prefix))
}
