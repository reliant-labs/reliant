package obsbackend

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/dashspec"
	"github.com/reliant-labs/forge/pkg/observe/metrics"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

// Local is the backend over the project's own ClickStack. Every verb compiles
// with pkg/obsquery against the `default` database (obsquery.LocalDatabase)
// and runs the statement through Runner.
type Local struct {
	Env    string
	Runner Runner
	// Ledger opens this machine's release ledger, for `forge obs changes`.
	// Nil when the caller has none; a read that needs it then fails with a
	// remedy rather than a panic.
	Ledger func() (LedgerReader, error)
	// Declared reads the metrics schemas this project declares
	// (bin/observability/metrics-schema.json), for `forge obs metrics list`
	// and `describe`. Nil when the caller has none.
	Declared func() []metrics.Schema
}

// Info implements Backend.
func (l Local) Info() Info { return Info{Env: l.Env, Backend: BackendLocal} }

func (l Local) run(ctx context.Context, st obsquery.Stmt, lim Limits) (Result, error) {
	return l.Runner.Run(ctx, st, lim)
}

// compileErr maps a compiler refusal to a request error (exit 1).
func compileErr(err error) error {
	if err == nil {
		return nil
	}
	return Wrong("%s", strings.TrimPrefix(err.Error(), obsquery.ErrInvalid.Error()+": "))
}

func stmt(purpose string, st obsquery.Stmt) Statement {
	return Statement{Purpose: purpose, SQL: st.SQL, Params: st.Params}
}

// ── logs ────────────────────────────────────────────────────────────────

// Logs implements Backend.
func (l Local) Logs(ctx context.Context, req LogsRequest) (LogsResult, error) {
	cursor, err := obsquery.DecodeLogCursor(req.PageToken)
	if err != nil {
		return LogsResult{}, compileErr(err)
	}
	q := obsquery.LogQuery{
		Scope:      LocalScope(req.Narrow, l.Env),
		Query:      WithTrace(req.Query, req.TraceID),
		Severities: req.Severities,
		Oldest:     req.Oldest,
		Limit:      req.Limit,
		Cursor:     cursor,
	}
	st, err := q.Compile()
	if err != nil {
		return LogsResult{}, compileErr(err)
	}
	res := LogsResult{Statements: []Statement{stmt("logs", st)}}
	if req.Explain {
		return res, nil
	}
	r, err := l.run(ctx, st, DefaultLimits())
	if err != nil {
		return res, err
	}
	var lastTS time.Time
	var lastHash uint64
	for _, row := range r.Rows {
		if len(row) < 10 {
			return res, Undetermined("the local ClickStack returned %d columns for logs, want 10", len(row))
		}
		if len(res.Logs) == req.Limit { // the one extra row: there is a next page
			res.NextPageToken = q.NextCursor(lastTS, lastHash)
			break
		}
		rec := logRecord(row)
		res.Logs = append(res.Logs, rec)
		lastTS, lastHash = rec.Time, rec.Hash
	}
	return res, nil
}

func logRecord(row []any) LogRecord {
	return LogRecord{
		Time:           AsTime(row[0]),
		Severity:       AsString(row[1]),
		SeverityNumber: int(AsInt(row[2])),
		Service:        AsString(row[3]),
		Body:           AsString(row[4]),
		TraceID:        AsString(row[5]),
		SpanID:         AsString(row[6]),
		Attributes:     AsStringMap(row[7]),
		Hash:           AsUint(row[9]),
	}
}

// WithTrace narrows a log query to one trace. The query is parenthesised so an
// OR in it cannot escape the trace id (AND binds tighter than OR).
func WithTrace(query, traceID string) string {
	if traceID == "" {
		return query
	}
	t := "trace_id:" + strings.ToLower(traceID)
	if strings.TrimSpace(query) == "" {
		return t
	}
	return t + " (" + query + ")"
}

// ── traces ──────────────────────────────────────────────────────────────

// Traces implements Backend.
func (l Local) Traces(ctx context.Context, req TracesRequest) (TracesResult, error) {
	cursor, err := obsquery.DecodeTraceCursor(req.PageToken)
	if err != nil {
		return TracesResult{}, compileErr(err)
	}
	limit := req.Limit
	if req.Slow {
		if req.PageToken != "" {
			return TracesResult{}, Wrong("--slow sorts a fixed window and has no next page")
		}
		limit = SlowWindow
	}
	q := obsquery.TraceQuery{
		Scope:         LocalScope(req.Narrow, l.Env),
		ErrorsOnly:    req.ErrorsOnly,
		MinDurationMs: req.MinDurationMs,
		RootSpanName:  req.Route,
		Limit:         limit,
		Cursor:        cursor,
	}
	st, err := q.Compile()
	if err != nil {
		return TracesResult{}, compileErr(err)
	}
	res := TracesResult{Statements: []Statement{stmt("traces", st)}}
	red, err := q.CompileRED()
	if err != nil {
		return res, compileErr(err)
	}
	if req.PageToken == "" {
		res.Statements = append(res.Statements, stmt("traces summary (rate, errors, duration)", red))
	}
	if req.Explain {
		return res, nil
	}
	r, err := l.run(ctx, st, DefaultLimits())
	if err != nil {
		return res, err
	}
	var last TraceSummary
	for _, row := range r.Rows {
		if len(row) < 11 {
			return res, Undetermined("the local ClickStack returned %d columns for traces, want 11", len(row))
		}
		if len(res.Traces) == limit {
			if !req.Slow {
				res.NextPageToken = q.NextCursor(last.Start, last.TraceID)
			}
			break
		}
		last = traceSummary(row)
		res.Traces = append(res.Traces, last)
	}
	if req.Slow {
		res.SortedFrom = len(res.Traces)
		sort.SliceStable(res.Traces, func(i, j int) bool { return res.Traces[i].DurationMs > res.Traces[j].DurationMs })
		if len(res.Traces) > req.Limit {
			res.Traces = res.Traces[:req.Limit]
		}
	}
	if req.PageToken == "" {
		rr, err := l.run(ctx, red, DefaultLimits())
		if err != nil {
			return res, err
		}
		if len(rr.Rows) == 1 && len(rr.Rows[0]) >= 3 {
			qs := AsFloats(rr.Rows[0][2])
			s := &REDSummary{Traces: AsUint(rr.Rows[0][0]), ErrorTraces: AsUint(rr.Rows[0][1])}
			if len(qs) == 3 {
				s.P50Ms, s.P95Ms, s.P99Ms = finite(qs[0]), finite(qs[1]), finite(qs[2])
			}
			if s.Traces > 0 {
				res.Summary = s
			}
		}
	}
	return res, nil
}

func traceSummary(row []any) TraceSummary {
	return TraceSummary{
		TraceID:        AsString(row[0]),
		Start:          AsTime(row[1]),
		DurationMs:     finite(AsFloat(row[2])),
		RootService:    AsString(row[3]),
		RootName:       AsString(row[4]),
		RootDurationMs: finite(AsFloat(row[5])),
		Spans:          int(AsInt(row[6])),
		Errors:         int(AsInt(row[7])),
		Weight:         math.Max(finite(AsFloat(row[8])), 1),
		Services:       AsStrings(row[9]),
	}
}

// Trace implements Backend.
func (l Local) Trace(ctx context.Context, req TraceRequest) (TraceResult, error) {
	id, err := obsquery.NormalizeTraceID(req.TraceID)
	if err != nil {
		return TraceResult{}, compileErr(err)
	}
	span := obsquery.TraceSpans{Scope: obsquery.Scope{Database: obsquery.LocalDatabase, Local: true, Start: req.Range.From, End: req.Range.To}, TraceID: id}
	st, err := span.Compile()
	if err != nil {
		return TraceResult{}, compileErr(err)
	}
	res := TraceResult{TraceID: id, Statements: []Statement{stmt("trace spans", st)}}
	logLimit := req.LogLimit
	var lq obsquery.LogQuery
	var lst obsquery.Stmt
	if logLimit > 0 {
		lq = obsquery.LogQuery{
			Scope:  obsquery.Scope{Database: obsquery.LocalDatabase, Local: true, Start: req.Range.From, End: req.Range.To},
			Query:  WithTrace("", id),
			Oldest: true,
			Limit:  logLimit,
		}
		if lst, err = lq.Compile(); err != nil {
			return res, compileErr(err)
		}
		res.Statements = append(res.Statements, stmt("trace logs", lst))
	}
	if req.Explain {
		return res, nil
	}
	r, err := l.run(ctx, st, DefaultLimits())
	if err != nil {
		return res, err
	}
	for _, row := range r.Rows {
		if len(row) < 16 {
			return res, Undetermined("the local ClickStack returned %d columns for a span, want 16", len(row))
		}
		if len(res.Spans) == obsquery.MaxSpansPerTrace {
			res.SpansTruncated = true
			break
		}
		res.Spans = append(res.Spans, spanOf(row))
	}
	if len(res.Spans) == 0 {
		return res, Wrong("no trace %s in the local ClickStack for %s — traces are kept 72h, and a trace is only stored once its spans have been exported", id, rangeText(req.Range)).
			With(Cmd("forge", "obs", "traces", l.Env, "--since", "1h"))
	}
	if logLimit > 0 {
		lr, err := l.run(ctx, lst, DefaultLimits())
		if err != nil {
			return res, err
		}
		for _, row := range lr.Rows {
			if len(row) < 10 {
				break
			}
			if len(res.Logs) == logLimit {
				res.LogsTruncated = true
				break
			}
			res.Logs = append(res.Logs, logRecord(row))
		}
	}
	return res, nil
}

func spanOf(row []any) Span {
	sp := Span{
		Start:         AsTime(row[0]),
		SpanID:        AsString(row[1]),
		ParentID:      AsString(row[2]),
		Name:          AsString(row[3]),
		Kind:          strings.ToLower(strings.TrimPrefix(AsString(row[4]), "SPAN_KIND_")),
		Service:       AsString(row[5]),
		DurationNs:    int64(AsUint(row[6])),
		StatusCode:    strings.ToLower(strings.TrimPrefix(AsString(row[7]), "STATUS_CODE_")),
		StatusMessage: AsString(row[8]),
		Attributes:    AsStringMap(row[9]),
	}
	times, names, attrs := AsAny(row[11]), AsStrings(row[12]), AsAny(row[13])
	for i, n := range names {
		ev := SpanEvent{Name: n}
		if i < len(times) {
			ev.Time = AsTime(times[i])
		}
		if i < len(attrs) {
			ev.Attributes = AsStringMap(attrs[i])
		}
		sp.Events = append(sp.Events, ev)
	}
	return sp
}

func rangeText(r Range) string {
	return fmt.Sprintf("%s .. %s", r.From.UTC().Format(time.RFC3339), r.To.UTC().Format(time.RFC3339))
}

// ── endpoints ───────────────────────────────────────────────────────────

// endpointsPanel is the QueryPanel table the hosted backend runs for
// `endpoints`: server spans grouped by service and span name.
func endpointsPanel() obsquery.Panel {
	return obsquery.Panel{
		ID: "endpoints", Kind: "table", Signal: "traces",
		Where: []obsquery.PanelPredicate{{Field: "span.kind", Op: "eq", Value: "server"}},
		Series: []obsquery.PanelSeries{
			{Label: "requests", Agg: "count"},
			{Label: "errors", Agg: "count", Where: []obsquery.PanelPredicate{{Field: "span.status", Op: "eq", Value: "error"}}},
			{Label: "p50", Agg: "quantile", Quantile: 0.5, Field: "span.duration"},
			{Label: "p95", Agg: "quantile", Quantile: 0.95, Field: "span.duration"},
			{Label: "p99", Agg: "quantile", Quantile: 0.99, Field: "span.duration"},
		},
		GroupBy: []string{"service.name", "span.name"},
		Limit:   obsquery.MaxTableRows,
	}
}

// Endpoints implements Backend. The table is the busiest MaxTableRows
// endpoints; a different sort re-ranks within them.
func (l Local) Endpoints(ctx context.Context, req EndpointsRequest) (EndpointsResult, error) {
	switch req.Sort {
	case "", "requests", "p95", "errors", "rate":
	default:
		return EndpointsResult{}, Wrong("--sort %q: use requests, p95, errors or rate", req.Sort)
	}
	q := obsquery.PanelQuery{Scope: LocalScope(req.Narrow, l.Env), Panel: endpointsPanel()}
	st, shape, err := q.Compile()
	if err != nil {
		return EndpointsResult{}, compileErr(err)
	}
	res := EndpointsResult{Statements: []Statement{stmt("endpoints", st)}}
	if req.Explain {
		return res, nil
	}
	r, err := l.run(ctx, st, DefaultLimits())
	if err != nil {
		return res, err
	}
	secs := req.Range.To.Sub(req.Range.From).Seconds()
	for _, row := range r.Rows {
		if len(row) < 1+shape.Series {
			return res, Undetermined("the local ClickStack returned %d columns for endpoints, want %d", len(row), 1+shape.Series)
		}
		if len(res.Endpoints) == shape.Rows {
			break
		}
		labels := AsStrings(row[0])
		if len(labels) < 2 {
			continue
		}
		v := func(i int) float64 { return finite(AsFloat(row[1+i])) }
		e := Endpoint{Service: labels[0], Name: labels[1], Requests: v(0), Errors: v(1), P50Ms: v(2), P95Ms: v(3), P99Ms: v(4)}
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

// ── metrics ─────────────────────────────────────────────────────────────

// metricLimits bound a metrics statement: the evaluator's own row cap, as a
// partial result rather than a failure (it reports the cut as truncated).
func metricLimits() Limits {
	return Limits{Timeout: 30 * time.Second, MaxRows: obsquery.MaxMetricRows, Break: true}
}

// evalRunner is the obsquery.Runner over this backend's ClickHouse runner.
func (l Local) evalRunner() obsquery.Runner {
	return func(ctx context.Context, st obsquery.Stmt) (obsquery.Rows, error) {
		r, err := l.Runner.Run(ctx, st, metricLimits())
		if err != nil {
			return nil, err
		}
		return RowsOf(r), nil
	}
}

// declared reads the metrics schemas the project declares (none when the
// caller has none).
func (l Local) declared() []metrics.Schema {
	if l.Declared == nil {
		return nil
	}
	return l.Declared()
}

// evalErr maps an evaluator failure to a backend error: a refused request is
// exit 1, and a failure of the runner keeps its own status.
func (l Local) evalErr(err error) error {
	var be *Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &be):
		return err
	case errors.Is(err, obsquery.ErrInvalid):
		return Wrong("%s", strings.ReplaceAll(err.Error(), obsquery.ErrInvalid.Error()+": ", ""))
	}
	return Wrong("%s", err.Error())
}

// MetricsList implements Backend: the catalog over every metric kind, joined
// with the declared schemas this machine has.
func (l Local) MetricsList(ctx context.Context, req MetricsListRequest) (MetricsListResult, error) {
	env := l.Env
	if req.NoEnv {
		env = ""
	}
	q := obsquery.MetricCatalog{Scope: LocalScope(req.Narrow, env), NameContains: req.NameContains, Limit: req.Limit}
	st, err := q.Compile()
	if err != nil {
		return MetricsListResult{}, compileErr(err)
	}
	res := MetricsListResult{Statements: []Statement{stmt("metric catalog", st)}}
	if req.Explain {
		return res, nil
	}
	r, err := l.run(ctx, st, DefaultLimits())
	if err != nil {
		return res, err
	}
	entries, truncated, err := obsquery.ParseCatalog(RowsOf(r), req.Limit)
	if err != nil {
		return res, Undetermined("the local ClickStack returned a catalog forge cannot read").Cause(err)
	}
	res.Truncated = truncated
	res.Metrics = catalogInfos(l.mergeDeclared(entries, req.NameContains))
	if req.Limit > 0 && len(res.Metrics) > req.Limit {
		res.Metrics, res.Truncated = res.Metrics[:req.Limit], true
	}
	return res, nil
}

// mergeDeclared joins the observed catalog with the declared schemas, keeping
// only names that contain the filter (a declared metric never seen is added by
// the merge whatever the filter said).
func (l Local) mergeDeclared(observed []obsquery.CatalogEntry, contains string) []obsquery.CatalogEntry {
	merged := obsquery.MergeDeclared(observed, l.declared())
	if contains == "" {
		return merged
	}
	out := merged[:0:0]
	for _, e := range merged {
		if strings.Contains(strings.ToLower(e.Name), strings.ToLower(contains)) {
			out = append(out, e)
		}
	}
	return out
}

// MetricsQuery implements Backend: the panel through obsquery.EvalMetricPanel,
// the evaluator the control plane runs, over this backend's runner.
func (l Local) MetricsQuery(ctx context.Context, req MetricsQueryRequest) (MetricsQueryResult, error) {
	return metricsWithCompare(req, func(r MetricsQueryRequest) (MetricsQueryResult, error) { return l.evalPanel(ctx, r) })
}

func (l Local) evalPanel(ctx context.Context, req MetricsQueryRequest) (MetricsQueryResult, error) {
	p := req.Panel
	p.Signal = "metrics"
	scope := LocalScope(req.Narrow, l.Env)
	o := obsquery.EvalOptions{StepSeconds: int32(min(req.StepSeconds, math.MaxInt32)), Explain: req.Explain, Now: req.Now, MaxSeries: req.MaxSeries}
	pr, err := obsquery.EvalMetricPanel(ctx, l.evalRunner(), scope, p, o)
	out := metricsResult(pr)
	if err != nil {
		return out, l.evalErr(err)
	}
	// The evaluator draws an absent metric as an empty series (a dashboard's
	// "no data in this window"). A command that asked for nothing but absent
	// metrics is more likely a misspelt name, so it says so and exits 1.
	if len(p.Series) > 0 && len(pr.Absent) == len(p.Series) {
		name := pr.Absent[0]
		return out, Wrong("no metric %q in the local ClickStack for the range", name).With(Cmd("forge", "obs", "metrics", "list", l.Env, "--contains", name))
	}
	return out, nil
}

// MetricsDescribe implements Backend: the catalog entry, the export interval,
// every attribute key with its distinct-value estimate, and the declared spec.
func (l Local) MetricsDescribe(ctx context.Context, req MetricsDescribeRequest) (MetricsDescribeResult, error) {
	scope := LocalScope(req.Narrow, l.Env)
	cat := obsquery.MetricCatalog{Scope: scope, NameContains: req.Name, Limit: obsquery.ListMetricsMax}
	cst, err := cat.Compile()
	if err != nil {
		return MetricsDescribeResult{}, compileErr(err)
	}
	res := MetricsDescribeResult{Statements: []Statement{stmt("metric catalog", cst)}}
	kind := obsquery.CatalogHistogram
	if req.Explain {
		res.Statements[0].Purpose += " (--explain does not look the metric up; assuming a histogram for the statements below)"
	} else {
		r, err := l.run(ctx, cst, DefaultLimits())
		if err != nil {
			return res, err
		}
		entries, _, err := obsquery.ParseCatalog(RowsOf(r), obsquery.ListMetricsMax)
		if err != nil {
			return res, Undetermined("the local ClickStack returned a catalog forge cannot read").Cause(err)
		}
		var mine []obsquery.CatalogEntry
		for _, e := range l.mergeDeclared(entries, req.Name) {
			if e.Name == req.Name {
				mine = append(mine, e)
			}
		}
		if len(mine) == 0 {
			return res, Wrong("no metric %q in the local ClickStack for %s", req.Name, rangeText(req.Range)).
				With(Cmd("forge", "obs", "metrics", "list", l.Env, "--contains", req.Name))
		}
		res.Info = catalogInfo(mine[0])
		kind = mine[0].Kind
		if !mine[0].Seen {
			return res, nil
		}
	}
	res.Info.Name = req.Name

	look, err := obsquery.MetricLookup{Scope: scope, Name: req.Name}.Compile()
	if err != nil {
		return res, compileErr(err)
	}
	attrs, err := obsquery.MetricAttributes{Scope: scope, Name: req.Name, Kind: kind}.Compile()
	if err != nil {
		return res, compileErr(err)
	}
	res.Statements = append(res.Statements, stmt("metric lookup", look), stmt("metric attributes", attrs))
	if req.Explain {
		return res, nil
	}
	if kind.Queryable() {
		lr, err := l.run(ctx, look, DefaultLimits())
		if err != nil {
			return res, err
		}
		info, err := obsquery.ParseLookup(RowsOf(lr), obsquery.MetricType(kind))
		if err != nil && !errors.Is(err, obsquery.ErrMetricNotFound) {
			return res, Undetermined("the local ClickStack returned a lookup forge cannot read").Cause(err)
		}
		res.IntervalSeconds, res.MixedTemporality = info.IntervalSeconds, info.MixedTemporality
	}
	ar, err := l.run(ctx, attrs, DefaultLimits())
	if err != nil {
		return res, err
	}
	var spec *metrics.Spec
	for _, sch := range l.declared() {
		for i := range sch.Metrics {
			if sch.Metrics[i].Name == req.Name && spec == nil {
				spec = &sch.Metrics[i]
			}
		}
	}
	stats, err := obsquery.ParseAttributes(RowsOf(ar), spec, 0)
	if err != nil {
		return res, Undetermined("the local ClickStack returned attributes forge cannot read").Cause(err)
	}
	for _, s := range stats {
		res.Attributes = append(res.Attributes, MetricAttribute{Key: s.Key, Resource: s.Resource, DistinctEstimate: s.DistinctEstimate, Enumerated: s.Enumerated, HighCardinality: s.HighCardinality})
	}
	res.SuggestedBreakdowns = obsquery.SuggestedBreakdowns(stats)
	return res, nil
}

// MetricsExemplars implements Backend: the sampled observations of a metric,
// the ones whose trace is stored first.
func (l Local) MetricsExemplars(ctx context.Context, req MetricsExemplarsRequest) (MetricsExemplarsResult, error) {
	scope := LocalScope(req.Narrow, l.Env)
	res := MetricsExemplarsResult{}
	kind := obsquery.CatalogHistogram
	if !req.Explain {
		cat := obsquery.MetricCatalog{Scope: scope, NameContains: req.Name, Limit: obsquery.ListMetricsMax}
		cst, err := cat.Compile()
		if err != nil {
			return res, compileErr(err)
		}
		r, err := l.run(ctx, cst, DefaultLimits())
		if err != nil {
			return res, err
		}
		entries, _, err := obsquery.ParseCatalog(RowsOf(r), obsquery.ListMetricsMax)
		if err != nil {
			return res, Undetermined("the local ClickStack returned a catalog forge cannot read").Cause(err)
		}
		found := false
		for _, e := range entries {
			if e.Name == req.Name {
				kind, found = e.Kind, true
				break
			}
		}
		if !found {
			return res, Wrong("no metric %q in the local ClickStack for %s", req.Name, rangeText(req.Range)).
				With(Cmd("forge", "obs", "metrics", "list", l.Env, "--contains", req.Name))
		}
	}
	q := obsquery.MetricExemplars{Scope: scope, Name: req.Name, Kind: kind, Where: req.Where, Group: req.Group, From: req.From, To: req.To, MinValue: req.MinValue, Limit: req.Limit}
	st, err := q.Compile()
	if err != nil {
		return res, compileErr(err)
	}
	res.Statements = []Statement{stmt("metric exemplars", st)}
	if req.Explain {
		res.Statements[0].Purpose += " (--explain does not look the metric up; assuming a histogram)"
		return res, nil
	}
	r, err := l.run(ctx, st, DefaultLimits())
	if err != nil {
		return res, err
	}
	ex, err := obsquery.ParseExemplars(RowsOf(r))
	if err != nil {
		return res, Undetermined("the local ClickStack returned exemplars forge cannot read").Cause(err)
	}
	if len(ex) > 0 {
		ids := make([]string, 0, len(ex))
		seen := map[string]bool{}
		for _, e := range ex {
			if !seen[e.TraceID] {
				seen[e.TraceID] = true
				ids = append(ids, e.TraceID)
			}
		}
		probe, err := obsquery.ExemplarsStored{Scope: scope, TraceIDs: ids, From: req.From, To: req.To}.Compile()
		if err != nil {
			return res, compileErr(err)
		}
		res.Statements = append(res.Statements, stmt("which exemplar traces are stored", probe))
		pr, err := l.run(ctx, probe, DefaultLimits())
		if err != nil {
			return res, err
		}
		stored, err := obsquery.ParseStoredTraceIDs(RowsOf(pr))
		if err != nil {
			return res, Undetermined("the local ClickStack returned trace ids forge cannot read").Cause(err)
		}
		ex = obsquery.MarkStored(ex, stored)
	}
	for _, e := range ex {
		res.Exemplars = append(res.Exemplars, MetricExemplar{Time: e.Time.UTC(), Value: finite(e.Value), TraceID: e.TraceID, SpanID: e.SpanID, Attributes: e.Attributes, TraceStored: e.Stored})
	}
	return res, nil
}

func finite(f float64) float64 {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return f
}

// ── query ───────────────────────────────────────────────────────────────

// reservedParams are the scope parameters a raw statement gets bound; a
// caller's own parameter may not shadow them.
var reservedParams = map[string]bool{
	dashspec.SQLParamFrom: true, dashspec.SQLParamTo: true, dashspec.SQLParamStep: true,
	dashspec.SQLParamEnvIDs: true, dashspec.SQLParamEnvs: true, dashspec.SQLParamServices: true,
	obsquery.EnvIDParam: true,
}

// sqlScope is the scope raw SQL is bound to. Locally there is no reliant.env.id,
// so the env is its name: {envs} holds it and {env_ids} holds the empty id the
// local rows carry, which keeps `has({env_ids}, ResourceAttributes['reliant.env.id'])`
// true for them. It is one environment, so {reliant_env_id} (an alert rule's
// placeholder) is bound to that same empty id, and a rule's query runs here as
// written.
func (l Local) sqlScope(n Narrow) obsquery.Scope {
	sc := obsquery.Scope{Database: obsquery.LocalDatabase, Local: true, Services: n.Services, Start: n.Range.From, End: n.Range.To}
	if l.Env != "" {
		sc.EnvRestricted = true
		sc.EnvIDs = []string{""}
		sc.Envs = []obsquery.Env{{ID: "", Name: l.Env}}
	}
	return sc
}

// Query implements Backend.
func (l Local) Query(ctx context.Context, req QueryRequest) (QueryResult, error) {
	if (req.SQL == "") == (req.Panel == nil) {
		return QueryResult{}, Wrong("give exactly one of --sql and --panel")
	}
	step := req.StepSeconds
	if step <= 0 {
		step, _ = obsquery.StepForMetric(req.Range.To.Sub(req.Range.From), 0, 0)
	}
	if req.SQL != "" {
		return l.querySQL(ctx, req, step)
	}
	return l.queryPanel(ctx, req, step)
}

func (l Local) querySQL(ctx context.Context, req QueryRequest, step int64) (QueryResult, error) {
	st, err := obsquery.SQLPanel{Scope: l.sqlScope(req.Narrow), SQL: req.SQL, StepSeconds: step}.Compile()
	if err != nil {
		return QueryResult{}, compileErr(err)
	}
	for k, v := range req.Params {
		if reservedParams[k] {
			return QueryResult{}, Wrong("--param %s is a scope parameter forge already binds (%s)", k, ReservedParamList())
		}
		st.Params[k] = v
	}
	res := QueryResult{Statements: []Statement{stmt("sql", st)}}
	if req.Explain {
		return res, nil
	}
	return l.runTable(ctx, st, req.Limit, res)
}

// ReservedParamList names the scope parameters, for help text.
func ReservedParamList() string {
	return "{from:DateTime64(9)}, {to:DateTime64(9)}, {step:UInt32}, {env_ids:Array(String)}, {envs:Array(String)}, {services:Array(String)}, {" + obsquery.EnvIDParam + ":String}"
}

func (l Local) runTable(ctx context.Context, st obsquery.Stmt, limit int, res QueryResult) (QueryResult, error) {
	lim := Limits{Timeout: 30 * time.Second, MaxRows: limit + 1, Break: true}
	r, err := l.run(ctx, st, lim)
	if err != nil {
		return res, err
	}
	res.Columns, res.Rows = r.Columns, r.Rows
	if limit > 0 && len(res.Rows) > limit {
		res.Rows, res.Truncated = res.Rows[:limit], true
	}
	return res, nil
}

func (l Local) queryPanel(ctx context.Context, req QueryRequest, step int64) (QueryResult, error) {
	p := *req.Panel
	if err := p.Validate(); err != nil {
		return QueryResult{}, compileErr(err)
	}
	switch {
	case p.Kind == "sql":
		r := req
		r.SQL, r.Panel = p.SQL, nil
		return l.querySQL(ctx, r, step)
	case obsquery.IsBrowserKind(p.Kind):
		return QueryResult{}, Wrong("a %s panel reads tables the hosted platform derives (error_events, rum_*); they do not exist in the local ClickStack", p.Kind)
	case p.Signal == "metrics":
		mr, err := l.MetricsQuery(ctx, MetricsQueryRequest{Narrow: req.Narrow, Panel: p, StepSeconds: req.StepSeconds})
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
	q := obsquery.PanelQuery{Scope: LocalScope(req.Narrow, l.Env), Panel: p}
	switch p.Kind {
	case "line", "heatmap":
		q.StepSeconds = step
	}
	st, shape, err := q.Compile()
	if err != nil {
		return QueryResult{}, compileErr(err)
	}
	res := QueryResult{Statements: []Statement{stmt("panel "+p.Kind, st)}}
	if req.Explain {
		return res, nil
	}
	lim := Limits{Timeout: 30 * time.Second, MaxRows: obsquery.MaxSQLRows + 1}
	r, err := l.run(ctx, st, lim)
	if err != nil {
		return res, err
	}
	res.Columns, res.Rows = flattenPanel(p, shape, r)
	if shape.Rows > 0 && len(res.Rows) > shape.Rows {
		res.Rows, res.Truncated = res.Rows[:shape.Rows], true
	}
	if req.Limit > 0 && len(res.Rows) > req.Limit {
		res.Rows, res.Truncated = res.Rows[:req.Limit], true
	}
	return res, nil
}

// flattenPanel names a panel statement's columns: the bucket is "time", the
// labels array becomes one column per group-by field, and each v<i> takes its
// series' label.
func flattenPanel(p obsquery.Panel, shape obsquery.PanelShape, r Result) ([]Column, [][]any) {
	if shape.Heatmap {
		cols := []Column{{Name: "time", Type: "DateTime"}, {Name: "low", Type: "Float64"}, {Name: "high", Type: "Float64"}, {Name: "count", Type: "Float64"}}
		rows := make([][]any, 0, len(r.Rows))
		for _, row := range r.Rows {
			if len(row) < 3 {
				continue
			}
			low, high := obsquery.HeatmapBounds(int32(AsInt(row[1])))
			rows = append(rows, []any{AsString(row[0]), low, high, AsFloat(row[2])})
		}
		return cols, rows
	}
	var cols []Column
	if shape.Bucketed {
		cols = append(cols, Column{Name: "time", Type: "DateTime"})
	}
	if shape.Total {
		cols = append(cols, Column{Name: "total", Type: "UInt8"})
	}
	for _, g := range p.GroupBy {
		cols = append(cols, Column{Name: g, Type: "String"})
	}
	for _, s := range p.Series {
		cols = append(cols, Column{Name: s.Label, Type: "Float64"})
	}
	rows := make([][]any, 0, len(r.Rows))
	for _, row := range r.Rows {
		var out []any
		i := 0
		if shape.Bucketed && i < len(row) {
			out = append(out, row[i])
			i++
		}
		if shape.Total && i < len(row) {
			out = append(out, row[i])
			i++
		}
		if i >= len(row) {
			continue
		}
		labels := AsStrings(row[i])
		i++
		for gi := range p.GroupBy {
			if gi < len(labels) {
				out = append(out, labels[gi])
			} else {
				out = append(out, "")
			}
		}
		out = append(out, row[i:]...)
		rows = append(rows, out)
	}
	return cols, rows
}
