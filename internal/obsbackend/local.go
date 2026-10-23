package obsbackend

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/dashspec"
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

// MetricsList implements Backend.
func (l Local) MetricsList(ctx context.Context, req MetricsListRequest) (MetricsListResult, error) {
	q := obsquery.MetricList{Scope: LocalScope(req.Narrow, l.Env), NameContains: req.NameContains, Limit: req.Limit}
	st, err := q.Compile()
	if err != nil {
		return MetricsListResult{}, compileErr(err)
	}
	res := MetricsListResult{Statements: []Statement{stmt("metrics", st)}}
	if req.Explain {
		return res, nil
	}
	r, err := l.run(ctx, st, DefaultLimits())
	if err != nil {
		return res, err
	}
	for _, row := range r.Rows {
		if len(row) < 4 {
			return res, Undetermined("the local ClickStack returned %d columns for metrics, want 4", len(row))
		}
		if len(res.Metrics) == req.Limit {
			res.Truncated = true
			break
		}
		res.Metrics = append(res.Metrics, MetricInfo{
			Name: AsString(row[0]), Unit: AsString(row[1]), Description: AsString(row[2]),
			Type: MetricTypeName(obsquery.MetricType(AsInt(row[3]))),
		})
	}
	return res, nil
}

// MetricTypeName is "gauge", "sum" or "histogram".
func MetricTypeName(t obsquery.MetricType) string {
	switch t {
	case obsquery.MetricGauge:
		return "gauge"
	case obsquery.MetricSum:
		return "sum"
	case obsquery.MetricHistogram:
		return "histogram"
	}
	return ""
}

var aggNames = map[string]obsquery.Agg{
	"": obsquery.AggDefault, "avg": obsquery.AggAvg, "sum": obsquery.AggSum, "min": obsquery.AggMin, "max": obsquery.AggMax,
	"count": obsquery.AggCount, "increase": obsquery.AggIncrease, "rate": obsquery.AggRate,
	"p50": obsquery.AggP50, "p95": obsquery.AggP95, "p99": obsquery.AggP99,
}

// AggNames lists the --agg values, for help text.
const AggNames = "avg, sum, min, max, count, increase, rate, p50, p95, p99"

func aggName(a obsquery.Agg) string {
	for n, v := range aggNames {
		if v == a && n != "" {
			return n
		}
	}
	return ""
}

// MetricsQuery implements Backend.
func (l Local) MetricsQuery(ctx context.Context, req MetricsQueryRequest) (MetricsQueryResult, error) {
	agg, ok := aggNames[req.Agg]
	if !ok {
		return MetricsQueryResult{}, Wrong("--agg %q: use one of %s", req.Agg, AggNames)
	}
	scope := LocalScope(req.Narrow, l.Env)
	res := MetricsQueryResult{Name: req.Name}
	look := obsquery.MetricLookup{Scope: scope, Name: req.Name}
	lst, err := look.Compile()
	if err != nil {
		return res, compileErr(err)
	}
	res.Statements = append(res.Statements, stmt("metric type lookup", lst))

	kind := obsquery.MetricUnknown
	var kinds []obsquery.MetricType
	if !req.Explain {
		lr, err := l.run(ctx, lst, DefaultLimits())
		if err != nil {
			return res, err
		}
		for _, row := range lr.Rows {
			if len(row) < 3 {
				return res, Undetermined("the local ClickStack returned %d columns for a metric lookup, want 3", len(row))
			}
			k := obsquery.MetricType(AsInt(row[0]))
			kinds = append(kinds, k)
			if kind == obsquery.MetricUnknown {
				kind, res.Unit = k, AsString(row[1])
			}
		}
		if kind == obsquery.MetricUnknown {
			return res, Wrong("no metric %q in the local ClickStack for %s", req.Name, rangeText(req.Range)).
				With(Cmd("forge", "obs", "metrics", "list", l.Env, "--contains", req.Name))
		}
		if len(kinds) > 1 {
			// The same name stored as several kinds: the first (gauge < sum <
			// histogram) is read, and the others are named so it is not silent.
			res.Statements = append(res.Statements, Statement{Purpose: fmt.Sprintf("note: %s is stored as %d metric kinds; reading %s", req.Name, len(kinds), MetricTypeName(kind))})
		}
	} else {
		kind = obsquery.MetricSum
	}

	keys := append([]string{}, req.GroupBy...)
	for _, f := range scope.Filters {
		keys = append(keys, f.Key)
	}
	if obsquery.NeedsDeclared(keys...) {
		dq := obsquery.DeclaredKeys{Scope: scope, Name: req.Name, Type: kind}
		dst, err := dq.Compile()
		if err != nil {
			return res, compileErr(err)
		}
		res.Statements = append(res.Statements, stmt("metric attribute keys", dst))
		if !req.Explain {
			dr, err := l.run(ctx, dst, DefaultLimits())
			if err != nil {
				return res, err
			}
			scope.Declared = map[string]bool{}
			for _, row := range dr.Rows {
				if len(row) > 0 {
					scope.Declared[AsString(row[0])] = true
				}
			}
		}
	}

	step := req.StepSeconds
	if rng := req.Range.To.Sub(req.Range.From); rng > 0 {
		step = obsquery.StepForMetric(rng, int32(min(req.StepSeconds, math.MaxInt32)))
	}
	resolved, err := obsquery.ResolveAgg(kind, agg)
	if err != nil {
		return res, compileErr(err)
	}
	mq := obsquery.MetricQuery{Scope: scope, Name: req.Name, Type: kind, Agg: resolved, GroupBy: req.GroupBy, StepSeconds: step}
	st, err := mq.Compile()
	if err != nil {
		return res, compileErr(err)
	}
	res.Type, res.Agg, res.StepSeconds = MetricTypeName(kind), aggName(resolved), step
	res.Statements = append(res.Statements, stmt("metric series", st))
	if req.Explain {
		return res, nil
	}
	r, err := l.run(ctx, st, DefaultLimits())
	if err != nil {
		return res, err
	}
	fold := foldSeries(r, resolved, req.GroupBy)
	res.Truncated = len(r.Rows) >= obsquery.MaxMetricRows
	res.Series, res.SeriesOmitted = capSeries(fold, req.MaxSeries)
	return res, nil
}

// foldSeries groups the rows by label set into time-ordered series.
func foldSeries(r Result, agg obsquery.Agg, groupBy []string) []Series {
	quant := map[obsquery.Agg]float64{obsquery.AggP50: 0.5, obsquery.AggP95: 0.95, obsquery.AggP99: 0.99}
	level, isQ := quant[agg]
	byKey := map[string]*Series{}
	var order []string
	for _, row := range r.Rows {
		if len(row) < 3 {
			continue
		}
		labels := AsStrings(row[1])
		var v float64
		if isQ {
			if len(row) < 4 {
				continue
			}
			q, ok := obsquery.HistogramQuantile(AsFloats(row[2]), AsFloats(row[3]), level)
			if !ok {
				continue
			}
			v = q
		} else {
			v = AsFloat(row[2])
		}
		key := strings.Join(labels, "\x00")
		s, ok := byKey[key]
		if !ok {
			s = &Series{Labels: map[string]string{}}
			for i, g := range groupBy {
				if i < len(labels) {
					s.Labels[g] = labels[i]
				}
			}
			byKey[key] = s
			order = append(order, key)
		}
		s.Points = append(s.Points, Point{Time: AsTime(row[0]), Value: finite(v)})
	}
	out := make([]Series, 0, len(order))
	for _, k := range order {
		s := byKey[k]
		sort.SliceStable(s.Points, func(i, j int) bool { return s.Points[i].Time.Before(s.Points[j].Time) })
		out = append(out, *s)
	}
	total := func(s Series) float64 {
		var t float64
		for _, p := range s.Points {
			t += math.Abs(p.Value)
		}
		return t
	}
	sort.SliceStable(out, func(i, j int) bool { return total(out[i]) > total(out[j]) })
	return out
}

func capSeries(s []Series, max int) ([]Series, int) {
	if max <= 0 {
		max = obsquery.MaxMetricSeries
	}
	if len(s) <= max {
		return s, 0
	}
	return s[:max], len(s) - max
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
}

// sqlScope is the scope raw SQL is bound to. Locally there is no reliant.env.id,
// so the env is its name: {envs} holds it and {env_ids} holds the empty id the
// local rows carry, which keeps `has({env_ids}, ResourceAttributes['reliant.env.id'])`
// true for them.
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
		step = obsquery.StepForMetric(req.Range.To.Sub(req.Range.From), 0)
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
	return "{from:DateTime64(9)}, {to:DateTime64(9)}, {step:UInt32}, {env_ids:Array(String)}, {envs:Array(String)}, {services:Array(String)}"
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
		return QueryResult{}, Wrong("a metrics panel is run per metric: use `forge obs metrics query %s <name>` (the panel's series are metric names)", l.Env)
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
