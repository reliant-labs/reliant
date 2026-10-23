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
}

// MetricInfo names one metric.
type MetricInfo struct {
	Name        string
	Unit        string
	Description string
	// Type is "gauge", "sum" or "histogram".
	Type string
}

// MetricsListResult is the metrics in range.
type MetricsListResult struct {
	Metrics    []MetricInfo
	Truncated  bool
	Statements []Statement
}

// MetricsQueryRequest reads one metric as time series.
type MetricsQueryRequest struct {
	Narrow
	Name string
	// Agg is "", avg, sum, min, max, count, increase, rate, p50, p95 or p99;
	// "" resolves per metric type.
	Agg         string
	GroupBy     []string
	StepSeconds int64
	// MaxSeries bounds the series returned (the largest by total).
	MaxSeries int
}

// Point is one sample of a series.
type Point struct {
	Time  time.Time
	Value float64
}

// Series is one labelled time series.
type Series struct {
	Labels map[string]string
	Points []Point
}

// MetricsQueryResult is the series of one metric.
type MetricsQueryResult struct {
	Name        string
	Type        string
	Unit        string
	Agg         string
	StepSeconds int64
	Series      []Series
	// SeriesOmitted counts series the MaxSeries cap cut; Truncated says the
	// compiler's own row cap was hit.
	SeriesOmitted int
	Truncated     bool
	Statements    []Statement
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
