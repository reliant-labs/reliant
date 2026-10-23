package obsquery

import (
	"crypto/sha256"
	"encoding/hex"
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
	// MaxMetricRows bounds the rows one QueryMetric statement may return; a
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
)

func (t MetricType) table() string {
	switch t {
	case MetricGauge:
		return "otel_metrics_gauge"
	case MetricSum:
		return "otel_metrics_sum"
	case MetricHistogram:
		return "otel_metrics_histogram"
	}
	return ""
}

// Agg is a metric aggregation.
type Agg int

// The aggregations. AggDefault resolves per type (AVG gauge, RATE sum, AVG
// histogram).
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

// ResolveAgg applies the per-type default and rejects combinations that have no
// meaning.
func ResolveAgg(t MetricType, a Agg) (Agg, error) {
	if a == AggDefault {
		switch t {
		case MetricSum:
			return AggRate, nil
		default:
			return AggAvg, nil
		}
	}
	if _, isQ := a.quantile(); isQ && t != MetricHistogram {
		return 0, invalid("percentile aggregations apply to histogram metrics only")
	}
	if (a == AggIncrease || a == AggRate) && t == MetricGauge {
		return 0, invalid("increase and rate apply to sum and histogram metrics, not gauges")
	}
	return a, nil
}

// StepForMetric picks the bucket width: the caller's, raised so a series never
// exceeds MaxPointsPerSeries; or about TargetPoints per series when unset.
func StepForMetric(rng time.Duration, requested int32) int64 {
	floor := int64(math.Ceil(rng.Seconds() / MaxPointsPerSeries))
	if floor < 1 {
		floor = 1
	}
	if requested > 0 {
		if int64(requested) < floor {
			return floor
		}
		return int64(requested)
	}
	return StepFor(rng, TargetPoints, floor)
}

// MetricList is a ListMetrics request.
type MetricList struct {
	Scope        Scope
	NameContains string
	Limit        int
}

// Compile builds the metric-name statement. Columns: MetricName, MetricUnit,
// MetricDescription, kind (1 gauge, 2 sum, 3 histogram).
func (q MetricList) Compile() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeMetrics); err != nil {
		return Stmt{}, err
	}
	if q.Limit <= 0 || q.Limit > ListMetricsMax {
		return Stmt{}, invalid("limit must be 1-%d", ListMetricsMax)
	}
	b := newBuilder(q.Scope)
	parts := make([]string, 0, 3)
	for _, t := range []MetricType{MetricGauge, MetricSum, MetricHistogram} {
		// Only RESOURCE-level filters narrow the picker: any other key is
		// valid for one specific metric only, and is checked when that metric
		// is queried.
		sc, err := b.resourceConds(SigMetrics, q.Scope)
		if err != nil {
			return Stmt{}, err
		}
		conds := append([]string{b.rangeCondSec("TimeUnix", q.Scope)}, sc...)
		if q.NameContains != "" {
			conds = append(conds, "positionCaseInsensitive(MetricName, "+b.str(q.NameContains)+") > 0")
		}
		parts = append(parts, "SELECT MetricName, any(MetricUnit) AS MetricUnit, any(MetricDescription) AS MetricDescription, "+
			strconv.Itoa(int(t))+" AS kind\nFROM "+b.table(q.Scope, t.table())+"\n"+where(conds)+"\nGROUP BY MetricName")
	}
	return b.stmt("SELECT * FROM (\n" + strings.Join(parts, "\nUNION ALL\n") + "\n) ORDER BY MetricName, kind LIMIT " + strconv.Itoa(q.Limit+1)), nil
}

// DeclaredKeys lists the attribute keys a metric's own data points carry,
// the extension to the key allowlist for metric filters and group-bys. It is
// scoped by environment and service only (the filters are what it enables).
// Column: key.
type DeclaredKeys struct {
	Scope Scope
	Name  string
	Type  MetricType
}

// MaxDeclaredKeys bounds the keys one metric may declare.
const MaxDeclaredKeys = 200

// Compile builds the declared-keys statement.
func (q DeclaredKeys) Compile() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeMetrics); err != nil {
		return Stmt{}, err
	}
	if q.Type.table() == "" {
		return Stmt{}, invalid("unknown metric type")
	}
	b := newBuilder(q.Scope)
	conds := append([]string{b.rangeCondSec("TimeUnix", q.Scope), "MetricName = " + b.str(q.Name)}, b.scopeBase(q.Scope)...)
	return b.stmt("SELECT arrayJoin(mapKeys(Attributes)) AS k FROM " + b.table(q.Scope, q.Type.table()) + " " + where(conds) +
		" GROUP BY k ORDER BY k LIMIT " + strconv.Itoa(MaxDeclaredKeys)), nil
}

// MetricLookup finds which table holds a metric. Columns: kind, unit, mono.
type MetricLookup struct {
	Scope Scope
	Name  string
}

// Compile builds the type-lookup statement.
func (q MetricLookup) Compile() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeMetrics); err != nil {
		return Stmt{}, err
	}
	b := newBuilder(q.Scope)
	parts := make([]string, 0, 3)
	for _, t := range []MetricType{MetricGauge, MetricSum, MetricHistogram} {
		// Which table holds a metric does not depend on the filters.
		conds := append([]string{b.rangeCondSec("TimeUnix", q.Scope), "MetricName = " + b.str(q.Name)}, b.scopeBase(q.Scope)...)
		mono := "toUInt8(0)"
		if t == MetricSum {
			mono = "toUInt8(max(IsMonotonic))"
		}
		parts = append(parts, "SELECT "+strconv.Itoa(int(t))+" AS kind, any(MetricUnit) AS unit, "+mono+" AS mono FROM "+b.table(q.Scope, t.table())+" "+where(conds)+" HAVING count() > 0")
	}
	return b.stmt("SELECT * FROM (\n" + strings.Join(parts, "\nUNION ALL\n") + "\n) ORDER BY kind LIMIT 3"), nil
}

// MetricQuery is a QueryMetric request.
type MetricQuery struct {
	Scope Scope
	Name  string
	Type  MetricType
	Agg   Agg
	// GroupBy are allowlisted keys, the metric's declared attributes
	// (Scope.Declared), or panel-vocabulary keys (metric.attr.<k>,
	// resource.attr.<k>).
	GroupBy []string
	// Where are dashboard-panel predicates (metric.attr.*, service.name,
	// resource.attr.*), ANDed with Scope.Filters.
	Where []PanelPredicate
	// StepSeconds is the resolved bucket width (see StepForMetric).
	StepSeconds int64
}

// Compile builds the time-series statement. Columns for every aggregation but
// the percentiles: bucket (DateTime), labels (Array(String), one per GroupBy
// key), value (Float64). For percentiles: bucket, labels, bounds
// (Array(Float64)), counts (Array(Float64)): the per-bucket bucket-count
// increases, from which HistogramQuantile reads the percentile.
func (q MetricQuery) Compile() (Stmt, error) {
	agg, err := q.validate()
	if err != nil {
		return Stmt{}, err
	}
	b := newBuilder(q.Scope)
	conds := []string{b.rangeCondSec("TimeUnix", q.Scope), "MetricName = " + b.str(q.Name)}
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
	// Group expressions, aliased g0..gN so they can be named in the outer query.
	var gSel, gNames []string
	for i, k := range q.GroupBy {
		if k == "" || len(k) > 128 {
			return Stmt{}, invalid("group_by keys must be 1-128 characters")
		}
		e, _, err := b.groupExpr(SigMetrics, k)
		if err != nil {
			return Stmt{}, err
		}
		n := "g" + strconv.Itoa(i)
		gSel = append(gSel, e+" AS "+n)
		gNames = append(gNames, n)
	}
	gSelList := ""
	if len(gSel) > 0 {
		gSelList = ", " + strings.Join(gSel, ", ")
	}
	gNameList := ""
	if len(gNames) > 0 {
		gNameList = ", " + strings.Join(gNames, ", ")
	}
	labels := "CAST([], 'Array(String)')"
	if len(gNames) > 0 {
		labels = "[" + strings.Join(gNames, ", ") + "]"
	}
	step := "toIntervalSecond(" + b.u32(uint32(q.StepSeconds)) + ")"
	bucket := "toStartOfInterval(TimeUnix, " + step + ")"
	from := b.table(q.Scope, q.Type.table())
	sid := "cityHash64(ResourceAttributes, Attributes)"
	if q.Type == MetricHistogram {
		sid = "cityHash64(ResourceAttributes, Attributes, ExplicitBounds)"
	}

	var sql string
	switch q.Type {
	case MetricGauge, MetricSum:
		switch {
		case agg == AggIncrease || agg == AggRate:
			expr := "sum(inc)"
			if agg == AggRate {
				expr = "sum(inc) / " + b.f64(float64(q.StepSeconds))
			}
			sql = "SELECT bucket, " + labels + " AS labels, " + expr + " AS value FROM (\n" +
				"  SELECT " + bucket + " AS bucket" + gNameList + ",\n" +
				"         multiIf(tmp = 1, Value, Value >= prev, Value - prev, Value) AS inc\n" +
				"  FROM (\n    SELECT TimeUnix, Value, AggregationTemporality AS tmp" + gSelList + ",\n" +
				"           lagInFrame(Value, 1, Value) OVER (PARTITION BY " + sid + " ORDER BY TimeUnix ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING) AS prev\n" +
				"    FROM " + from + "\n    " + where(conds) + "\n  )\n" +
				") GROUP BY bucket" + gNameList + " ORDER BY bucket" + gNameList + " LIMIT " + strconv.Itoa(MaxMetricRows)
		case agg == AggCount:
			sql = "SELECT bucket, " + labels + " AS labels, toFloat64(count()) AS value FROM (\n" +
				"  SELECT " + bucket + " AS bucket" + gSelList + "\n  FROM " + from + "\n  " + where(conds) + "\n" +
				") GROUP BY bucket" + gNameList + " ORDER BY bucket" + gNameList + " LIMIT " + strconv.Itoa(MaxMetricRows)
		default: // AVG, SUM, MIN, MAX: last value per series in the bucket, then across series
			fn := map[Agg]string{AggAvg: "avg", AggSum: "sum", AggMin: "min", AggMax: "max"}[agg]
			sql = "SELECT bucket, " + labels + " AS labels, " + fn + "(v) AS value FROM (\n" +
				"  SELECT " + bucket + " AS bucket" + gSelList + ", " + sid + " AS sid, argMax(Value, TimeUnix) AS v\n" +
				"  FROM " + from + "\n  " + where(conds) + "\n  GROUP BY bucket" + gNameList + ", sid\n" +
				") GROUP BY bucket" + gNameList + " ORDER BY bucket" + gNameList + " LIMIT " + strconv.Itoa(MaxMetricRows)
		}
	case MetricHistogram:
		if _, isQ := agg.quantile(); isQ {
			sql = "SELECT bucket, " + labels + " AS labels, bounds, sumForEach(incs) AS counts FROM (\n" +
				"  SELECT " + bucket + " AS bucket" + gNameList + ", ExplicitBounds AS bounds,\n" +
				"         arrayMap((c, p) -> multiIf(tmp = 1, toFloat64(c), Count >= pcount, greatest(toFloat64(c) - toFloat64(p), 0), toFloat64(c)), BucketCounts, pbuckets) AS incs\n" +
				"  FROM (\n    SELECT TimeUnix, Count, BucketCounts, ExplicitBounds, AggregationTemporality AS tmp" + gSelList + ",\n" +
				"           lagInFrame(Count, 1, Count) OVER w AS pcount,\n" +
				"           lagInFrame(BucketCounts, 1, BucketCounts) OVER w AS pbuckets\n" +
				"    FROM " + from + "\n    " + where(conds) + "\n" +
				"    WINDOW w AS (PARTITION BY " + sid + " ORDER BY TimeUnix ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING)\n  )\n" +
				") GROUP BY bucket" + gNameList + ", bounds ORDER BY bucket" + gNameList + " LIMIT " + strconv.Itoa(MaxMetricRows)
			break
		}
		var valueExpr string
		switch agg {
		case AggMin:
			valueExpr = "min(Min)"
		case AggMax:
			valueExpr = "max(Max)"
		default:
			incOf := func(col string) string {
				f := "toFloat64(" + col + ")"
				return "multiIf(tmp = 1, " + f + ", " + col + " >= p_" + col + ", " + f + " - toFloat64(p_" + col + "), " + f + ")"
			}
			switch agg {
			case AggSum:
				valueExpr = "sum(" + incOf("Sum") + ")"
			case AggCount, AggIncrease:
				valueExpr = "sum(" + incOf("Count") + ")"
			case AggRate:
				valueExpr = "sum(" + incOf("Count") + ") / " + b.f64(float64(q.StepSeconds))
			default: // AVG
				valueExpr = "if(sum(" + incOf("Count") + ") = 0, 0, sum(" + incOf("Sum") + ") / sum(" + incOf("Count") + "))"
			}
		}
		sql = "SELECT bucket, " + labels + " AS labels, toFloat64(" + valueExpr + ") AS value FROM (\n" +
			"  SELECT " + bucket + " AS bucket" + gNameList + ", tmp, Count, Sum, Min, Max, p_Count, p_Sum FROM (\n" +
			"    SELECT TimeUnix, AggregationTemporality AS tmp, Count, Sum, Min, Max" + gSelList + ",\n" +
			"           lagInFrame(Count, 1, Count) OVER w AS p_Count, lagInFrame(Sum, 1, Sum) OVER w AS p_Sum\n" +
			"    FROM " + from + "\n    " + where(conds) + "\n" +
			"    WINDOW w AS (PARTITION BY " + sid + " ORDER BY TimeUnix ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING)\n  )\n" +
			") GROUP BY bucket" + gNameList + " ORDER BY bucket" + gNameList + " LIMIT " + strconv.Itoa(MaxMetricRows)
	}
	return b.stmt(sql), nil
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
	if q.StepSeconds < 1 {
		return 0, invalid("step must be at least 1 second")
	}
	if int64(q.Scope.End.Sub(q.Scope.Start).Seconds())/q.StepSeconds > MaxPointsPerSeries {
		return 0, invalid("step %ds would return more than %d points per series", q.StepSeconds, MaxPointsPerSeries)
	}
	agg, err := ResolveAgg(q.Type, q.Agg)
	if err != nil {
		return 0, err
	}
	if q.Type.table() == "" {
		return 0, invalid("unknown metric type")
	}

	return agg, nil
}
