// Package obsbackend reads telemetry for `forge obs`: the env -> backend
// resolver, the ClickHouse runner, and the two backends: the local one over
// the project's ClickStack and the cloud one over the control plane's
// ObservabilityService. Backend (contract.go) is what the verbs read through.
//
//forge:not-a-component: a library the forge CLI calls; the server composes nothing from it
package obsbackend

import (
	"time"

	"github.com/reliant-labs/forge/pkg/obsquery"
)

// Backend names, as they appear in every document's scope.
const (
	BackendLocal = "local"
	BackendCloud = "cloud"
)

// Info says what a backend is bound to.
type Info struct {
	Env     string
	Backend string
	// EnvID is the control plane's id for the env, on the cloud backend.
	EnvID string
}

// Range is the half-open time range of a query, in UTC.
type Range struct{ From, To time.Time }

// Filter is a --filter key=value (or key!=value) predicate over the server's
// attribute allowlist (obsquery.AllowedKeys).
type Filter struct {
	Key   string
	Value string
	Not   bool
}

// Narrow is what every telemetry request is narrowed by.
type Narrow struct {
	Range    Range
	Services []string
	Filters  []Filter
	// Explain compiles the final statements without running them; the result
	// carries them in Statements.
	Explain bool
	// Now is the clock a metrics read marks partial buckets against; zero is
	// time.Now(). The CLI sets it so a golden is deterministic.
	Now time.Time
}

// Statement is one compiled statement a request ran (or, under Explain, would
// run): the starting point for `forge obs query --sql`.
type Statement struct {
	Purpose string            `json:"purpose"`
	SQL     string            `json:"sql"`
	Params  map[string]string `json:"params,omitempty"`
}

// ── logs ────────────────────────────────────────────────────────────────

// LogsRequest is a log search. Query is the log query language
// (obsquery.ParseLogQuery); TraceID narrows to one trace.
type LogsRequest struct {
	Narrow
	Query      string
	Severities []string
	TraceID    string
	Oldest     bool
	Limit      int
	PageToken  string
}

// LogRecord is one log line.
type LogRecord struct {
	Time           time.Time
	Severity       string
	SeverityNumber int
	Service        string
	Body           string
	TraceID        string
	SpanID         string
	Attributes     map[string]string
	// Hash is the keyset hash of the row (follow mode resumes from it).
	Hash uint64
}

// LogsResult is a page of logs in the order requested.
type LogsResult struct {
	Logs          []LogRecord
	NextPageToken string
	Statements    []Statement
}

// ── traces ──────────────────────────────────────────────────────────────

// TracesRequest lists traces, newest first.
type TracesRequest struct {
	Narrow
	ErrorsOnly    bool
	MinDurationMs float64
	// Route narrows to traces whose root span name contains it.
	Route string
	// Slow orders by duration, slowest first. The compiler pages newest-first
	// only, so the slowest are taken from the newest SlowWindow traces.
	Slow      bool
	Limit     int
	PageToken string
}

// SlowWindow is how many of the newest matching traces a Slow request sorts.
const SlowWindow = obsquery.MaxTraceLimit

// TraceSummary is one trace in a list.
type TraceSummary struct {
	TraceID        string
	Start          time.Time
	DurationMs     float64
	RootService    string
	RootName       string
	RootDurationMs float64
	Spans          int
	Errors         int
	// Weight is how many real traces this stored one stands for (tail
	// sampling); 1 locally.
	Weight   float64
	Services []string
}

// REDSummary is the rate/errors/duration over every trace the filters match.
type REDSummary struct {
	Traces      uint64
	ErrorTraces uint64
	P50Ms       float64
	P95Ms       float64
	P99Ms       float64
}

// TracesResult is a page of traces.
type TracesResult struct {
	Traces        []TraceSummary
	Summary       *REDSummary
	NextPageToken string
	// SortedFrom is how many traces a Slow request sorted (SlowWindow at most).
	SortedFrom int
	Statements []Statement
}

// TraceRequest reads one trace and the logs that carry its id.
type TraceRequest struct {
	Narrow
	TraceID string
	// LogLimit bounds the correlated logs returned.
	LogLimit int
}

// Span is one span of a trace.
type Span struct {
	SpanID        string
	ParentID      string
	Name          string
	Kind          string
	Service       string
	Start         time.Time
	DurationNs    int64
	StatusCode    string
	StatusMessage string
	Attributes    map[string]string
	Events        []SpanEvent
}

// SpanEvent is an event recorded on a span (exceptions are events).
type SpanEvent struct {
	Name       string
	Time       time.Time
	Attributes map[string]string
}

// TraceResult is every span of the trace (up to obsquery.MaxSpansPerTrace)
// and a page of its logs.
type TraceResult struct {
	TraceID string
	Spans   []Span
	// SpansTruncated says the trace has more spans than the compiler returns.
	SpansTruncated bool
	Logs           []LogRecord
	LogsTruncated  bool
	Statements     []Statement
}

// ── endpoints ───────────────────────────────────────────────────────────

// EndpointsRequest ranks server spans by name.
type EndpointsRequest struct {
	Narrow
	// Sort is "requests" (default), "p95", "errors" or "rate".
	Sort string
	Top  int
}

// Endpoint is the RED summary of one server span name of one service.
type Endpoint struct {
	Service       string
	Name          string
	Requests      float64
	Errors        float64
	ErrorRatio    float64
	RatePerSecond float64
	P50Ms         float64
	P95Ms         float64
	P99Ms         float64
}

// EndpointsResult is the ranked endpoints.
type EndpointsResult struct {
	Endpoints []Endpoint
	// Omitted counts endpoints the Top cap cut.
	Omitted    int
	Statements []Statement
}

// ── metrics ─────────────────────────────────────────────────────────────

// MetricsListRequest lists the metric names in range.
type MetricsListRequest struct {
	Narrow
	NameContains string
	Limit        int
	// NoEnv drops the env predicate, as PanelReadoutRequest.NoEnv does for a
	// platform-scoped dashboard: its telemetry carries no env id, so an
	// env-bound catalog cannot see its metrics.
	NoEnv bool
}

// MetricAttributeSpec is one attribute a release declares for a metric, with
// the closed value set when the declaration enumerates one.
type MetricAttributeSpec struct {
	Key    string
	Values []string
}

// MetricInfo is one catalog entry: a metric as one stored kind (a name stored
// as several kinds is listed once per kind).
type MetricInfo struct {
	Name        string
	Unit        string
	Description string
	// Type is "gauge", "sum", "histogram", "exp_histogram" or "summary".
	Type string
	// Monotonic is a sum that is a counter, not an up-down counter.
	Monotonic bool
	// Temporality is "delta", "cumulative", or "" (a gauge, or not known).
	Temporality string
	// Services are up to 10 service.name values that emit it; ServiceCount is
	// how many do.
	Services     []string
	ServiceCount int
	// LastSeen is the newest data point in the catalog range; zero for a
	// declared metric never seen.
	LastSeen       time.Time
	SeriesEstimate int64
	// Queryable is false for a kind the evaluator cannot run yet.
	Queryable bool
	Builtin   bool
	// Declared: the release's metrics schema lists it. Seen: it has data in
	// the range.
	Declared, Seen bool
	// Stability is "stable", "experimental" or "deprecated"; empty when not
	// declared.
	Stability          string
	DeclaredAttributes []MetricAttributeSpec
}

// MetricsListResult is the metrics in range.
type MetricsListResult struct {
	Metrics    []MetricInfo
	Truncated  bool
	Statements []Statement
}

// MetricsQueryRequest runs one metrics panel: the explorer's query. The panel
// is the dashspec vocabulary (series A, B, ... each naming a metric, an
// aggregation and its own where; a formula; panel-level where and group-by).
type MetricsQueryRequest struct {
	Narrow
	Panel       obsquery.Panel
	StepSeconds int64
	// MaxSeries lowers the series cap (the most groups per series label).
	MaxSeries int
	// Compare, when positive, runs the panel a second time over the range
	// shifted back by this much; Result.Compare carries it.
	Compare time.Duration
}

// MetricPoint is one bucket of a series. Null is a gap (no value; Value is 0);
// Partial says the bucket was still filling when it was read.
type MetricPoint struct {
	Time          time.Time
	Value         float64
	Null, Partial bool
}

// MetricSeries is one group of one panel series (Label), or of the panel's
// "formula" or "ratio". A number, table or bar panel fills Reduced (nil: the
// reduction is null).
type MetricSeries struct {
	Label   string
	Labels  map[string]string
	Points  []MetricPoint
	Reduced *float64
}

// MetricMeta says what one series label measures, after the metric was looked
// up and the aggregation resolved: everything a reader needs to format and
// label it.
type MetricMeta struct {
	Label  string
	Metric string
	// Type is "gauge", "sum" or "histogram".
	Type        string
	Monotonic   bool
	Temporality string
	// Agg is the dashspec aggregation after defaulting: rate, increase, sum,
	// avg, min, max, last, count or quantile.
	Agg       string
	Quantile  float64
	ValueUnit string
}

// MetricsCompare is the panel read over the range shifted back by Offset. Its
// points are moved forward by Offset so they overlay the current range; From
// and To are the window actually read.
type MetricsCompare struct {
	Offset      time.Duration
	From, To    time.Time
	Series      []MetricSeries
	SeriesTotal int
}

// MetricsQueryResult is what a metrics panel evaluates to.
type MetricsQueryResult struct {
	Meta   []MetricMeta
	Series []MetricSeries
	// SeriesTotal is how many groups matched before the series cap.
	SeriesTotal int
	Truncated   bool
	StepSeconds int64
	// Warnings are stable codes: mixed_bounds, mixed_temporality,
	// step_raised, metric_stored_as_n_kinds, metric_absent,
	// metric_kind_mismatch.
	Warnings   []string
	Compare    *MetricsCompare
	Statements []Statement
}

// MetricsDescribeRequest reads one metric's detail.
type MetricsDescribeRequest struct {
	Narrow
	Name string
}

// MetricAttribute is one attribute key of a metric.
type MetricAttribute struct {
	Key      string
	Resource bool
	// DistinctEstimate is the estimated number of values over the range.
	DistinctEstimate int64
	// Enumerated is the declared closed value set; empty when open.
	Enumerated []string
	// HighCardinality: grouping by the key could exceed the series cap.
	HighCardinality bool
}

// MetricsDescribeResult is the metric detail the explorer's side pane shows.
type MetricsDescribeResult struct {
	Info MetricInfo
	// IntervalSeconds estimates the export interval: the floor of a useful
	// step. 0 when it cannot be estimated.
	IntervalSeconds  int64
	MixedTemporality bool
	Attributes       []MetricAttribute
	// SuggestedBreakdowns are the attribute keys worth grouping by, best first.
	SuggestedBreakdowns []string
	Statements          []Statement
}

// MetricsExemplarsRequest reads the exemplars of one metric. Narrow.Range is
// the scope; From and To (both zero: the scope) are the window the exemplars'
// own timestamps must fall in.
type MetricsExemplarsRequest struct {
	Narrow
	Name     string
	From, To time.Time
	// Where narrows to series matching the predicates (panel vocabulary).
	Where []obsquery.PanelPredicate
	// Group narrows to one series group: group-by key to value.
	Group    map[string]string
	MinValue *float64
	Limit    int
}

// MetricExemplar is one sampled observation of a metric.
type MetricExemplar struct {
	Time       time.Time
	Value      float64
	TraceID    string
	SpanID     string
	Attributes map[string]string
	// TraceStored: the trace is in the trace store. false means it was sampled
	// out and the trace view would be empty.
	TraceStored bool
}

// MetricsExemplarsResult is the exemplars, stored traces first.
type MetricsExemplarsResult struct {
	Exemplars  []MetricExemplar
	Statements []Statement
}

// ── query ───────────────────────────────────────────────────────────────

// QueryRequest runs raw SQL (SQL) or a dashboard panel (Panel) against the
// scope. Raw SQL has the scope bound as the dashspec.SQLParam* parameters; it
// may also bind Params of its own.
type QueryRequest struct {
	Narrow
	SQL    string
	Panel  *obsquery.Panel
	Params map[string]string
	// StepSeconds is the {step} parameter and a panel's bucket width; 0 picks
	// one for the range.
	StepSeconds int64
	// Limit is the most rows returned; the statement is cut one past it to
	// detect truncation.
	Limit int
}

// QueryResult is a table: the rows of a raw statement, or a panel flattened
// into rows.
type QueryResult struct {
	Columns    []Column
	Rows       [][]any
	Truncated  bool
	Statements []Statement
}
