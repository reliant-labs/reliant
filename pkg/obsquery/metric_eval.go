package obsquery

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Temporality is how a sum or histogram's SDK exports its value; the values are
// the OTel AggregationTemporality enum.
type Temporality int32

// The temporalities.
const (
	TemporalityUnknown    Temporality = 0
	TemporalityDelta      Temporality = 1
	TemporalityCumulative Temporality = 2
)

func (t Temporality) String() string {
	switch t {
	case TemporalityDelta:
		return "delta"
	case TemporalityCumulative:
		return "cumulative"
	}
	return "unknown"
}

// Rows is the subset of a ClickHouse result obsquery reads (clickhouse-go's
// driver.Rows satisfies it; a backend with decoded rows adapts them). The
// caller closes a cursor it passes to a Parse* function.
type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close() error
}

// Runner executes a compiled statement on the caller's connection. obsquery
// opens nothing: the backend's connection, settings and access gate apply.
type Runner func(ctx context.Context, st Stmt) (Rows, error)

// MetricPoint is one bucket of a series. Null is a gap (a formula whose operand
// is absent); Partial says the bucket was still filling when it was read.
type MetricPoint struct {
	Time          time.Time
	Value         float64
	Null, Partial bool
}

// MetricSeriesResult is one group of one panel series (Label), or of the
// panel's "formula" or "ratio". A number, table or bar panel fills Reduced
// (nil: the reduction is null) and a number panel's Points are its sparkline.
type MetricSeriesResult struct {
	Label   string
	Labels  map[string]string
	Points  []MetricPoint
	Reduced *float64
}

// SeriesMeta describes one panel series after its metric was looked up and its
// aggregation resolved.
type SeriesMeta struct {
	Label, Metric string
	Info          MetricInfo
	Agg           Agg
	Quantile      float64
	ValueUnit     string
}

// NamedStmt is a compiled statement and what it is for (an explain's rows).
type NamedStmt struct {
	Purpose string
	Stmt    Stmt
}

// MetricPanelResult is what EvalMetricPanel returns.
type MetricPanelResult struct {
	Meta   []SeriesMeta
	Series []MetricSeriesResult
	// SeriesTotal is how many groups existed before the series cap.
	SeriesTotal int
	Truncated   bool
	StepSeconds int64
	// Warnings are "<code>" or "<code>: <detail>"; see WarningCode.
	Warnings []string
	// Absent are the metrics of panel series that have no data in the scope
	// and range (each with a metric_absent warning). Their series are empty.
	Absent     []string
	Statements []NamedStmt
}

// The warning codes.
const (
	WarnMixedBounds      = "mixed_bounds"
	WarnMixedTemporality = "mixed_temporality"
	WarnStepRaised       = "step_raised"
	WarnStoredAsNKinds   = "metric_stored_as_n_kinds"
	// WarnMetricAbsent: a series' metric has no data in the scope and range,
	// so the series is empty. Absence is a fact about the window, not a
	// malformed panel: the panel's other series are still drawn.
	WarnMetricAbsent = "metric_absent"
	// WarnKindMismatch: the series declares a metric kind the metric is not
	// stored as; the stored kind is read.
	WarnKindMismatch    = "metric_kind_mismatch"
	warnDetailSeparator = ": "
)

// WarningCode is the machine-readable part of a warning.
func WarningCode(w string) string {
	code, _, _ := strings.Cut(w, warnDetailSeparator)
	return code
}

// EvalOptions are the request-level knobs of EvalMetricPanel.
type EvalOptions struct {
	// StepSeconds is the requested bucket width; 0 picks one. It is raised to
	// the metrics' export interval, with a step_raised warning.
	StepSeconds int32
	// Explain compiles every statement and runs nothing (the Runner is not
	// called). The kind of each metric is taken from the series' metricType.
	Explain bool
	// Now decides which buckets are partial; zero is time.Now().
	Now time.Time
	// MaxSeries lowers the series cap (default MaxMetricSeries).
	MaxSeries int
	// Filters are ANDed onto every series' predicates.
	Filters []PanelPredicate
}

// PanelSeriesAgg maps a metrics series' dashspec aggregation onto an Agg and
// its quantile level.
func PanelSeriesAgg(ps PanelSeries) (Agg, float64, error) {
	switch ps.Agg {
	case "sum":
		return AggSum, 0, nil
	case "avg":
		return AggAvg, 0, nil
	case "min":
		return AggMin, 0, nil
	case "max":
		return AggMax, 0, nil
	case "last":
		return AggLast, 0, nil
	case "count":
		return AggCount, 0, nil
	case "rate":
		return AggRate, 0, nil
	case "increase":
		return AggIncrease, 0, nil
	case "quantile":
		if !(ps.Quantile > 0 && ps.Quantile <= 1) {
			return 0, 0, invalid("quantile must be in (0, 1]")
		}
		return AggQuantile, ps.Quantile, nil
	}
	return 0, 0, invalid("aggregation %q is not supported on metrics: use rate, increase, sum, avg, min, max, last, count or quantile", ps.Agg)
}

func metricTypeOf(s string) MetricType {
	switch s {
	case "gauge":
		return MetricGauge
	case "sum":
		return MetricSum
	case "histogram":
		return MetricHistogram
	case "exp_histogram":
		return MetricExpHistogram
	case "summary":
		return MetricSummary
	}
	return MetricUnknown
}

func (t MetricType) String() string {
	switch t {
	case MetricGauge:
		return "gauge"
	case MetricSum:
		return "sum"
	case MetricHistogram:
		return "histogram"
	case MetricExpHistogram:
		return "exp_histogram"
	case MetricSummary:
		return "summary"
	}
	return "unknown"
}

// ValueUnit is the unit of an aggregation's values: a rate is per second, a
// count is a count, everything else keeps the metric's unit.
func ValueUnit(info MetricInfo, agg Agg) string {
	per := func(u string) string {
		if u == "" {
			u = "1"
		}
		return u + "/s"
	}
	switch {
	case info.Type.hasCounts() && agg == AggRate:
		return "{count}/s"
	case info.Type.hasCounts() && (agg == AggCount || agg == AggIncrease):
		return "{count}"
	case agg == AggRate:
		return per(info.Unit)
	case agg == AggCount:
		return "{count}"
	}
	return info.Unit
}

type seriesPlan struct {
	ps       PanelSeries
	q        MetricQuery
	agg      Agg
	zeroFill bool
	// absent: the metric has no data in scope; nothing is compiled or run.
	absent bool
}

type groupData struct {
	labels map[string]string
	pts    map[int64]float64
}

type evaluator struct {
	run      Runner
	scope    Scope
	p        Panel
	o        EvalOptions
	plans    []seriesPlan
	res      MetricPanelResult
	warned   map[string]bool
	interval int64
	now      time.Time
	maxRows  bool
	totals   []int
}

func (ev *evaluator) warn(code, detail string) {
	w := code
	if detail != "" {
		w += warnDetailSeparator + detail
	}
	if !ev.warned[w] {
		ev.warned[w] = true
		ev.res.Warnings = append(ev.res.Warnings, w)
	}
}

func (ev *evaluator) stmt(purpose string, st Stmt) {
	ev.res.Statements = append(ev.res.Statements, NamedStmt{Purpose: purpose, Stmt: st})
}

// EvalMetricPanel runs a metrics panel end to end: look every series' metric
// up, resolve its aggregation, compile and run its statements through run, fold
// the rows into series, and combine them (formula or ratio). It is the one
// implementation of metric semantics: the control plane's QueryPanel and
// forge's local backend both call it. The panel's kind decides the shape: a
// line is a time series; a number is the range reduction plus a sparkline; a
// table or bar is the top groups by their range reduction.
//
// A series whose metric has no data in the scope and range is empty, with a
// metric_absent warning and its name in Absent, never an error: a counter that
// only exists once something fails, or a metric outside the selected
// environments, is "no data in this window", and the panel's other series
// still draw. Its declared aggregation is still validated against its declared
// kind.
func EvalMetricPanel(ctx context.Context, run Runner, scope Scope, p Panel, o EvalOptions) (MetricPanelResult, error) {
	if err := p.Validate(); err != nil {
		return MetricPanelResult{}, err
	}
	if p.Signal != "metrics" {
		return MetricPanelResult{}, invalid("EvalMetricPanel runs metrics panels, not %q", p.Signal)
	}
	if err := scope.validate(MaxRangeMetrics); err != nil {
		return MetricPanelResult{}, err
	}
	ev := &evaluator{run: run, scope: scope, p: p, o: o, warned: map[string]bool{}, now: o.Now}
	if ev.now.IsZero() {
		ev.now = time.Now()
	}
	if err := ev.plan(ctx); err != nil {
		return ev.res, err
	}
	rng := scope.End.Sub(scope.Start)
	step, raised := StepForMetric(rng, o.StepSeconds, ev.interval)
	ev.res.StepSeconds = step
	if raised {
		ev.warn(WarnStepRaised, fmt.Sprintf("%ds requested, %ds used (the metric is exported every %ds)", o.StepSeconds, step, ev.interval))
	}

	switch p.Kind {
	case "line":
		groups, err := ev.pass(ctx, false, step)
		if err != nil {
			return ev.res, err
		}
		ev.res.Series = ev.assemble(groups, false)
	case "number", "table", "bar":
		reduced, err := ev.pass(ctx, true, step)
		if err != nil {
			return ev.res, err
		}
		series := ev.assemble(reduced, true)
		if p.Kind == "number" && len(p.GroupBy) == 0 && (len(series) > 0 || o.Explain) {
			spark := SparklineStep(rng)
			if floor := max(ev.interval, 1); spark < floor {
				spark = niceStep(floor)
			}
			sgroups, err := ev.pass(ctx, false, spark)
			if err != nil {
				return ev.res, err
			}
			ev.attachSparkline(series, ev.assemble(sgroups, false))
		}
		if p.Kind != "number" {
			series = ev.topRows(series)
		}
		ev.res.Series = series
	default:
		return ev.res, invalid("a metrics panel is a line, number, table or bar, not %q", p.Kind)
	}
	ev.finish()
	return ev.res, nil
}

func (ev *evaluator) plan(ctx context.Context) error {
	for _, ps := range ev.p.Series {
		agg, ql, err := PanelSeriesAgg(ps)
		if err != nil {
			return fmt.Errorf("series %q: %w", ps.Label, err)
		}
		want := metricTypeOf(ps.MetricType)
		lookup := MetricLookup{Scope: ev.scope, Name: ps.Field}
		lst, err := lookup.Compile()
		if err != nil {
			return err
		}
		ev.stmt("lookup of "+ps.Field, lst)
		info := MetricInfo{Type: want, Monotonic: want == MetricSum, StoredKinds: 1}
		if !ev.o.Explain {
			rows, err := ev.run(ctx, lst)
			if err != nil {
				return err
			}
			if info, err = ParseLookup(rows, want); err != nil {
				if errors.Is(err, ErrMetricNotFound) {
					if err := ev.planAbsent(ps, agg, ql, want); err != nil {
						return err
					}
					continue
				}
				return err
			}
			if want != MetricUnknown && info.Type != want {
				ev.warn(WarnKindMismatch, fmt.Sprintf("%s is declared %s but stored as %s; reading %s", ps.Field, want, info.Type, info.Type))
			}
			if info.StoredKinds > 1 {
				ev.warn(WarnStoredAsNKinds, fmt.Sprintf("%s is stored as %d metric kinds; reading %s", ps.Field, info.StoredKinds, info.Type))
			}
			if info.MixedTemporality {
				ev.warn(WarnMixedTemporality, ps.Field+" carries both delta and cumulative points")
			}
		}
		resolved, err := ResolveAgg(info, agg)
		if err != nil {
			return fmt.Errorf("series %q: %w", ps.Label, err)
		}

		sc := ev.scope
		keys := append([]string{}, ev.p.GroupBy...)
		for _, f := range sc.Filters {
			keys = append(keys, f.Key)
		}
		if NeedsDeclared(keys...) {
			dst, err := DeclaredKeys{Scope: sc, Name: ps.Field, Type: info.Type}.Compile()
			if err != nil {
				return err
			}
			ev.stmt("attribute keys of "+ps.Field, dst)
			sc.Declared = map[string]bool{}
			if ev.o.Explain {
				for _, k := range keys {
					sc.Declared[k] = true
				}
			} else if err := ev.readDeclared(ctx, dst, sc.Declared); err != nil {
				return err
			}
		}

		where := append(append(append([]PanelPredicate{}, ev.p.Where...), ps.Where...), ev.o.Filters...)
		ev.plans = append(ev.plans, seriesPlan{
			ps: ps, agg: resolved,
			zeroFill: MetricQuery{Info: info}.family(resolved) == famCounter,
			q: MetricQuery{
				Scope: sc, Name: ps.Field, Info: info, Agg: resolved, Quantile: ql,
				GroupBy: ev.p.GroupBy, Where: where, MaxSeries: ev.seriesCap(),
			},
		})
		ev.interval = max(ev.interval, info.IntervalSeconds)
		ev.res.Meta = append(ev.res.Meta, SeriesMeta{
			Label: ps.Label, Metric: ps.Field, Info: info, Agg: resolved, Quantile: ql, ValueUnit: ValueUnit(info, resolved),
		})
	}
	return nil
}

// planAbsent records a series whose metric has no data in scope: an empty
// series with a warning. A declared kind still validates the aggregation, so a
// malformed panel is refused whether or not its metric has data.
func (ev *evaluator) planAbsent(ps PanelSeries, agg Agg, ql float64, want MetricType) error {
	info := MetricInfo{Type: want, Monotonic: want == MetricSum}
	resolved := agg
	if want != MetricUnknown {
		r, err := ResolveAgg(info, agg)
		if err != nil {
			return fmt.Errorf("series %q: %w", ps.Label, err)
		}
		resolved = r
	}
	where := "in this time range"
	if ev.scope.EnvRestricted {
		where = "in the selected environments in this time range"
	}
	ev.warn(WarnMetricAbsent, fmt.Sprintf("%s has no data %s", ps.Field, where))
	if !slices.Contains(ev.res.Absent, ps.Field) {
		ev.res.Absent = append(ev.res.Absent, ps.Field)
	}
	// An absent counter in a formula or ratio is zero (a failure counter that
	// was never incremented), as a missing bucket of a present one is.
	zeroFill := want != MetricUnknown && MetricQuery{Info: info}.family(resolved) == famCounter
	ev.plans = append(ev.plans, seriesPlan{ps: ps, agg: resolved, zeroFill: zeroFill, absent: true})
	ev.res.Meta = append(ev.res.Meta, SeriesMeta{
		Label: ps.Label, Metric: ps.Field, Info: info, Agg: resolved, Quantile: ql, ValueUnit: ValueUnit(info, resolved),
	})
	return nil
}

func (ev *evaluator) readDeclared(ctx context.Context, st Stmt, into map[string]bool) error {
	rows, err := ev.run(ctx, st)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return err
		}
		into[k] = true
	}
	return rows.Err()
}

// seriesCap is how many groups a series keeps: the panel kind's own cap,
// lowered by the request.
func (ev *evaluator) seriesCap() int {
	limit := MaxMetricSeries
	if ev.p.Kind == "table" || ev.p.Kind == "bar" {
		limit = ev.p.RowCap()
	}
	if ev.o.MaxSeries > 0 && ev.o.MaxSeries < limit {
		limit = ev.o.MaxSeries
	}
	return limit
}

func (ev *evaluator) pass(ctx context.Context, reduce bool, step int64) ([]map[string]*groupData, error) {
	out := make([]map[string]*groupData, len(ev.plans))
	ev.totals = make([]int, len(ev.plans))
	for i, pl := range ev.plans {
		if pl.absent {
			out[i] = map[string]*groupData{}
			continue
		}
		q := pl.q
		q.Reduce, q.StepSeconds = reduce, step
		st, err := q.Compile()
		if err != nil {
			return nil, err
		}
		purpose := "series " + pl.ps.Label
		switch {
		case reduce:
			purpose += " over the range"
		case ev.p.Kind == "number":
			purpose += " sparkline"
		}
		if ev.o.Explain && pl.q.Info.Type == MetricSum {
			purpose += " (assuming a monotonic sum)"
		}
		ev.stmt(purpose, st)
		if ev.o.Explain {
			out[i] = map[string]*groupData{}
			continue
		}
		g, total, err := ev.fold(ctx, st, q)
		if err != nil {
			return nil, err
		}
		out[i] = g
		ev.totals[i] = max(total, len(g))
		ev.res.SeriesTotal = max(ev.res.SeriesTotal, ev.totals[i])
	}
	return out, nil
}

type histAcc struct {
	gd     *groupData
	t      int64
	bounds [][]float64
	counts [][]float64
	exp    []expRow
}

// fold reads a statement's rows into groups keyed by label tuple.
func (ev *evaluator) fold(ctx context.Context, st Stmt, q MetricQuery) (map[string]*groupData, int, error) {
	rows, err := ev.run(ctx, st)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	fam := q.family(q.Agg)
	isQ, isExp, isSummaryQ := fam == famHistQuantile, fam == famHistQuantile && q.Info.Type == MetricExpHistogram, fam == famSummaryQuantile
	groups := map[string]*groupData{}
	hist := map[string]*histAcc{}
	var histOrder []string
	var seriesTotal uint64
	n := 0
	for rows.Next() {
		var (
			bucket         time.Time
			labels         []string
			value          float64
			bounds, counts []float64
			total          uint64
			exp            expRow
			nseries, nmiss uint64
		)
		switch {
		case isExp:
			err = rows.Scan(&bucket, &labels, &exp.scale, &exp.zero, &exp.pk, &exp.pv, &exp.nk, &exp.nv, &total)
		case isQ:
			err = rows.Scan(&bucket, &labels, &bounds, &counts, &total)
		case isSummaryQ:
			if err = rows.Scan(&bucket, &labels, &value, &total, &nseries, &nmiss); err == nil {
				err = summaryQuantileError(q.Name, q.QuantileLevel(), nseries, nmiss)
			}
		default:
			err = rows.Scan(&bucket, &labels, &value, &total)
		}
		if err != nil {
			return nil, 0, err
		}
		n++
		seriesTotal = max(seriesTotal, total)
		key := strings.Join(labels, "\x00")
		gd := groups[key]
		if gd == nil {
			gd = &groupData{labels: map[string]string{}, pts: map[int64]float64{}}
			for i, g := range q.GroupBy {
				if i < len(labels) {
					gd.labels[g] = labels[i]
				}
			}
			groups[key] = gd
		}
		t := bucket.Unix()
		if isQ {
			hk := key + "\x01" + strconv.FormatInt(t, 10)
			a := hist[hk]
			if a == nil {
				a = &histAcc{gd: gd, t: t}
				hist[hk] = a
				histOrder = append(histOrder, hk)
			}
			if isExp {
				a.exp = append(a.exp, exp)
			} else {
				a.bounds, a.counts = append(a.bounds, bounds), append(a.counts, counts)
			}
			continue
		}
		gd.pts[t] = value
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if n >= MaxMetricRows {
		ev.maxRows = true
	}
	for _, hk := range histOrder {
		a := hist[hk]
		if isExp {
			bounds, counts := mergeExpRows(a.exp).bounds()
			if v, ok := HistogramQuantile(bounds, counts, q.QuantileLevel()); ok {
				a.gd.pts[a.t] = v
			}
			continue
		}
		bounds, counts, mixed := MergeHistograms(a.bounds, a.counts)
		if mixed {
			ev.warn(WarnMixedBounds, q.Name+" has series with different explicit bounds; merged onto the union of bounds")
		}
		if v, ok := HistogramQuantile(bounds, counts, q.QuantileLevel()); ok {
			a.gd.pts[a.t] = v
		}
	}
	for k, gd := range groups {
		if len(gd.pts) == 0 {
			delete(groups, k)
		}
	}
	return groups, int(seriesTotal), nil
}

// MergeHistograms folds several (bounds, counts) of one bucket into one. When
// every source has the same bounds the counts are summed. Otherwise the result
// is on the union of bounds, each source bucket's count spread over the union
// intervals it covers in proportion to their width (the assumption
// HistogramQuantile makes inside a bucket); an open top bucket lands in the
// union's open top bucket. mixed reports that the bounds differed.
func MergeHistograms(bounds, counts [][]float64) (outBounds, outCounts []float64, mixed bool) {
	if len(bounds) == 0 {
		return nil, nil, false
	}
	same := true
	for _, b := range bounds[1:] {
		if !equalFloats(b, bounds[0]) {
			same = false
			break
		}
	}
	if same {
		sum := make([]float64, len(bounds[0])+1)
		for _, c := range counts {
			for i := 0; i < len(c) && i < len(sum); i++ {
				sum[i] += c[i]
			}
		}
		return bounds[0], sum, false
	}
	seen := map[float64]bool{}
	var union []float64
	for _, bs := range bounds {
		for _, b := range bs {
			if !seen[b] {
				seen[b] = true
				union = append(union, b)
			}
		}
	}
	sort.Float64s(union)
	merged := make([]float64, len(union)+1)
	lower := func(bs []float64, i int) float64 {
		if i == 0 {
			if bs[0] > 0 {
				return 0
			}
			return bs[0]
		}
		return bs[i-1]
	}
	for s, bs := range bounds {
		if len(counts[s]) != len(bs)+1 {
			continue
		}
		for i, c := range counts[s] {
			if c == 0 {
				continue
			}
			if i == len(bs) {
				merged[len(union)] += c
				continue
			}
			hiIdx := sort.SearchFloat64s(union, bs[i])
			loIdx := 0
			if i > 0 {
				loIdx = sort.SearchFloat64s(union, bs[i-1]) + 1
			}
			var width float64
			for j := loIdx; j <= hiIdx; j++ {
				width += union[j] - lower(union, j)
			}
			if width <= 0 {
				merged[hiIdx] += c
				continue
			}
			for j := loIdx; j <= hiIdx; j++ {
				merged[j] += c * (union[j] - lower(union, j)) / width
			}
		}
	}
	return union, merged, true
}

func equalFloats(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type combiner struct {
	label string
	eval  func(vals []float64, present []bool) (float64, bool)
}

func (ev *evaluator) combiner() *combiner {
	if ev.p.Ratio {
		return &combiner{label: "ratio", eval: func(v []float64, ok []bool) (float64, bool) {
			if !ok[0] || !ok[1] || v[1] == 0 {
				return 0, false
			}
			r := v[0] / v[1]
			return r, !math.IsNaN(r) && !math.IsInf(r, 0)
		}}
	}
	if f, err := ev.p.ParsedFormula(); err == nil && f != nil {
		return &combiner{label: "formula", eval: f.EvalNullable}
	}
	return nil
}

func total(g *groupData) float64 {
	var t float64
	for _, v := range g.pts {
		t += math.Abs(v)
	}
	return t
}

func sortedGroupKeys(m map[string]*groupData) []string {
	keys := make([]string, 0, len(m))
	totals := make(map[string]float64, len(m))
	for k, g := range m {
		keys = append(keys, k)
		totals[k] = total(g)
	}
	SortByTotal(keys, totals)
	return keys
}

func sortedTimes(m map[int64]float64) []int64 {
	ts := make([]int64, 0, len(m))
	for t := range m {
		ts = append(ts, t)
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i] < ts[j] })
	return ts
}

// assemble turns a pass's groups into result series: each panel series' own, or
// the panel's one combined formula/ratio. reduced marks a one-bucket pass.
func (ev *evaluator) assemble(groups []map[string]*groupData, reduced bool) []MetricSeriesResult {
	var out []MetricSeriesResult
	emit := func(label string, g *groupData, times []int64, at func(t int64) (float64, bool)) {
		r := MetricSeriesResult{Label: label, Labels: g.labels}
		for _, t := range times {
			v, ok := at(t)
			pt := MetricPoint{Time: time.Unix(t, 0).UTC(), Value: v, Null: !ok}
			if !ok || math.IsNaN(v) || math.IsInf(v, 0) {
				pt.Value, pt.Null = 0, true
			}
			if !reduced {
				end := pt.Time.Add(time.Duration(ev.res.StepSeconds) * time.Second)
				pt.Partial = end.After(ev.now.Add(-time.Duration(ev.interval) * time.Second))
			}
			r.Points = append(r.Points, pt)
		}
		if reduced && len(r.Points) == 1 && !r.Points[0].Null {
			v := r.Points[0].Value
			r.Reduced = &v
		}
		out = append(out, r)
	}
	c := ev.combiner()
	if c == nil {
		for i, pl := range ev.plans {
			for _, k := range sortedGroupKeys(groups[i]) {
				g := groups[i][k]
				emit(pl.ps.Label, g, sortedTimes(g.pts), func(t int64) (float64, bool) { v, ok := g.pts[t]; return v, ok })
			}
		}
		return out
	}
	union := map[string]*groupData{}
	for _, gs := range groups {
		for k, g := range gs {
			if _, ok := union[k]; !ok {
				union[k] = g
			}
		}
	}
	combinedTotal := map[string]float64{}
	for k := range union {
		for _, gs := range groups {
			if g := gs[k]; g != nil {
				combinedTotal[k] += total(g)
			}
		}
	}
	keys := make([]string, 0, len(union))
	for k := range union {
		keys = append(keys, k)
	}
	SortByTotal(keys, combinedTotal)
	for _, k := range keys {
		times := map[int64]float64{}
		for _, gs := range groups {
			if g := gs[k]; g != nil {
				for t := range g.pts {
					times[t] = 0
				}
			}
		}
		vals, present := make([]float64, len(ev.plans)), make([]bool, len(ev.plans))
		emit(c.label, union[k], sortedTimes(times), func(t int64) (float64, bool) {
			for i, pl := range ev.plans {
				v, ok := 0.0, false
				if g := groups[i][k]; g != nil {
					v, ok = g.pts[t]
				}
				if !ok && pl.zeroFill {
					v, ok = 0, true
				}
				vals[i], present[i] = v, ok
			}
			return c.eval(vals, present)
		})
	}
	return out
}

// attachSparkline gives each reduced series (ungrouped number panel) the points
// of the matching sparkline series.
func (ev *evaluator) attachSparkline(reduced, spark []MetricSeriesResult) {
	for i := range reduced {
		for _, s := range spark {
			if s.Label == reduced[i].Label {
				reduced[i].Points = s.Points
				break
			}
		}
	}
}

// topRows orders each label's groups by their reduction, largest first, and
// keeps the panel's row cap per label; labels keep their series order.
func (ev *evaluator) topRows(series []MetricSeriesResult) []MetricSeriesResult {
	order := map[string]int{}
	for i, pl := range ev.plans {
		order[pl.ps.Label] = i
	}
	order["formula"], order["ratio"] = 0, 0
	val := func(s MetricSeriesResult) float64 {
		if s.Reduced == nil {
			return math.Inf(-1)
		}
		return *s.Reduced
	}
	sort.SliceStable(series, func(i, j int) bool {
		if oi, oj := order[series[i].Label], order[series[j].Label]; oi != oj {
			return oi < oj
		}
		return val(series[i]) > val(series[j])
	})
	kept, per := series[:0], map[string]int{}
	for _, s := range series {
		s.Points = nil
		if per[s.Label] < ev.seriesCap() {
			kept = append(kept, s)
		}
		per[s.Label]++
	}
	return kept
}

// finish settles Truncated against what is returned.
func (ev *evaluator) finish() {
	emitted := map[string]int{}
	for _, s := range ev.res.Series {
		emitted[s.Label]++
	}
	most := 0
	for _, n := range emitted {
		most = max(most, n)
	}
	ev.res.SeriesTotal = max(ev.res.SeriesTotal, most)
	ev.res.Truncated = ev.maxRows || ev.res.SeriesTotal > most
}
