package obsquery

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Trace limits.
const (
	DefaultTraceLimit = 50
	MaxTraceLimit     = 500
	// MaxSpansPerTrace caps GetTrace; the statement fetches one more to detect it.
	MaxSpansPerTrace = 10000
)

var traceIDRe = regexp.MustCompile(`^[0-9a-fA-F]{1,64}$`)

// NormalizeTraceID lowercases a hex trace id, or returns ErrInvalid.
func NormalizeTraceID(id string) (string, error) {
	if !traceIDRe.MatchString(id) {
		return "", invalid("trace_id must be 1-64 hex characters")
	}
	return strings.ToLower(id), nil
}

// TraceOrder is the row order of a ListTraces page.
type TraceOrder string

const (
	// OrderNewest is newest first, the default (the zero TraceOrder).
	OrderNewest TraceOrder = ""
	// OrderDurationDesc is slowest first: the traces to open when latency
	// regressed. Duration is the trace's wall-clock span (dur_ms), not a
	// weighted figure, so the sampling weight does not move a trace in the
	// order; it still weights the RED summaries over the same filters.
	OrderDurationDesc TraceOrder = "duration_desc"
)

// traceDurNanoExpr is a trace's duration as the exact integer dur_ms is derived
// from, so a keyset cursor compares integers, never a rounded float.
const traceDurNanoExpr = "(max(toUnixTimestamp64Nano(Timestamp) + toInt64(Duration)) - min(toUnixTimestamp64Nano(Timestamp)))"

// TraceQuery is a ListTraces request.
type TraceQuery struct {
	Scope         Scope
	ErrorsOnly    bool
	MinDurationMs float64
	RootSpanName  string
	// GroupBy are the allowlisted keys the RED summary is split by, valued at
	// each trace's root span (CompileREDGroups).
	GroupBy []string
	// Order is the page's row order; the zero value is newest first. It is
	// part of the cursor's fingerprint: a token only continues the order that
	// produced it.
	Order  TraceOrder
	Limit  int
	Cursor *TraceCursor
}

// TraceCursor is the keyset position of the last trace of a page.
type TraceCursor struct {
	TsNano  int64  `json:"t"`
	TraceID string `json:"i"`
	// DurNano is the last trace's duration, set only for OrderDurationDesc.
	DurNano int64  `json:"d,omitempty"`
	Finger  string `json:"f"`
}

// Fingerprint identifies the filters a cursor belongs to.
func (q TraceQuery) Fingerprint() string {
	parts := append(q.Scope.Fingerprint(), strconv.FormatBool(q.ErrorsOnly),
		strconv.FormatFloat(q.MinDurationMs, 'g', -1, 64), q.RootSpanName)
	if q.Order != OrderNewest {
		// Appended only for a non-default order, so newest-first tokens are
		// byte-identical to what they were before Order existed.
		parts = append(parts, "order:"+string(q.Order))
	}
	return fingerprint(parts...)
}

// NextCursor builds the page token from the last returned trace of a
// newest-first page. A duration-ordered page uses NextDurationCursor.
func (q TraceQuery) NextCursor(start time.Time, traceID string) string {
	raw, _ := json.Marshal(TraceCursor{TsNano: start.UnixNano(), TraceID: traceID, Finger: q.Fingerprint()})
	return base64.RawURLEncoding.EncodeToString(raw)
}

// NextDurationCursor builds the page token from the last returned trace of an
// OrderDurationDesc page; durMs is that row's dur_ms column.
func (q TraceQuery) NextDurationCursor(start time.Time, traceID string, durMs float64) string {
	raw, _ := json.Marshal(TraceCursor{
		TsNano: start.UnixNano(), TraceID: traceID, DurNano: int64(math.Round(durMs * 1e6)), Finger: q.Fingerprint(),
	})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func (q TraceQuery) validateOrder() error {
	switch q.Order {
	case OrderNewest, OrderDurationDesc:
		return nil
	}
	return invalid("unknown trace order %q", string(q.Order))
}

// DecodeTraceCursor parses a page token.
func DecodeTraceCursor(tok string) (*TraceCursor, error) {
	if tok == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil {
		return nil, invalid("page_token is malformed")
	}
	var c TraceCursor
	if err := json.Unmarshal(raw, &c); err != nil || c.Finger == "" {
		return nil, invalid("page_token is malformed")
	}
	return &c, nil
}

// perTrace is the per-trace summary subquery shared by the page and RED
// statements. Columns: trace_id, start (DateTime64(9)), dur_ms, root_service,
// root_name, root_dur_ms, span_count, error_count, tw (how many real traces this
// stored one stands for, see weight.go), services, source.
func (q TraceQuery) perTrace(b *builder, withCursor, withGroups bool) (string, error) {
	conds := []string{b.rangeCond("Timestamp", q.Scope), "TraceId != ''"}
	// Resource-level filters pick the spans counted; span-level ones keep a
	// trace when ANY of its spans matches.
	sc, spanHaving, err := b.scopeParts(SigTraces, q.Scope, true)
	if err != nil {
		return "", err
	}
	conds = append(conds, sc...)
	// Narrow the traces to aggregate before aggregating them. The per-trace
	// GROUP BY reads every span in the range, which over days of traffic
	// outlasts any request budget; a trace with an error, or whose root is
	// named X, has at least one span with an error status, or named X, so
	// these candidate sets are supersets of what the HAVING keeps and the
	// result is unchanged. (A root is the earliest parentless span, or the
	// earliest span of an orphaned trace, so the name test is over any span.)
	prefilter := func(pred string) {
		sub := append([]string{b.rangeCond("Timestamp", q.Scope), pred}, sc...)
		conds = append(conds, "TraceId IN (SELECT TraceId FROM "+b.table(q.Scope, "otel_traces")+" "+where(sub)+")")
	}
	if q.ErrorsOnly {
		prefilter(spanErrorExpr)
	}
	if q.RootSpanName != "" {
		prefilter("positionCaseInsensitive(SpanName, " + b.str(q.RootSpanName) + ") > 0")
	}

	having := append([]string{}, spanHaving...)
	if q.ErrorsOnly {
		having = append(having, "error_count > 0")
	}
	if q.MinDurationMs > 0 {
		having = append(having, "dur_ms >= "+b.f64(q.MinDurationMs))
	}
	if q.RootSpanName != "" {
		having = append(having, "positionCaseInsensitive(root_name, "+b.str(q.RootSpanName)+") > 0")
	}
	if withCursor && q.Cursor != nil {
		if q.Cursor.Finger != q.Fingerprint() {
			return "", invalid("page_token does not belong to this query")
		}
		if q.Order == OrderDurationDesc {
			having = append(having, "("+traceDurNanoExpr+", start, trace_id) < ("+b.i64(q.Cursor.DurNano)+
				", fromUnixTimestamp64Nano("+b.i64(q.Cursor.TsNano)+"), "+b.str(q.Cursor.TraceID)+")")
		} else {
			having = append(having, "(start, trace_id) < (fromUnixTimestamp64Nano("+b.i64(q.Cursor.TsNano)+"), "+b.str(q.Cursor.TraceID)+")")
		}
	}
	groupCols := ""
	if withGroups {
		for i, k := range q.GroupBy {
			e, _, err := b.groupExpr(SigTraces, k)
			if err != nil {
				return "", err
			}
			groupCols += ",\n       argMin(toString(" + e + "), (ParentSpanId != '', Timestamp)) AS g" + strconv.Itoa(i)
		}
	}
	sql := "SELECT TraceId AS trace_id,\n" +
		"       min(Timestamp) AS start,\n" +
		"       toFloat64(max(toUnixTimestamp64Nano(Timestamp) + toInt64(Duration)) - min(toUnixTimestamp64Nano(Timestamp))) / 1000000 AS dur_ms,\n" +
		"       argMin(ServiceName, (ParentSpanId != '', Timestamp)) AS root_service,\n" +
		"       argMin(SpanName, (ParentSpanId != '', Timestamp)) AS root_name,\n" +
		"       toFloat64(argMin(Duration, (ParentSpanId != '', Timestamp))) / 1000000 AS root_dur_ms,\n" +
		"       count() AS span_count,\n" +
		"       countIf(" + spanErrorExpr + ") AS error_count,\n" +
		"       max(" + spanWeightExpr + ") AS tw,\n" +
		"       groupUniqArray(ServiceName) AS services,\n" +
		"       maxIf(ResourceAttributes['reliant.source'], ResourceAttributes['reliant.source'] != '') AS source" + groupCols + "\n" +
		"FROM " + b.table(q.Scope, "otel_traces") + "\n" + where(conds) + "\n" +
		"GROUP BY trace_id"
	if len(having) > 0 {
		sql += "\nHAVING " + strings.Join(having, "\n   AND ")
	}
	return sql, nil
}

// Compile builds the page statement in q.Order (newest first by default), one
// row more than Limit.
func (q TraceQuery) Compile() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeTraces); err != nil {
		return Stmt{}, err
	}
	if err := q.validateOrder(); err != nil {
		return Stmt{}, err
	}
	if q.Limit <= 0 || q.Limit > MaxTraceLimit {
		return Stmt{}, invalid("page size must be 1-%d", MaxTraceLimit)
	}
	b := newBuilder(q.Scope)
	inner, err := q.perTrace(b, true, false)
	if err != nil {
		return Stmt{}, err
	}
	order := "start DESC, trace_id DESC"
	if q.Order == OrderDurationDesc {
		order = traceDurNanoExpr + " DESC, start DESC, trace_id DESC"
	}
	return b.stmt(inner + "\nORDER BY " + order + "\nLIMIT " + strconv.Itoa(q.Limit+1)), nil
}

// CompileRED builds the RED summary over every trace the filters match.
// Columns: trace_count, error_trace_count, p50, p95, p99.
func (q TraceQuery) CompileRED() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeTraces); err != nil {
		return Stmt{}, err
	}
	b := newBuilder(q.Scope)
	inner, err := q.perTrace(b, false, false)
	if err != nil {
		return Stmt{}, err
	}
	return b.stmt("SELECT toUInt64(round(sum(tw))) AS trace_count, toUInt64(round(sumIf(tw, error_count > 0))) AS error_trace_count,\n" +
		"       quantilesExactWeighted(0.5, 0.95, 0.99)(dur_ms, toUInt64(round(tw))) AS q\nFROM (\n" + inner + "\n)"), nil
}

// MaxREDGroups bounds the groups CompileREDGroups returns.
const MaxREDGroups = 50

// CompileREDGroups builds the RED summary split by q.GroupBy (values read at
// each trace's root span), busiest group first. Columns: labels
// (Array(String)), trace_count, error_trace_count, q.
func (q TraceQuery) CompileREDGroups() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeTraces); err != nil {
		return Stmt{}, err
	}
	if len(q.GroupBy) == 0 || len(q.GroupBy) > MaxMetricGroupBy {
		return Stmt{}, invalid("group_by takes 1-%d keys", MaxMetricGroupBy)
	}
	b := newBuilder(q.Scope)
	inner, err := q.perTrace(b, false, true)
	if err != nil {
		return Stmt{}, err
	}
	names := make([]string, len(q.GroupBy))
	for i := range names {
		names[i] = "g" + strconv.Itoa(i)
	}
	gl := strings.Join(names, ", ")
	return b.stmt("SELECT [" + gl + "] AS labels, toUInt64(round(sum(tw))) AS trace_count, toUInt64(round(sumIf(tw, error_count > 0))) AS error_trace_count,\n" +
		"       quantilesExactWeighted(0.5, 0.95, 0.99)(dur_ms, toUInt64(round(tw))) AS q\nFROM (\n" + inner + "\n)\nGROUP BY " + gl +
		"\nORDER BY trace_count DESC, " + gl + "\nLIMIT " + strconv.Itoa(MaxREDGroups)), nil
}

// TraceSpans is a GetTrace request.
type TraceSpans struct {
	Scope   Scope
	TraceID string
}

// Compile builds the all-spans statement. Columns: Timestamp, SpanId,
// ParentSpanId, SpanName, SpanKind, ServiceName, Duration, StatusCode,
// StatusMessage, SpanAttributes, ResourceAttributes, Events.Timestamp,
// Events.Name, Events.Attributes, Links.TraceId, Links.SpanId.
func (q TraceSpans) Compile() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeTraces); err != nil {
		return Stmt{}, err
	}
	id, err := NormalizeTraceID(q.TraceID)
	if err != nil {
		return Stmt{}, err
	}
	b := newBuilder(q.Scope)
	conds := []string{b.rangeCond("Timestamp", q.Scope), "TraceId = " + b.str(id)}
	if e := b.envCond(q.Scope); e != "" {
		conds = append(conds, e)
	}
	return b.stmt("SELECT Timestamp, SpanId, ParentSpanId, SpanName, SpanKind, ServiceName, Duration, StatusCode, StatusMessage,\n" +
		"       SpanAttributes, ResourceAttributes, `Events.Timestamp`, `Events.Name`, `Events.Attributes`, `Links.TraceId`, `Links.SpanId`\n" +
		"FROM " + b.table(q.Scope, "otel_traces") + "\n" + where(conds) + "\n" +
		"ORDER BY Timestamp, SpanId\nLIMIT " + strconv.Itoa(MaxSpansPerTrace+1)), nil
}

// ── Service overview ────────────────────────────────────────────────────

// Overview is a GetServiceOverview request.
type Overview struct {
	Scope Scope
	// GroupBy splits each service by these allowlisted keys.
	GroupBy []string
}

// StepFor returns the bucket width for a range: about 120 points, snapped to a
// round number of seconds, never below minSec.
func StepFor(d time.Duration, wantPoints int, minSec int64) int64 {
	if wantPoints <= 0 {
		wantPoints = 120
	}
	raw := int64(d.Seconds()) / int64(wantPoints)
	for _, s := range []int64{1, 5, 10, 15, 30, 60, 120, 300, 600, 900, 1800, 3600, 7200, 21600, 43200, 86400} {
		if s >= raw && s >= minSec {
			return s
		}
	}
	return 86400
}

// groupSel renders the group-by expressions of an overview statement as
// ", <expr> AS g0, ..." (for SELECT) and ", g0, ..." (for GROUP BY / ORDER BY).
func (q Overview) groupSel(b *builder, sig Signal) (sel, names string, err error) {
	if len(q.GroupBy) > MaxMetricGroupBy {
		return "", "", invalid("at most %d group_by keys", MaxMetricGroupBy)
	}
	for i, k := range q.GroupBy {
		e, resource, err := b.groupExpr(sig, k)
		if err != nil {
			return "", "", err
		}
		if sig == SigLogs && !resource {
			return "", "", errNotOnLogs
		}
		n := "g" + strconv.Itoa(i)
		sel += ", toString(" + e + ") AS " + n
		names += ", " + n
	}
	return sel, names, nil
}

// errNotOnLogs marks a group-by key that has no meaning on logs; the caller
// then leaves the log-derived figure out rather than failing the request.
var errNotOnLogs = invalid("group key does not apply to logs")

// IsNotOnLogs reports whether err is the not-applicable-to-logs marker.
func IsNotOnLogs(err error) bool { return err == errNotOnLogs }

// StreamTypeAttr is the span attribute serverkit stamps on the server span of
// a streaming RPC: "server", "client" or "bidi".
const StreamTypeAttr = "forge.rpc.stream_type"

// RED builds the per-service request statement over server spans. Columns:
// ServiceName, requests, errors, q (p50,p95,p99 in ms), source, then one
// String column per group-by key. Requests and errors count every server
// span; the latency quantiles are over unary ones only, since a streaming
// RPC's span lasts as long as the stream (spanStreamExpr).
func (q Overview) RED() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeOverview); err != nil {
		return Stmt{}, err
	}
	b := newBuilder(q.Scope)
	sc, err := b.scopeConds(SigTraces, q.Scope)
	if err != nil {
		return Stmt{}, err
	}
	gsel, gnames, err := q.groupSel(b, SigTraces)
	if err != nil {
		return Stmt{}, err
	}
	conds := append([]string{b.rangeCond("Timestamp", q.Scope), spanServerExpr}, sc...)
	return b.stmt("SELECT ServiceName, toUInt64(round(sum(" + spanWeightExpr + "))) AS requests, toUInt64(round(sumIf(" + spanWeightExpr + ", " + spanErrorExpr + "))) AS errors,\n" +
		"       quantilesExactWeightedIf(0.5, 0.95, 0.99)(Duration / 1000000, " + spanWeightInt + ", NOT " + spanStreamExpr + ") AS q,\n" +
		"       maxIf(ResourceAttributes['reliant.source'], ResourceAttributes['reliant.source'] != '') AS source" + gsel + "\n" +
		"FROM " + b.table(q.Scope, "otel_traces") + "\n" + where(conds) + "\n" +
		"GROUP BY ServiceName" + gnames + "\nORDER BY requests DESC, ServiceName" + gnames + "\nLIMIT 200"), nil
}

// Trend builds the per-service per-bucket request and error counts. Columns:
// ServiceName, bucket (DateTime), requests, errors, then the group columns.
func (q Overview) Trend(stepSec int64) (Stmt, error) {
	if err := q.Scope.validate(MaxRangeOverview); err != nil {
		return Stmt{}, err
	}
	b := newBuilder(q.Scope)
	sc, err := b.scopeConds(SigTraces, q.Scope)
	if err != nil {
		return Stmt{}, err
	}
	gsel, gnames, err := q.groupSel(b, SigTraces)
	if err != nil {
		return Stmt{}, err
	}
	conds := append([]string{b.rangeCond("Timestamp", q.Scope), spanServerExpr}, sc...)
	return b.stmt("SELECT ServiceName, toStartOfInterval(toDateTime(Timestamp), toIntervalSecond(" + b.u32(uint32(stepSec)) + ")) AS bucket,\n" +
		"       toUInt64(round(sum(" + spanWeightExpr + "))) AS requests, toUInt64(round(sumIf(" + spanWeightExpr + ", " + spanErrorExpr + "))) AS errors" + gsel + "\n" +
		"FROM " + b.table(q.Scope, "otel_traces") + "\n" + where(conds) + "\n" +
		"GROUP BY ServiceName, bucket" + gnames + "\nORDER BY ServiceName" + gnames + ", bucket\nLIMIT 100000"), nil
}

// Logs builds the per-service log counts. Columns: ServiceName, total,
// error_logs, source, then the group columns. Only resource-level filters and
// group keys apply: a span-level filter does not describe a log record, and
// would silently zero the count. IsNotOnLogs(err) means a group key is
// span-level, so there is no log figure to give.
func (q Overview) Logs() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeOverview); err != nil {
		return Stmt{}, err
	}
	b := newBuilder(q.Scope)
	sc, err := b.resourceConds(SigLogs, q.Scope)
	if err != nil {
		return Stmt{}, err
	}
	gsel, gnames, err := q.groupSel(b, SigLogs)
	if err != nil {
		return Stmt{}, err
	}
	conds := append([]string{b.rangeCond("Timestamp", q.Scope)}, sc...)
	errSet := make([]string, 0, 8)
	for _, n := range severitySet(4, 5) {
		errSet = append(errSet, b.str(n))
	}
	return b.stmt("SELECT ServiceName, count() AS total, countIf(upper(SeverityText) IN (" + strings.Join(errSet, ", ") + ")) AS error_logs,\n" +
		"       maxIf(ResourceAttributes['reliant.source'], ResourceAttributes['reliant.source'] != '') AS source" + gsel + "\n" +
		"FROM " + b.table(q.Scope, "otel_logs") + "\n" + where(conds) + "\n" +
		"GROUP BY ServiceName" + gnames + "\nLIMIT 500"), nil
}

// SeenServices lists service names with spans or logs in the scope, for the
// synthesized dashboards. Columns: ServiceName, has_spans.
func (q Overview) SeenServices() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeDashboards); err != nil {
		return Stmt{}, err
	}
	b := newBuilder(q.Scope)
	tsc, err := b.scopeConds(SigTraces, q.Scope)
	if err != nil {
		return Stmt{}, err
	}
	lsc, err := b.resourceConds(SigLogs, q.Scope)
	if err != nil {
		return Stmt{}, err
	}
	tr := append([]string{b.rangeCond("Timestamp", q.Scope), spanServerExpr}, tsc...)
	lg := append([]string{b.rangeCond("Timestamp", q.Scope)}, lsc...)
	return b.stmt("SELECT ServiceName, max(has_spans) AS has_spans FROM (\n" +
		"  SELECT ServiceName, 1 AS has_spans FROM " + b.table(q.Scope, "otel_traces") + " " + where(tr) + " GROUP BY ServiceName\n" +
		"  UNION ALL\n" +
		"  SELECT ServiceName, 0 AS has_spans FROM " + b.table(q.Scope, "otel_logs") + " " + where(lg) + " GROUP BY ServiceName\n" +
		") WHERE ServiceName != '' GROUP BY ServiceName ORDER BY ServiceName LIMIT 100"), nil
}

func fingerprint(parts ...string) string {
	return hashHex(strings.Join(parts, "\x00"))
}
