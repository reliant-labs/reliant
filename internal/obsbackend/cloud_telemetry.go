package obsbackend

import (
	"context"
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
			SpanID: s.SpanID, ParentID: s.ParentSpanID, Name: s.Name, Kind: s.Kind, Service: s.Service,
			Start: s.StartTime.UTC(), DurationNs: int64(s.DurationNs), StatusCode: s.StatusCode,
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
}

func wirePreds(ps []obsquery.PanelPredicate) []wirePredicate {
	var out []wirePredicate
	for _, p := range ps {
		out = append(out, wirePredicate{Field: p.Field, Op: p.Op, Value: p.Value, Values: p.Values})
	}
	return out
}

// panelWire is p as the control plane's Panel message.
func panelWire(p obsquery.Panel) (wirePanel, error) {
	if p.Funnel != nil {
		return wirePanel{}, Wrong("a funnel panel cannot be sent from the CLI yet; run it in the console")
	}
	w := wirePanel{ID: p.ID, Kind: p.Kind, Signal: p.Signal, Ratio: p.Ratio, Where: wirePreds(p.Where),
		GroupBy: p.GroupBy, Formula: p.Formula, Limit: p.Limit, SQL: p.SQL, HasError: p.HasError}
	for _, s := range p.Series {
		w.Series = append(w.Series, wirePanelSeries{Label: s.Label, Agg: s.Agg, Quantile: s.Quantile,
			Field: s.Field, MetricType: s.MetricType, Where: wirePreds(s.Where)})
	}
	return w, nil
}

type wirePanelResponse struct {
	Columns []string `json:"columns"`
	Rows    []struct {
		Group  map[string]string `json:"group"`
		Values []float64         `json:"values"`
	} `json:"rows"`
	Series []struct {
		Label  string            `json:"label"`
		Group  map[string]string `json:"group"`
		Points []struct {
			Time  time.Time `json:"time"`
			Value float64   `json:"value"`
		} `json:"points"`
		Value float64 `json:"value"`
	} `json:"series"`
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
}

func (c Cloud) queryPanel(ctx context.Context, n Narrow, p obsquery.Panel, step int64) (wirePanelResponse, error) {
	panel, err := panelWire(p)
	if err != nil {
		return wirePanelResponse{}, err
	}
	in := struct {
		Scope       wireScope `json:"scope"`
		Panel       wirePanel `json:"panel"`
		StepSeconds int64     `json:"stepSeconds,omitempty"`
		Explain     bool      `json:"explain,omitempty"`
	}{c.scope(n), panel, step, n.Explain}
	var out wirePanelResponse
	err = c.call(ctx, procQueryPanel, in, &out)
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
	var out struct {
		Metrics []struct {
			Name        string `json:"name"`
			Type        string `json:"type"`
			Unit        string `json:"unit"`
			Description string `json:"description"`
		} `json:"metrics"`
		Truncated bool `json:"truncated"`
	}
	if err := c.call(ctx, procListMetrics, in, &out); err != nil {
		return MetricsListResult{}, err
	}
	res := MetricsListResult{Truncated: out.Truncated}
	for _, m := range out.Metrics {
		res.Metrics = append(res.Metrics, MetricInfo{Name: m.Name, Unit: m.Unit, Description: m.Description, Type: wireMetricTypes[m.Type]})
	}
	return res, nil
}

// MetricsQuery implements Backend over QueryMetric.
func (c Cloud) MetricsQuery(ctx context.Context, req MetricsQueryRequest) (MetricsQueryResult, error) {
	if _, ok := aggNames[req.Agg]; !ok {
		return MetricsQueryResult{}, Wrong("--agg %q: use one of %s", req.Agg, AggNames)
	}
	if req.Explain {
		res, err := c.explainer().MetricsQuery(ctx, req)
		res.Statements = explained(res.Statements)
		return res, err
	}
	in := struct {
		Scope       wireScope `json:"scope"`
		MetricName  string    `json:"metricName"`
		Aggregation string    `json:"aggregation,omitempty"`
		GroupBy     []string  `json:"groupBy,omitempty"`
		StepSeconds int64     `json:"stepSeconds,omitempty"`
	}{Scope: c.scope(req.Narrow), MetricName: req.Name, GroupBy: req.GroupBy, StepSeconds: req.StepSeconds}
	if req.Agg != "" {
		in.Aggregation = "METRIC_AGGREGATION_" + strings.ToUpper(req.Agg)
	}
	var out struct {
		Series []struct {
			Labels map[string]string `json:"labels"`
			Points []struct {
				Time  time.Time `json:"time"`
				Value float64   `json:"value"`
			} `json:"points"`
		} `json:"series"`
		StepSeconds     int64  `json:"stepSeconds"`
		MetricType      string `json:"metricType"`
		Unit            string `json:"unit"`
		Aggregation     string `json:"aggregation"`
		SeriesTruncated bool   `json:"seriesTruncated"`
	}
	if err := c.call(ctx, procQueryMetric, in, &out); err != nil {
		return MetricsQueryResult{}, err
	}
	res := MetricsQueryResult{
		Name: req.Name, Type: wireMetricTypes[out.MetricType], Unit: out.Unit, StepSeconds: out.StepSeconds,
		Agg: strings.ToLower(strings.TrimPrefix(out.Aggregation, "METRIC_AGGREGATION_")), Truncated: out.SeriesTruncated,
	}
	for _, s := range out.Series {
		ser := Series{Labels: s.Labels}
		for _, p := range s.Points {
			ser.Points = append(ser.Points, Point{Time: p.Time.UTC(), Value: finite(p.Value)})
		}
		res.Series = append(res.Series, ser)
	}
	if req.MaxSeries > 0 {
		res.Series, res.SeriesOmitted = capSeries(res.Series, req.MaxSeries)
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
