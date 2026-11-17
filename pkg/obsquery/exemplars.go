package obsquery

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// MaxExemplars bounds one ListMetricExemplars answer and one stored-trace probe.
const MaxExemplars = 20

// exemplarLag is how far past the requested window a data point is read: an
// exemplar is exported with the first data point after it was recorded, and
// the SDK exports every 30-60 seconds.
const exemplarLag = 5 * time.Minute

// traceProbeMargin widens the stored-trace probe around the exemplar window:
// a trace may start before, and end after, the observation that points at it.
const traceProbeMargin = time.Hour

// MetricExemplars is a ListMetricExemplars request: the sampled observations
// recorded with a metric's data points, newest first.
type MetricExemplars struct {
	Scope Scope
	Name  string
	Kind  CatalogKind
	// Where narrows to series matching the predicates (a panel's vocabulary).
	Where []PanelPredicate
	// Group narrows to one series group: group-by key to value.
	Group map[string]string
	// From and To bound the exemplars' own timestamps (the bucket the user
	// clicked), inside the scope's range; both zero means the scope's range.
	From, To time.Time
	// MinValue keeps only exemplars at or above it (the tail of a latency
	// histogram).
	MinValue *float64
	// Limit is at most MaxExemplars; zero means MaxExemplars.
	Limit int
}

func (q MetricExemplars) window() (from, to time.Time) {
	if q.From.IsZero() {
		return q.Scope.Start, q.Scope.End
	}
	return q.From, q.To
}

// Compile builds the exemplar statement. Columns: time (DateTime), value
// (Float64), trace_id (String, lowercase), span_id (String), attributes
// (Map(String, String)). Newest first, one row per (trace, span), only
// exemplars that carry a trace id.
func (q MetricExemplars) Compile() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeOverview); err != nil {
		return Stmt{}, err
	}
	if q.Name == "" || len(q.Name) > 256 {
		return Stmt{}, invalid("metric_name must be 1-256 characters")
	}
	switch q.Kind {
	case CatalogGauge, CatalogSum, CatalogHistogram, CatalogExpHistogram:
	case CatalogSummary:
		return Stmt{}, invalid("a summary metric records no exemplars")
	default:
		return Stmt{}, invalid("unknown metric kind")
	}
	limit := q.Limit
	if limit == 0 {
		limit = MaxExemplars
	}
	if limit < 0 || limit > MaxExemplars {
		return Stmt{}, invalid("limit must be 1-%d", MaxExemplars)
	}
	if (q.From.IsZero()) != (q.To.IsZero()) {
		return Stmt{}, invalid("from and to are given together")
	}
	from, to := q.window()
	if !to.After(from) {
		return Stmt{}, invalid("to must be after from")
	}
	if from.Before(q.Scope.Start) || to.After(q.Scope.End) {
		return Stmt{}, invalid("the exemplar window must lie inside the time range")
	}
	if q.MinValue != nil && (math.IsNaN(*q.MinValue) || math.IsInf(*q.MinValue, 0)) {
		return Stmt{}, invalid("min_value must be a finite number")
	}
	if len(q.Where) > MaxPanelPredicates {
		return Stmt{}, invalid("at most %d predicates", MaxPanelPredicates)
	}
	if len(q.Group) > MaxMetricGroupBy {
		return Stmt{}, invalid("a group has at most %d keys", MaxMetricGroupBy)
	}

	b := newBuilder(q.Scope)
	pointEnd := to.Add(exemplarLag)
	if pointEnd.After(q.Scope.End) {
		pointEnd = q.Scope.End
	}
	pointScope := q.Scope
	pointScope.Start, pointScope.End = from, pointEnd
	conds := []string{b.rangeCondSec("TimeUnix", pointScope), "MetricName = " + b.str(q.Name)}
	sc, err := b.scopeConds(SigMetrics, q.Scope)
	if err != nil {
		return Stmt{}, err
	}
	conds = append(conds, sc...)
	wc, err := b.conds("metrics", q.Where)
	if err != nil {
		return Stmt{}, err
	}
	conds = append(conds, wc...)
	keys := make([]string, 0, len(q.Group))
	for k := range q.Group {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := q.Group[k]
		if len(v) > maxPanelValueLen {
			return Stmt{}, invalid("group value is too long")
		}
		e, _, err := b.groupExpr(SigMetrics, k)
		if err != nil {
			return Stmt{}, err
		}
		conds = append(conds, e+" = "+b.str(v))
	}
	win := Scope{Start: from, End: to}
	conds = append(conds, "ex_trace != ''", b.rangeCondSec("ex_time", win))
	if q.MinValue != nil {
		conds = append(conds, "ex_value >= "+b.f64(*q.MinValue))
	}
	sql := "SELECT ex_time, ex_value, lower(ex_trace) AS trace_id, ex_span, ex_attrs\n" +
		"FROM " + b.table(q.Scope, q.Kind.table()) + "\n" +
		"ARRAY JOIN `Exemplars.TimeUnix` AS ex_time, `Exemplars.Value` AS ex_value, `Exemplars.TraceId` AS ex_trace, " +
		"`Exemplars.SpanId` AS ex_span, `Exemplars.FilteredAttributes` AS ex_attrs\n" +
		where(conds) + "\nORDER BY ex_time DESC, ex_value DESC\nLIMIT 1 BY trace_id, ex_span\nLIMIT " + strconv.Itoa(limit)
	return b.stmt(sql), nil
}

// Exemplar is one sampled observation of a metric.
type Exemplar struct {
	Time       time.Time
	Value      float64
	TraceID    string
	SpanID     string
	Attributes map[string]string
	// Stored: the trace is in the trace store. false means it was sampled out
	// and the trace view would be empty.
	Stored bool
}

// ParseExemplars reads MetricExemplars' rows. The caller closes rows.
func ParseExemplars(rows Rows) ([]Exemplar, error) {
	out := []Exemplar{}
	for rows.Next() {
		var e Exemplar
		if err := rows.Scan(&e.Time, &e.Value, &e.TraceID, &e.SpanID, &e.Attributes); err != nil {
			return nil, err
		}
		e.TraceID = strings.ToLower(e.TraceID)
		if e.Attributes == nil {
			e.Attributes = map[string]string{}
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// MarkStored sets Stored from the ids ParseStoredTraceIDs found and returns a
// copy with the stored exemplars first, each group keeping its order (newest
// first).
func MarkStored(in []Exemplar, stored map[string]bool) []Exemplar {
	out := make([]Exemplar, len(in))
	for i, e := range in {
		e.Stored = stored[e.TraceID]
		out[i] = e
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Stored && !out[j].Stored })
	return out
}

// ExemplarsStored is the probe for which of a set of trace ids are in the trace
// store. It is not narrowed to the scope's environments: the ids come from
// exemplars the caller was already allowed to read, and it reveals presence
// only.
type ExemplarsStored struct {
	Scope    Scope
	TraceIDs []string
	// From and To are the exemplar window; both zero means the scope's range.
	// The probe reads one hour either side.
	From, To time.Time
	// UseLookupTable reads the otel_traces_trace_id_ts lookup table (a hosted
	// backend has it) instead of otel_traces itself (the local ClickStack).
	UseLookupTable bool
}

// Compile builds the probe. Column: trace_id (String), each stored id once.
func (q ExemplarsStored) Compile() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeOverview); err != nil {
		return Stmt{}, err
	}
	if len(q.TraceIDs) == 0 || len(q.TraceIDs) > MaxExemplars {
		return Stmt{}, invalid("trace_ids must name 1-%d traces", MaxExemplars)
	}
	ids := make([]string, len(q.TraceIDs))
	for i, id := range q.TraceIDs {
		n, err := NormalizeTraceID(id)
		if err != nil {
			return Stmt{}, err
		}
		ids[i] = n
	}
	from, to := q.From, q.To
	if from.IsZero() && to.IsZero() {
		from, to = q.Scope.Start, q.Scope.End
	}
	if from.IsZero() || to.IsZero() || !to.After(from) {
		return Stmt{}, invalid("to must be after from")
	}
	b := newBuilder(q.Scope)
	arr := b.strArray(ids)
	lo, hi := from.Add(-traceProbeMargin), to.Add(traceProbeMargin)
	if q.UseLookupTable {
		w := Scope{Start: lo, End: hi}
		return b.stmt("SELECT DISTINCT TraceId AS trace_id FROM " + b.table(q.Scope, "otel_traces_trace_id_ts") +
			"\n" + where([]string{"TraceId IN " + arr, b.rangeCondSec("Start", w)})), nil
	}
	w := Scope{Start: lo, End: hi}
	return b.stmt("SELECT DISTINCT TraceId AS trace_id FROM " + b.table(q.Scope, "otel_traces") +
		"\n" + where([]string{"TraceId IN " + arr, b.rangeCond("Timestamp", w)})), nil
}

// ParseStoredTraceIDs reads ExemplarsStored's rows into a set of lowercase ids.
func ParseStoredTraceIDs(rows Rows) (map[string]bool, error) {
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[strings.ToLower(id)] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
