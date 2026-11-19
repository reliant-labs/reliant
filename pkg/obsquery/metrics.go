package obsquery

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

func hashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:16]
}

// Metric limits.
const (
	MaxMetricGroupBy   = 3
	MaxMetricSeries    = 50
	MaxPointsPerSeries = 1500
	TargetPoints       = 120
	// MaxMetricRows bounds the rows one metric statement may return; a
	// result that reaches it is reported as truncated.
	MaxMetricRows = MaxMetricSeries * MaxPointsPerSeries * 2
	// ListMetricsDefault and ListMetricsMax bound the metric picker.
	ListMetricsDefault = 200
	ListMetricsMax     = 500
)

// MetricType is the OTel metric kind a metric is stored as.
type MetricType int

// The supported metric kinds.
const (
	MetricUnknown MetricType = iota
	MetricGauge
	MetricSum
	MetricHistogram
	// MetricExpHistogram and MetricSummary equal their CatalogKind.
	MetricExpHistogram
	MetricSummary
)

func (t MetricType) table() string {
	switch t {
	case MetricGauge:
		return "otel_metrics_gauge"
	case MetricSum:
		return "otel_metrics_sum"
	case MetricHistogram:
		return "otel_metrics_histogram"
	case MetricExpHistogram:
		return "otel_metrics_exponential_histogram"
	case MetricSummary:
		return "otel_metrics_summary"
	}
	return ""
}

// MetricInfo is what one lookup statement learns about a metric: which table
// holds it and the facts the semantics depend on (see MetricLookup).
type MetricInfo struct {
	Type MetricType
	Unit string
	// Monotonic is a Sum's IsMonotonic. An up-down counter is not.
	Monotonic        bool
	Temporality      Temporality
	MixedTemporality bool
	// Series is the number of distinct series in the lookup window; it
	// decides whether the series cap is applied in SQL.
	Series int64
	// IntervalSeconds estimates the export interval; it is the floor of the
	// bucket width.
	IntervalSeconds int64
	// StoredKinds is how many metric kinds hold this name (normally 1).
	StoredKinds int
}

// Agg is a metric aggregation.
type Agg int

// The aggregations. AggDefault resolves per kind (see ResolveAgg).
// AggQuantile carries MetricQuery.Quantile (any level in (0, 1]); AggP50,
// AggP95 and AggP99 are aliases of it for those levels.
const (
	AggDefault Agg = iota
	AggAvg
	AggSum
	AggMin
	AggMax
	AggCount
	AggIncrease
	AggRate
	AggP50
	AggP95
	AggP99
	AggQuantile
	AggLast
)

func (a Agg) quantile() (float64, bool) {
	switch a {
	case AggP50:
		return 0.5, true
	case AggP95:
		return 0.95, true
	case AggP99:
		return 0.99, true
	}
	return 0, false
}

func (a Agg) isQuantile() bool {
	_, alias := a.quantile()
	return alias || a == AggQuantile
}

// ResolveAgg applies the per-kind default and refuses the combinations that
// have no meaning (the table in DESIGN §3.2):
//
//	monotonic sum   default rate; sum IS increase; count and quantile refused
//	up-down sum     default sum (of last values); rate, increase, count and quantile refused
//	gauge           default avg; rate, increase and quantile refused
//	histogram       default p95; every aggregation but last
//	exp histogram   as a histogram
//	summary         default avg; avg, sum, count, increase and rate over its Count and
//	                Sum; quantile only for a level every series stores (checked against
//	                the data); min, max and last refused
//
// A monotonic sum resolves AggSum to AggIncrease, so one word never means two
// things.
func ResolveAgg(info MetricInfo, a Agg) (Agg, error) {
	t := info.Type
	if t.table() == "" {
		return 0, invalid("unknown metric type")
	}
	if a == AggDefault {
		switch {
		case t == MetricSum && info.Monotonic:
			return AggRate, nil
		case t == MetricSum:
			return AggSum, nil
		case t == MetricHistogram || t == MetricExpHistogram:
			return AggP95, nil
		}
		return AggAvg, nil
	}
	if a.isQuantile() && t != MetricHistogram && t != MetricExpHistogram && t != MetricSummary {
		return 0, invalid("percentile aggregations apply to histogram and summary metrics only")
	}
	switch t {
	case MetricGauge:
		if a == AggRate || a == AggIncrease {
			return 0, invalid("increase and rate apply to monotonic sums and histograms, not gauges")
		}
	case MetricSum:
		if a == AggCount {
			return 0, invalid("count applies to gauges (data points) and histograms (observations), not sums: use rate or increase")
		}
		if !info.Monotonic && (a == AggRate || a == AggIncrease) {
			return 0, invalid("rate and increase are refused for an up-down counter (a sum that is not monotonic): use sum, avg, min, max or last")
		}
		if a == AggSum && info.Monotonic {
			return AggIncrease, nil
		}
	case MetricHistogram, MetricExpHistogram:
		if a == AggLast {
			return 0, invalid("last is not defined over a histogram: use a quantile, avg, min, max, sum, count or rate")
		}
	case MetricSummary:
		if a == AggMin || a == AggMax || a == AggLast {
			return 0, invalid("a summary keeps no minimum, maximum or last value: use avg, sum, count, increase, rate or a quantile it stores")
		}
	}
	return a, nil
}

// niceSteps are the bucket widths a step is rounded up to.
var niceSteps = []int64{1, 5, 10, 15, 30, 60, 120, 300, 600, 900, 1800, 3600, 7200, 21600, 43200, 86400}

// floorNice rounds an estimated export interval down to a nice step: a hole in
// a 60s series makes its mean gap 69s, and the interval is 60. Too low only
// leaves a step gappy (rates are time-weighted); too high would hide data.
func floorNice(sec int64) int64 {
	out := int64(0)
	for _, s := range niceSteps {
		if s > sec {
			break
		}
		out = s
	}
	return out
}

func niceStep(atLeast int64) int64 {
	for _, s := range niceSteps {
		if s >= atLeast {
			return s
		}
	}
	return 86400
}

// StepForMetric picks the bucket width. The floor is the larger of the width
// that keeps a series within MaxPointsPerSeries and the export interval, so a
// bucket never holds less than one sample. The caller's step is honoured at or
// above the floor; below it, the step is raised and raised is true (the
// response says so). Unset, it is about TargetPoints per series.
func StepForMetric(rng time.Duration, requested int32, intervalSec int64) (step int64, raised bool) {
	floor := int64(math.Ceil(rng.Seconds() / MaxPointsPerSeries))
	if floor < 1 {
		floor = 1
	}
	if intervalSec > floor {
		floor = intervalSec
	}
	if requested > 0 {
		if int64(requested) < floor {
			return niceStep(floor), true
		}
		return int64(requested), false
	}
	return StepFor(rng, TargetPoints, floor), false
}

// ErrMetricNotFound is ParseLookup's refusal when no table holds the metric in
// the lookup window. It is not ErrInvalid: the request was well-formed.
var ErrMetricNotFound = errors.New("metric not found")

// MetricLookup finds which tables hold a metric and what is known about it.
// Columns: kind, unit, mono, temporality, mixed, series, points, first, last,
// interval. ParseLookup turns the rows into a MetricInfo.
type MetricLookup struct {
	Scope Scope
	Name  string
}

// Compile builds the lookup statement. interval is the median over series of
// the mean gap between a series' points: unlike (last-first)/points it does not
// grow with series churn over a long range.
func (q MetricLookup) Compile() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeMetrics); err != nil {
		return Stmt{}, err
	}
	b := newBuilder(q.Scope)
	parts := make([]string, 0, 3)
	for _, t := range []MetricType{MetricGauge, MetricSum, MetricHistogram, MetricExpHistogram, MetricSummary} {
		// Which table holds a metric does not depend on the filters.
		conds := append([]string{b.rangeCondSec("TimeUnix", q.Scope), "MetricName = " + b.str(q.Name)}, b.scopeBase(q.Scope)...)
		mono, tmp, mixed := "toUInt8(0)", "toInt32(0)", "toUInt8(0)"
		sid := "cityHash64(ResourceAttributes, Attributes)"
		switch t {
		case MetricSum:
			mono = "toUInt8(max(IsMonotonic))"
			tmp, mixed = "toInt32(any(AggregationTemporality))", "toUInt8(uniq(AggregationTemporality) > 1)"
		case MetricHistogram:
			tmp, mixed = "toInt32(any(AggregationTemporality))", "toUInt8(uniq(AggregationTemporality) > 1)"
			sid = "cityHash64(ResourceAttributes, Attributes, ExplicitBounds)"
		case MetricExpHistogram:
			tmp, mixed = "toInt32(any(AggregationTemporality))", "toUInt8(uniq(AggregationTemporality) > 1)"
		}
		from := b.table(q.Scope, t.table()) + " " + where(conds)
		interval := "(SELECT toUInt64(ifNotFinite(round(medianExact(gap)), 0)) FROM (SELECT (max(TimeUnix) - min(TimeUnix)) / (count() - 1) AS gap FROM " +
			from + " GROUP BY " + sid + " HAVING count() > 1))"
		parts = append(parts, "SELECT "+strconv.Itoa(int(t))+" AS kind, any(MetricUnit) AS unit, "+mono+" AS mono, "+tmp+" AS temporality, "+mixed+" AS mixed, "+
			"uniq("+sid+") AS series, count() AS points, min(TimeUnix) AS first, max(TimeUnix) AS last, "+interval+" AS interval FROM "+from+" HAVING count() > 0")
	}
	return b.stmt("SELECT * FROM (\n" + strings.Join(parts, "\nUNION ALL\n") + "\n) ORDER BY kind LIMIT 5"), nil
}

// ParseLookup reads MetricLookup's rows into the info of the requested kind, or
// of the first kind stored when want is MetricUnknown or is not stored. It
// closes rows. No rows is ErrMetricNotFound.
func ParseLookup(rows Rows, want MetricType) (MetricInfo, error) {
	defer func() { _ = rows.Close() }()
	var infos []MetricInfo
	for rows.Next() {
		var (
			kind, mono, mixed uint8
			unit              string
			tmp               int32
			series, points    uint64
			first, last       time.Time
			interval          uint64
		)
		if err := rows.Scan(&kind, &unit, &mono, &tmp, &mixed, &series, &points, &first, &last, &interval); err != nil {
			return MetricInfo{}, err
		}
		infos = append(infos, MetricInfo{
			Type: MetricType(kind), Unit: unit, Monotonic: mono == 1, Temporality: Temporality(tmp), MixedTemporality: mixed == 1,
			Series: int64(series), IntervalSeconds: floorNice(int64(interval)),
		})
	}
	if err := rows.Err(); err != nil {
		return MetricInfo{}, err
	}
	if len(infos) == 0 {
		return MetricInfo{}, ErrMetricNotFound
	}
	out := infos[0]
	for _, i := range infos {
		if i.Type == want {
			out = i
		}
	}
	out.StoredKinds = len(infos)
	return out, nil
}

// MetricQuery is one metric statement: a series' time-series (or, with Reduce,
// its one-bucket reduction) over Scope's range.
type MetricQuery struct {
	Scope Scope
	Name  string
	// Info is what MetricLookup found (see ParseLookup); the kind selects the
	// table and the statement shape, and Info.Series decides the SQL series cap.
	Info MetricInfo
	// Agg is resolved against Info (ResolveAgg), so AggDefault is allowed.
	Agg Agg
	// Quantile is the level for AggQuantile, in (0, 1].
	Quantile float64
	// GroupBy are allowlisted keys, the metric's declared attributes
	// (Scope.Declared), or panel-vocabulary keys (metric.attr.<k>,
	// resource.attr.<k>).
	GroupBy []string
	// Where are dashboard-panel predicates (metric.attr.*, service.name,
	// resource.attr.*), ANDed with Scope.Filters.
	Where []PanelPredicate
	// StepSeconds is the resolved bucket width (see StepForMetric). Unused
	// with Reduce.
	StepSeconds int64
	// MaxSeries caps a grouped query in SQL at the top MaxSeries groups; 0 is
	// MaxMetricSeries, and the most a table or bar panel may ask is MaxTableRows.
	MaxSeries int
	// Reduce compiles one bucket spanning the whole range: a rate becomes the
	// range-average rate, a quantile is over the merged range, a level
	// aggregation is the last value.
	Reduce bool
}

type metricFamily int

const (
	famLevel metricFamily = iota
	famCounter
	famHistAvg
	famHistMinMax
	famHistQuantile
	famSummaryQuantile
)

func (q MetricQuery) family(agg Agg) metricFamily {
	switch q.Info.Type {
	case MetricSum:
		if agg == AggRate || agg == AggIncrease {
			return famCounter
		}
	case MetricHistogram, MetricExpHistogram, MetricSummary:
		switch {
		case agg.isQuantile() && q.Info.Type == MetricSummary:
			return famSummaryQuantile
		case agg.isQuantile():
			return famHistQuantile
		case agg == AggAvg:
			return famHistAvg
		case agg == AggMin || agg == AggMax:
			return famHistMinMax
		}
		return famCounter
	}
	return famLevel
}

// QuantileLevel is the level a resolved histogram quantile aggregation reads.
func (q MetricQuery) QuantileLevel() float64 {
	if l, ok := q.Agg.quantile(); ok {
		return l
	}
	return q.Quantile
}

func (q MetricQuery) seriesCap() int {
	switch {
	case q.MaxSeries <= 0:
		return MaxMetricSeries
	case q.MaxSeries > MaxTableRows:
		return MaxTableRows
	}
	return q.MaxSeries
}

// Capped reports whether the statement keeps only the top seriesCap groups in
// SQL: it is grouped and the metric has more series than the cap.
func (q MetricQuery) Capped() bool {
	return len(q.GroupBy) > 0 && q.Info.Series > int64(q.seriesCap())
}

// validate checks the request and returns the resolved aggregation.
func (q MetricQuery) validate() (Agg, error) {
	if err := q.Scope.validate(MaxRangeMetrics); err != nil {
		return 0, err
	}
	if q.Name == "" || len(q.Name) > 256 {
		return 0, invalid("metric_name must be 1-256 characters")
	}
	if len(q.GroupBy) > MaxMetricGroupBy {
		return 0, invalid("at most %d group_by keys", MaxMetricGroupBy)
	}
	if q.MaxSeries < 0 {
		return 0, invalid("max_series must not be negative")
	}
	if !q.Reduce {
		if q.StepSeconds < 1 {
			return 0, invalid("step must be at least 1 second")
		}
		if int64(q.Scope.End.Sub(q.Scope.Start).Seconds())/q.StepSeconds > MaxPointsPerSeries {
			return 0, invalid("step %ds would return more than %d points per series", q.StepSeconds, MaxPointsPerSeries)
		}
	}
	agg, err := ResolveAgg(q.Info, q.Agg)
	if err != nil {
		return 0, err
	}
	if agg == AggQuantile && !(q.Quantile > 0 && q.Quantile <= 1) {
		return 0, invalid("quantile must be in (0, 1]")
	}
	return agg, nil
}

// lookbackSeconds is how far before the range the read starts so the first
// in-range sample has its predecessor: twice the export interval, and at least
// one step.
func (q MetricQuery) lookbackSeconds() int64 {
	lb := max(2*q.Info.IntervalSeconds, q.StepSeconds)
	if q.Reduce {
		lb = max(2*q.Info.IntervalSeconds, 60)
	}
	if lb < 1 {
		lb = 60
	}
	return lb
}

// Compile builds the statement. Columns for every aggregation but a histogram
// quantile: bucket (DateTime), labels (Array(String), one per GroupBy key),
// value (Float64), series_total (UInt64; the group count before the cap when
// Capped, else 0). For a histogram quantile: bucket, labels, bounds
// (Array(Float64)), counts (Array(Float64)), series_total; one row per
// distinct bound set in a (bucket, labels), which MergeHistograms folds.
//
// Counters are rates per series first, Σinc/Σdt over the series' samples in the
// bucket, and then summed across series. A cumulative sample's increment is its
// value less its predecessor's, or its whole value when it follows a reset (the
// StartTimeUnix moved, or the counter decreased); a delta sample's is its value.
func (q MetricQuery) Compile() (Stmt, error) {
	agg, err := q.validate()
	if err != nil {
		return Stmt{}, err
	}
	fam := q.family(agg)
	b := newBuilder(q.Scope)
	from := b.table(q.Scope, q.Info.Type.table())

	readFrom := q.Scope.Start
	if fam == famCounter || fam == famHistAvg || fam == famHistQuantile {
		readFrom = readFrom.Add(-time.Duration(q.lookbackSeconds()) * time.Second)
	}
	conds := []string{"TimeUnix >= " + b.secTime(readFrom) + " AND TimeUnix < " + b.secTime(q.Scope.End), "MetricName = " + b.str(q.Name)}
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

	var gExprs, gSel, gNames []string
	for i, k := range q.GroupBy {
		if k == "" || len(k) > 128 {
			return Stmt{}, invalid("group_by keys must be 1-128 characters")
		}
		e, _, err := b.groupExpr(SigMetrics, k)
		if err != nil {
			return Stmt{}, err
		}
		n := "g" + strconv.Itoa(i)
		gExprs, gNames = append(gExprs, e), append(gNames, n)
		gSel = append(gSel, e+" AS "+n)
	}
	gSelList, gNameList := "", ""
	if len(gSel) > 0 {
		gSelList, gNameList = ", "+strings.Join(gSel, ", "), ", "+strings.Join(gNames, ", ")
	}
	labels := "CAST([], 'Array(String)')"
	if len(gNames) > 0 {
		labels = "[" + strings.Join(gNames, ", ") + "]"
	}

	total := "toUInt64(0)"
	if q.Capped() {
		tuple := gExprs[0]
		if len(gExprs) > 1 {
			tuple = "(" + strings.Join(gExprs, ", ") + ")"
		}
		rank := "sum(abs(Value))"
		if q.Info.Type.hasCounts() {
			rank = "sum(Count)"
		}
		total = "(SELECT uniqExact(" + tuple + ") FROM " + from + " " + where(conds) + ")"
		top := "SELECT " + strings.Join(gSel, ", ") + " FROM " + from + " " + where(conds) +
			" GROUP BY " + strings.Join(gNames, ", ") + " ORDER BY " + rank + " DESC, " + strings.Join(gNames, ", ") + " LIMIT " + strconv.Itoa(q.seriesCap())
		conds = append(append([]string{}, conds...), tuple+" IN ("+top+")")
	}

	var bucket string
	multiplier := float64(q.StepSeconds)
	if q.Reduce {
		bucket = "toDateTime(" + b.u32(uint32(q.Scope.Start.Unix())) + ")"
		multiplier = q.Scope.End.Sub(q.Scope.Start).Seconds()
	} else {
		bucket = "toStartOfInterval(TimeUnix, toIntervalSecond(" + b.u32(uint32(q.StepSeconds)) + "))"
	}
	sid := "cityHash64(ResourceAttributes, Attributes)"
	if q.Info.Type == MetricHistogram {
		sid = "cityHash64(ResourceAttributes, Attributes, ExplicitBounds)"
	}
	// Bound only where used: the lookback read pulls in samples before Start
	// that the outer layer must drop.
	startCond := ""
	if fam == famCounter || fam == famHistAvg || fam == famHistQuantile {
		startCond = "TimeUnix >= " + b.secTime(q.Scope.Start)
	}
	tail := " GROUP BY bucket" + gNameList + " ORDER BY bucket" + gNameList + " LIMIT " + strconv.Itoa(MaxMetricRows)
	window := "WINDOW w AS (PARTITION BY " + sid + " ORDER BY TimeUnix ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING)"

	var sql string
	switch fam {
	case famLevel:
		var lvl, across string
		switch {
		case q.Info.Type == MetricGauge && agg == AggCount:
			lvl, across = "toFloat64(count())", "sum(v)"
		case q.Info.Type == MetricGauge:
			lvl = "argMax(Value, TimeUnix)"
		default:
			// A delta sum's level over a bucket is what arrived in it, not its last arrival.
			lvl = "if(countIf(AggregationTemporality = 1) > 0, sumIf(Value, AggregationTemporality = 1), argMax(Value, TimeUnix))"
		}
		if across == "" {
			across = map[Agg]string{AggAvg: "avg(v)", AggSum: "sum(v)", AggMin: "min(v)", AggMax: "max(v)", AggLast: "argMax(v, lt)"}[agg]
		}
		sql = "SELECT bucket, " + labels + " AS labels, toFloat64(" + across + ") AS value, " + total + " AS series_total FROM (\n" +
			"  SELECT " + bucket + " AS bucket" + gSelList + ", " + sid + " AS sid, " + lvl + " AS v, max(TimeUnix) AS lt\n" +
			"  FROM " + from + "\n  " + where(conds) + "\n  GROUP BY bucket" + gNameList + ", sid\n" +
			")" + tail

	case famCounter:
		vCol, cCol := "Value", "Value"
		if q.Info.Type.hasCounts() {
			vCol, cCol = "Count", "Count"
			if agg == AggSum {
				vCol = "Sum"
			}
		}
		mult := ""
		if agg != AggRate {
			mult = " * " + b.f64(multiplier)
		}
		sql = "SELECT bucket, " + labels + " AS labels, toFloat64(sum(r)" + mult + ") AS value, " + total + " AS series_total FROM (\n" +
			"  SELECT " + bucket + " AS bucket" + gNameList + ", sid, sum(inc) / sum(dt) AS r FROM (\n" +
			q.incrementsSQL(vCol, cCol, gSelList, sid, from, conds, window) + "\n  )\n" +
			"  WHERE (tmp = 1 OR NOT is_first) AND dt > 0 AND " + startCond + "\n  GROUP BY bucket" + gNameList + ", sid\n" +
			")" + tail

	case famHistAvg:
		sql = "SELECT bucket, " + labels + " AS labels, sum(incS) / sum(incC) AS value, " + total + " AS series_total FROM (\n" +
			"  SELECT " + bucket + " AS bucket" + gNameList + ", incS, incC FROM (\n" +
			"    SELECT TimeUnix, tmp, is_first, " + strings.TrimPrefix(gNameList, ", ") + commaIf(gNameList) + "\n" +
			"           if(tmp = 1 OR is_reset, S, S - pS) AS incS, if(tmp = 1 OR is_reset, c, c - pc) AS incC FROM (\n" +
			"      SELECT TimeUnix, tmp, S, c, pS, pc, isNaN(pc) AS is_first, (NOT isNaN(pc)) AND (StartTimeUnix != pStart OR c < pc) AS is_reset" + gNameList + " FROM (\n" +
			"        SELECT TimeUnix, StartTimeUnix, " + q.tmpExpr() + " AS tmp, toFloat64(Sum) AS S, toFloat64(Count) AS c" + gSelList + ",\n" +
			"               lagInFrame(toFloat64(Sum), 1, nan) OVER w AS pS, lagInFrame(toFloat64(Count), 1, nan) OVER w AS pc, lagInFrame(StartTimeUnix, 1, toDateTime(0)) OVER w AS pStart\n" +
			"        FROM " + from + "\n        " + where(conds) + "\n        " + window + "\n      )\n    )\n" +
			"    WHERE (tmp = 1 OR NOT is_first) AND " + startCond + "\n  )\n" +
			") GROUP BY bucket" + gNameList + " HAVING sum(incC) > 0 ORDER BY bucket" + gNameList + " LIMIT " + strconv.Itoa(MaxMetricRows)

	case famHistMinMax:
		fn := map[Agg]string{AggMin: "min(Min)", AggMax: "max(Max)"}[agg]
		sql = "SELECT bucket, " + labels + " AS labels, toFloat64(" + fn + ") AS value, " + total + " AS series_total FROM (\n" +
			"  SELECT " + bucket + " AS bucket" + gSelList + ", Min, Max FROM " + from + "\n  " + where(conds) + "\n" +
			")" + tail

	case famSummaryQuantile:
		sql = q.summaryQuantileSQL(sqlParts{b: b, bucket: bucket, labels: labels, total: total, gSelList: gSelList, gNameList: gNameList, from: from, sid: sid, conds: conds}, q.QuantileLevel())

	case famHistQuantile:
		if q.Info.Type == MetricExpHistogram {
			sql = q.expQuantileSQL(sqlParts{b: b, bucket: bucket, labels: labels, total: total, gSelList: gSelList, gNameList: gNameList, from: from, sid: sid, startCond: startCond, conds: conds})
			break
		}
		sql = "SELECT bucket, " + labels + " AS labels, bounds, sumForEach(incs) AS counts, " + total + " AS series_total FROM (\n" +
			"  SELECT " + bucket + " AS bucket" + gNameList + ", bounds, incs FROM (\n" +
			"    SELECT TimeUnix, bounds, is_first, tmp, arrayMap((x, p) -> if(tmp = 1 OR is_reset, x, greatest(x - p, 0)), cur, prev) AS incs" + gNameList + " FROM (\n" +
			"      SELECT TimeUnix, tmp, bounds, cur, prev, isNaN(pc) AS is_first, (NOT isNaN(pc)) AND (StartTimeUnix != pStart OR c < pc) AS is_reset" + gNameList + " FROM (\n" +
			"        SELECT TimeUnix, StartTimeUnix, AggregationTemporality AS tmp, ExplicitBounds AS bounds, arrayMap(x -> toFloat64(x), BucketCounts) AS cur, toFloat64(Count) AS c" + gSelList + ",\n" +
			"               lagInFrame(toFloat64(Count), 1, nan) OVER w AS pc, lagInFrame(StartTimeUnix, 1, toDateTime(0)) OVER w AS pStart,\n" +
			"               lagInFrame(arrayMap(x -> toFloat64(x), BucketCounts), 1, arrayMap(x -> toFloat64(x), BucketCounts)) OVER w AS prev\n" +
			"        FROM " + from + "\n        " + where(conds) + "\n        " + window + "\n      )\n    )\n" +
			"    WHERE (tmp = 1 OR NOT is_first) AND " + startCond + "\n  )\n" +
			") GROUP BY bucket" + gNameList + ", bounds ORDER BY bucket" + gNameList + " LIMIT " + strconv.Itoa(MaxMetricRows)
	}
	return b.stmt(sql), nil
}

func commaIf(s string) string {
	if s == "" {
		return ""
	}
	return ","
}

// incrementsSQL is the per-sample layer of a counter statement: each sample's
// increment (inc) and the time it covers (dt), with is_first marking a
// cumulative sample that has no predecessor. v is the counter column, c the
// column whose decrease marks a reset (the count, for a histogram).
func (q MetricQuery) incrementsSQL(v, c, gSelList, sid, from string, conds []string, window string) string {
	gNames := ""
	for i := range q.GroupBy {
		gNames += ", g" + strconv.Itoa(i)
	}
	return "    SELECT TimeUnix, tmp, is_first, sid" + gNames + ",\n" +
		"           if(tmp = 1 OR is_reset, v, v - pv) AS inc,\n" +
		"           if(tmp = 1 OR (is_reset AND StartTimeUnix > pt), dateDiff('second', StartTimeUnix, TimeUnix), dateDiff('second', pt, TimeUnix)) AS dt FROM (\n" +
		"      SELECT TimeUnix, StartTimeUnix, tmp, v, pv, pt, sid" + gNames + ", isNaN(pc) AS is_first, (NOT isNaN(pc)) AND (StartTimeUnix != ps OR c < pc) AS is_reset FROM (\n" +
		"        SELECT TimeUnix, StartTimeUnix, " + q.tmpExpr() + " AS tmp, toFloat64(" + v + ") AS v, toFloat64(" + c + ") AS c, " + sid + " AS sid" + gSelList + ",\n" +
		"               lagInFrame(toFloat64(" + v + "), 1, nan) OVER w AS pv, lagInFrame(toFloat64(" + c + "), 1, nan) OVER w AS pc,\n" +
		"               lagInFrame(TimeUnix, 1, toDateTime(0)) OVER w AS pt, lagInFrame(StartTimeUnix, 1, toDateTime(0)) OVER w AS ps\n" +
		"        FROM " + from + "\n        " + where(conds) + "\n        " + window + "\n      )\n    )"
}

// HistogramQuantile estimates the q-th quantile from explicit bucket bounds and
// per-bucket counts (len(counts) == len(bounds)+1), interpolating linearly
// inside the bucket the rank falls in. The first bucket's lower edge is 0 when
// its upper bound is positive; the open top bucket reports the last bound.
// ok is false when there are no observations.
func HistogramQuantile(bounds []float64, counts []float64, q float64) (float64, bool) {
	if len(counts) != len(bounds)+1 || len(counts) == 0 {
		return 0, false
	}
	var total float64
	for _, c := range counts {
		total += c
	}
	if total <= 0 {
		return 0, false
	}
	rank := q * total
	var cum float64
	for i, c := range counts {
		if c <= 0 {
			continue
		}
		if cum+c < rank && i < len(counts)-1 {
			cum += c
			continue
		}
		switch {
		case i == len(bounds): // open top bucket
			if len(bounds) == 0 {
				return 0, true
			}
			return bounds[len(bounds)-1], true
		default:
			lo := 0.0
			if i > 0 {
				lo = bounds[i-1]
			} else if bounds[0] < 0 {
				lo = bounds[0]
			}
			hi := bounds[i]
			return lo + (hi-lo)*((rank-cum)/c), true
		}
	}
	return bounds[len(bounds)-1], true
}

// SortByTotal orders series keys by descending total, ties by key, for the
// series cap.
func SortByTotal(keys []string, total map[string]float64) {
	sort.Slice(keys, func(i, j int) bool {
		if total[keys[i]] != total[keys[j]] {
			return total[keys[i]] > total[keys[j]]
		}
		return keys[i] < keys[j]
	})
}
