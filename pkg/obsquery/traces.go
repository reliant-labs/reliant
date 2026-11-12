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
	// UserID keeps traces with a span of that user (traceUserExpr); SessionID
	// those with a span of that browser session (traceSessionExpr). Empty keeps
	// all.
	UserID, SessionID string
	// Browser and OS keep traces a browser of that client started (the
	// platform-derived user_agent.name / os.name of a browser span; exact).
	// FrontendVersion keeps traces with a browser span of that build, and
	// BackendVersion those with a server span of that build (service.version on
	// either side; see traceFrontendVersionExpr). Empty keeps all.
	Browser, OS, FrontendVersion, BackendVersion string
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
	// Appended only when set, for the same reason.
	if q.UserID != "" {
		parts = append(parts, "user:"+q.UserID)
	}
	if q.SessionID != "" {
		parts = append(parts, "session:"+q.SessionID)
	}
	for _, f := range [][2]string{{"browser", q.Browser}, {"os", q.OS}, {"frontend", q.FrontendVersion}, {"backend", q.BackendVersion}} {
		if f[1] != "" {
			parts = append(parts, f[0]+":"+f[1])
		}
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

// Who and where a span is, across the two shapes that carry it:
//
//   - user: a backend span's user.id, which forge's IdentitySpanInterceptor sets
//     from the AUTHENTICATED caller, else a browser span's userId, the id the
//     page reported (display only);
//   - session: a backend span's session.id (the browser's baggage, copied onto
//     the server span), else a browser span's rum.sessionId resource attribute;
//   - geo: the browser resource's geo.* attributes, which the platform's gateway
//     derived from the address the browser connected from. A backend span has
//     none, so a trace's location is its browser spans'.
const (
	traceUserExpr    = "if(SpanAttributes['user.id'] != '', SpanAttributes['user.id'], SpanAttributes['userId'])"
	traceSessionExpr = "if(SpanAttributes['session.id'] != '', SpanAttributes['session.id'], ResourceAttributes['rum.sessionId'])"
	traceCountryExpr = "ResourceAttributes['geo.country.iso_code']"
	traceRegionExpr  = "ResourceAttributes['geo.region.name']"
	traceCityExpr    = "ResourceAttributes['geo.locality.name']"
	// traceGeoExpr picks one span's whole location, so country, region and city
	// always come from the same span. ClickHouse computes an aggregate once
	// however many times the statement names it, so the three tupleElement reads
	// share one tuple.
	traceGeoExpr = "anyIf(tuple(" + traceCountryExpr + ", " + traceRegionExpr + ", " + traceCityExpr + "), " + traceCountryExpr + " != '')"
)

// Which client and which builds a trace ran on:
//
//   - a browser span is one with a rum.sessionId; its resource carries the
//     client the platform derived at ingest (user_agent.*, os.*,
//     reliant.device.type) and the desktop app's version the page reported
//     (app.desktop.version), all of one resource, so traceClientExpr picks
//     them whole from one span;
//   - the frontend version is a browser span's service.version (the build
//     that made the call);
//   - the backend versions are the service.version of every other span that
//     did not arrive through the browser pipeline (the builds that served
//     it), at most 8 (service, version) pairs.
const (
	traceBrowserSpan         = "ResourceAttributes['rum.sessionId'] != ''"
	traceServerSpan          = "ResourceAttributes['rum.sessionId'] = '' AND ResourceAttributes['reliant.ingest.class'] != 'browser'"
	traceVersionExpr         = "ResourceAttributes['service.version']"
	traceBrowserNameExpr     = "ResourceAttributes['user_agent.name']"
	traceOSNameExpr          = "ResourceAttributes['os.name']"
	traceDesktopVersionExpr  = "ResourceAttributes['app.desktop.version']"
	traceFrontendVersionExpr = "anyIf(" + traceVersionExpr + ", " + traceBrowserSpan + " AND " + traceVersionExpr + " != '')"
	traceBackendVersionsExpr = "groupUniqArrayIf(8)(tuple(ServiceName, " + traceVersionExpr + "), " + traceServerSpan + " AND " + traceVersionExpr + " != '')"
	traceClientExpr          = "anyIf(tuple(" + traceBrowserNameExpr + ", ResourceAttributes['user_agent.version'], " + traceOSNameExpr + ", ResourceAttributes['os.version'], ResourceAttributes['reliant.device.type'], " + traceDesktopVersionExpr + "), " + traceBrowserSpan + " AND (" + traceBrowserNameExpr + " != '' OR " + traceDesktopVersionExpr + " != ''))"
)

// tracePageCols are the page statement's per-trace columns (perTrace's
// contract below).
const tracePageCols = "SELECT TraceId AS trace_id,\n" +
	"       min(Timestamp) AS start,\n" +
	"       toFloat64(max(toUnixTimestamp64Nano(Timestamp) + toInt64(Duration)) - min(toUnixTimestamp64Nano(Timestamp))) / 1000000 AS dur_ms,\n" +
	"       argMin(ServiceName, (ParentSpanId != '', Timestamp)) AS root_service,\n" +
	"       argMin(SpanName, (ParentSpanId != '', Timestamp)) AS root_name,\n" +
	"       toFloat64(argMin(Duration, (ParentSpanId != '', Timestamp))) / 1000000 AS root_dur_ms,\n" +
	"       count() AS span_count,\n" +
	"       countIf(" + spanErrorExpr + ") AS error_count,\n" +
	"       max(" + spanWeightExpr + ") AS tw,\n" +
	"       groupUniqArray(ServiceName) AS services,\n" +
	"       maxIf(ResourceAttributes['reliant.source'], ResourceAttributes['reliant.source'] != '') AS source,\n" +
	"       anyIf(" + traceUserExpr + ", " + traceUserExpr + " != '') AS user_id,\n" +
	"       anyIf(" + traceSessionExpr + ", " + traceSessionExpr + " != '') AS session_id,\n" +
	"       tupleElement(" + traceGeoExpr + ", 1) AS country,\n" +
	"       tupleElement(" + traceGeoExpr + ", 2) AS region,\n" +
	"       tupleElement(" + traceGeoExpr + ", 3) AS city,\n" +
	"       tupleElement(" + traceClientExpr + ", 1) AS client_name,\n" +
	"       tupleElement(" + traceClientExpr + ", 2) AS client_version,\n" +
	"       tupleElement(" + traceClientExpr + ", 3) AS os_name,\n" +
	"       tupleElement(" + traceClientExpr + ", 4) AS os_version,\n" +
	"       tupleElement(" + traceClientExpr + ", 5) AS device_type,\n" +
	"       tupleElement(" + traceClientExpr + ", 6) AS desktop_version,\n" +
	"       " + traceFrontendVersionExpr + " AS frontend_version,\n" +
	"       arrayMap(p -> tupleElement(p, 1), " + traceBackendVersionsExpr + ") AS backend_services,\n" +
	"       arrayMap(p -> tupleElement(p, 2), " + traceBackendVersionsExpr + ") AS backend_versions"

// traceParts are the pieces a per-trace statement is assembled from.
type traceParts struct {
	// spans picks the spans a trace's columns aggregate: the range and the
	// scope's resource-level filters.
	spans []string
	// candidates narrow which traces are aggregated at all, each a
	// `TraceId IN (...)` superset of what having keeps. They test the trace id
	// alone, so they never change a kept trace's columns.
	candidates []string
	// having keeps a trace by its aggregates. Every term reads only trace_id,
	// start, dur_ms, root_name, error_count or a countIf over the span
	// columns: the cheap aggregates, never a page-only column.
	having    []string
	groupCols string
}

// conds is the WHERE of a statement that aggregates every candidate trace.
func (p traceParts) conds() []string {
	return append(append([]string{}, p.spans...), p.candidates...)
}

func (p traceParts) havingClause() string {
	if len(p.having) == 0 {
		return ""
	}
	return "\nHAVING " + strings.Join(p.having, "\n   AND ")
}

// pageOrder is the page's ORDER BY in q.Order.
func (q TraceQuery) pageOrder() string {
	if q.Order == OrderDurationDesc {
		return traceDurNanoExpr + " DESC, start DESC, trace_id DESC"
	}
	return "start DESC, trace_id DESC"
}

// perTrace is the per-trace summary subquery shared by the page and RED
// statements. Columns: trace_id, start (DateTime64(9)), dur_ms, root_service,
// root_name, root_dur_ms, span_count, error_count, tw (how many real traces this
// stored one stands for, see weight.go), services, source, user_id, session_id,
// country, region, city (each the first non-empty value of its trace's spans,
// "" when none), then client_name, client_version, os_name, os_version,
// device_type, desktop_version (one browser span's, traceClientExpr),
// frontend_version, backend_services and backend_versions (two parallel arrays,
// the same pairs in the same order).
//
// The page statement's columns are a NAMED contract, not a positional one: a
// column, once returned, keeps its name, and new ones are added as obsquery
// learns them. Read the page by column name (the control plane's ListTraces
// does, scanByName); a strict positional Scan breaks on every addition.
// TestTracePageKeepsEveryColumnItReturnedBefore pins the names.
//
// With page false (the RED statements) it returns only trace_id, dur_ms,
// root_name, error_count and tw, plus the group columns: the summaries read
// nothing else, and nothing reads that subquery by name.
//
// With page true it selects the page in two phases of one statement, already
// ordered by q.pageOrder and limited to Limit+1 inside (the caller orders and
// limits the outer rows the same way). Phase 1, the IN subquery, aggregates
// every candidate trace of the range but only the cheap columns its HAVING,
// cursor and ORDER BY read, and keeps the page's trace ids. Phase 2 computes
// the page columns over the spans of those ids alone. Each page column is an
// aggregate over every span of its trace (map lookups, anyIf,
// groupUniqArrayIf), and computing them for every trace of the window before
// the LIMIT kept ~100 of them cost a prod ListTraces 5-10s; in two phases the
// same rows took 1.2-2.9s and read 145MB instead of 350MB. Phase 2's spans are
// the same as the one-pass statement's for each kept trace (the candidate
// filters test only the trace id, which phase 1 has already applied), so every
// row is unchanged.
func (q TraceQuery) perTrace(b *builder, page, withGroups bool) (string, error) {
	p, err := q.traceParts(b, page, withGroups)
	if err != nil {
		return "", err
	}
	if !page {
		// A RED summary reads four facts per trace. The page's who/where/client
		// columns (each an aggregate over every span) cost it most of its time:
		// over 24h of prod traffic the full row set ran past the gateway's 15s
		// and the list answered 504, so the summary aggregates only what it
		// reads (and what its HAVING names).
		return "SELECT TraceId AS trace_id,\n" +
			"       toFloat64(" + traceDurNanoExpr + ") / 1000000 AS dur_ms,\n" +
			"       argMin(SpanName, (ParentSpanId != '', Timestamp)) AS root_name,\n" +
			"       countIf(" + spanErrorExpr + ") AS error_count,\n" +
			"       max(" + spanWeightExpr + ") AS tw" + p.groupCols + "\n" +
			"FROM " + b.table(q.Scope, "otel_traces") + "\n" + where(p.conds()) + "\n" +
			"GROUP BY trace_id" + p.havingClause(), nil
	}
	// Phase 1 names every column a having term or the order reads, so each
	// means here what it meant in the one-pass statement.
	ids := "SELECT TraceId AS trace_id,\n" +
		"       min(Timestamp) AS start,\n" +
		"       toFloat64(" + traceDurNanoExpr + ") / 1000000 AS dur_ms,\n" +
		"       argMin(SpanName, (ParentSpanId != '', Timestamp)) AS root_name,\n" +
		"       countIf(" + spanErrorExpr + ") AS error_count\n" +
		"FROM " + b.table(q.Scope, "otel_traces") + "\n" + where(p.conds()) + "\n" +
		"GROUP BY trace_id" + p.havingClause() + "\n" +
		"ORDER BY " + q.pageOrder() + "\n" +
		"LIMIT " + strconv.Itoa(q.Limit+1)
	page1 := "TraceId IN (SELECT trace_id FROM (\n    " + strings.ReplaceAll(ids, "\n", "\n    ") + "\n  ))"
	return tracePageCols + p.groupCols + "\n" +
		"FROM " + b.table(q.Scope, "otel_traces") + "\n" +
		where(append(append([]string{}, p.spans...), page1)) + "\n" +
		"GROUP BY trace_id", nil
}

// traceParts validates q and builds the WHERE and HAVING of its per-trace
// statements, binding their parameters.
func (q TraceQuery) traceParts(b *builder, page, withGroups bool) (traceParts, error) {
	if err := checkRumValue("user_id", q.UserID); err != nil {
		return traceParts{}, err
	}
	if q.SessionID != "" {
		if err := CheckSessionID(q.SessionID); err != nil {
			return traceParts{}, err
		}
	}
	for _, f := range [][2]string{{"browser", q.Browser}, {"os", q.OS}, {"frontend_version", q.FrontendVersion}, {"backend_version", q.BackendVersion}} {
		if err := checkRumValue(f[0], f[1]); err != nil {
			return traceParts{}, err
		}
	}
	spans := []string{b.rangeCond("Timestamp", q.Scope), "TraceId != ''"}
	// Resource-level filters pick the spans counted; span-level ones keep a
	// trace when ANY of its spans matches.
	sc, spanHaving, err := b.scopeParts(SigTraces, q.Scope, true)
	if err != nil {
		return traceParts{}, err
	}
	spans = append(spans, sc...)
	var candidates []string
	// Narrow the traces to aggregate before aggregating them. The per-trace
	// GROUP BY reads every span in the range, which over days of traffic
	// outlasts any request budget; a trace with an error, or whose root is
	// named X, has at least one span with an error status, or named X, so
	// these candidate sets are supersets of what the HAVING keeps and the
	// result is unchanged. (A root is the earliest parentless span, or the
	// earliest span of an orphaned trace, so the name test is over any span.)
	prefilter := func(pred string, narrow []string) {
		sub := append([]string{b.rangeCond("Timestamp", q.Scope), pred}, narrow...)
		candidates = append(candidates, "TraceId IN (SELECT TraceId FROM "+b.table(q.Scope, "otel_traces")+" "+where(sub)+")")
	}
	if q.ErrorsOnly {
		prefilter(spanErrorExpr, sc)
	}
	if q.RootSpanName != "" {
		prefilter("positionCaseInsensitive(SpanName, "+b.str(q.RootSpanName)+") > 0", sc)
	}
	// Who made the call and on which builds are facts of the WHOLE trace, held
	// by spans the scope's service and resource filters do not pick: a
	// browser's client and build are on its browser spans, a user on the
	// browser's or the server's, a backend build on the server's. So these
	// candidate sets are narrowed to the environment and instance only. Narrowed like the
	// two above, "the api's traces from Chrome" looked for Chrome on the api's
	// own spans, which never carry it, and matched nothing.
	var traceWide []string
	if q.UserID != "" || q.SessionID != "" || q.Browser != "" || q.OS != "" || q.FrontendVersion != "" || q.BackendVersion != "" {
		// Bound only when a filter uses it: an unused parameter is refused.
		if e := b.envCond(q.Scope); e != "" {
			traceWide = []string{e}
		}
		// An instance stamps every span it sends, its browser's included.
		if e := b.instanceCond(q.Scope); e != "" {
			traceWide = append(traceWide, e)
		}
	}
	if q.UserID != "" {
		prefilter(traceUserExpr+" = "+b.str(q.UserID), traceWide)
	}
	if q.SessionID != "" {
		prefilter(traceSessionExpr+" = "+b.str(q.SessionID), traceWide)
	}
	if q.Browser != "" {
		prefilter(traceBrowserSpan+" AND "+traceBrowserNameExpr+" = "+b.str(q.Browser), traceWide)
	}
	if q.OS != "" {
		prefilter(traceBrowserSpan+" AND "+traceOSNameExpr+" = "+b.str(q.OS), traceWide)
	}
	if q.FrontendVersion != "" {
		prefilter(traceBrowserSpan+" AND "+traceVersionExpr+" = "+b.str(q.FrontendVersion), traceWide)
	}
	if q.BackendVersion != "" {
		prefilter(traceServerSpan+" AND "+traceVersionExpr+" = "+b.str(q.BackendVersion), traceWide)
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
	if page && q.Cursor != nil {
		if q.Cursor.Finger != q.Fingerprint() {
			return traceParts{}, invalid("page_token does not belong to this query")
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
				return traceParts{}, err
			}
			groupCols += ",\n       argMin(toString(" + e + "), (ParentSpanId != '', Timestamp)) AS g" + strconv.Itoa(i)
		}
	}
	return traceParts{spans: spans, candidates: candidates, having: having, groupCols: groupCols}, nil
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
	return b.stmt(inner + "\nORDER BY " + q.pageOrder() + "\nLIMIT " + strconv.Itoa(q.Limit+1)), nil
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
	return b.stmt("SELECT Timestamp, SpanId, ParentSpanId, SpanName, SpanKind, ServiceName, Duration, StatusCode, StatusMessage,\n" +
		"       SpanAttributes, ResourceAttributes, `Events.Timestamp`, `Events.Name`, `Events.Attributes`, `Links.TraceId`, `Links.SpanId`\n" +
		"FROM " + b.table(q.Scope, "otel_traces") + "\n" + where(traceSpanConds(b, q.Scope, id)) + "\n" +
		"ORDER BY Timestamp, SpanId\nLIMIT " + strconv.Itoa(MaxSpansPerTrace+1)), nil
}

// traceSpanConds selects one trace's spans in the scope's range, env and
// instance: what every per-trace statement reads.
func traceSpanConds(b *builder, sc Scope, id string) []string {
	conds := []string{b.rangeCond("Timestamp", sc), "TraceId = " + b.str(id)}
	if e := b.envCond(sc); e != "" {
		conds = append(conds, e)
	}
	if e := b.instanceCond(sc); e != "" {
		conds = append(conds, e)
	}
	return conds
}

// MaxTraceSpanGroups caps the rows TraceSpanGroups returns.
const MaxTraceSpanGroups = 50

// TraceSpanGroups counts the spans of one whole trace, however many there
// are: TraceSpans stops at MaxSpansPerTrace, and a request that ran an N+1
// loop until its deadline has far more. Counting is cheap where reading every
// span is not, so a reader of the capped tree still learns what the trace
// holds beyond the read.
type TraceSpanGroups struct {
	Scope   Scope
	TraceID string
	// Siblings groups by parent span, name and status: each row is a run of
	// identical siblings under one parent (two or more), as the trace tree
	// collapses them ("×79427 UPDATE orders"). Without it the rows are the
	// trace's spans by service and name, wherever they hang.
	Siblings bool
}

// Compile builds the statement, largest group first, at most
// MaxTraceSpanGroups+1 rows (one more than the cap says it was hit).
// Columns: ParentSpanId, ServiceName, SpanName, StatusCode (the first and last
// empty without Siblings), spans in the group, the sum of every row's spans
// before the LIMIT (without Siblings: every span of the trace; with it, the
// spans in runs), sum / min / max of their Duration, quantiles(0.5, 0.95) of
// it, failing spans.
func (q TraceSpanGroups) Compile() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeTraces); err != nil {
		return Stmt{}, err
	}
	id, err := NormalizeTraceID(q.TraceID)
	if err != nil {
		return Stmt{}, err
	}
	b := newBuilder(q.Scope)
	keys, by, having := "'' AS parent, ServiceName, SpanName, '' AS status", "ServiceName, SpanName", ""
	if q.Siblings {
		// A run is two or more: a parent's lone child is not repetition, and
		// an N+1's per-row children (one UPDATE under each wrapper) would
		// otherwise be one row each.
		keys, by, having = "ParentSpanId AS parent, ServiceName, SpanName, StatusCode AS status", "ParentSpanId, ServiceName, SpanName, StatusCode", "\nHAVING n > 1"
	}
	return b.stmt("SELECT " + keys + ",\n" +
		"       count() AS n, sum(count()) OVER () AS trace_spans,\n" +
		"       sum(Duration) AS total_ns, min(Duration) AS min_ns, max(Duration) AS max_ns,\n" +
		"       quantiles(0.5, 0.95)(Duration) AS q_ns, countIf(" + spanErrorExpr + ") AS errors\n" +
		"FROM " + b.table(q.Scope, "otel_traces") + "\n" + where(traceSpanConds(b, q.Scope, id)) + "\n" +
		"GROUP BY " + by + having + "\n" +
		"ORDER BY n DESC, " + by + "\nLIMIT " + strconv.Itoa(MaxTraceSpanGroups+1)), nil
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

// MaxREDEndpoints bounds the rows REDByEndpoint is complete for. The
// statement asks for one row more, so a result of more than MaxREDEndpoints
// rows is a truncated endpoint set: some service's endpoints are missing.
const MaxREDEndpoints = 1000

// REDByEndpoint is RED split by endpoint (the server span's name), busiest
// first. Columns: ServiceName, endpoint, requests, errors, q (p50,p95,p99 in
// ms). Release health judges latency per endpoint from it
// (releasehealth.ServiceHealth.Endpoints), so a change in which endpoints a
// window holds does not read as a service getting slower. GroupBy is not
// applied. It returns at most MaxREDEndpoints+1 rows; more than
// MaxREDEndpoints means the set is truncated and must not be judged as whole.
func (q Overview) REDByEndpoint() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeOverview); err != nil {
		return Stmt{}, err
	}
	b := newBuilder(q.Scope)
	sc, err := b.scopeConds(SigTraces, q.Scope)
	if err != nil {
		return Stmt{}, err
	}
	conds := append([]string{b.rangeCond("Timestamp", q.Scope), spanServerExpr}, sc...)
	return b.stmt("SELECT ServiceName, SpanName AS endpoint, toUInt64(round(sum(" + spanWeightExpr + "))) AS requests, toUInt64(round(sumIf(" + spanWeightExpr + ", " + spanErrorExpr + "))) AS errors,\n" +
		"       quantilesExactWeightedIf(0.5, 0.95, 0.99)(Duration / 1000000, " + spanWeightInt + ", NOT " + spanStreamExpr + ") AS q\n" +
		"FROM " + b.table(q.Scope, "otel_traces") + "\n" + where(conds) + "\n" +
		"GROUP BY ServiceName, endpoint\nORDER BY requests DESC, ServiceName, endpoint\nLIMIT " + strconv.Itoa(MaxREDEndpoints+1)), nil
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
