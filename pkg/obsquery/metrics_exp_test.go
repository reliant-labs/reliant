package obsquery

import (
	"context"
	"math"
	"reflect"
	"strings"
	"testing"
)

// anyRows hands back rows of exactly the Go types the real columns scan into.
type anyRows struct {
	rows [][]any
	i    int
}

func (r *anyRows) Next() bool { r.i++; return r.i <= len(r.rows) }
func (r *anyRows) Err() error { return nil }
func (r *anyRows) Close() error {
	return nil
}
func (r *anyRows) Scan(dest ...any) error {
	row := r.rows[r.i-1]
	if len(dest) != len(row) {
		panic("scan: wrong column count")
	}
	for i, d := range dest {
		reflect.ValueOf(d).Elem().Set(reflect.ValueOf(row[i]))
	}
	return nil
}

func kindRunner(kind MetricType, series [][]any) Runner {
	return func(_ context.Context, st Stmt) (Rows, error) {
		switch {
		case strings.Contains(st.SQL, "AS kind"):
			return &fakeRows{rows: [][]any{lookupRow(kind, "s", false, 2, 1, 60)}}, nil
		case strings.Contains(st.SQL, "mapKeys"):
			return &fakeRows{rows: [][]any{{"pod"}}}, nil
		}
		return &anyRows{rows: series}, nil
	}
}

func approxSlice(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if math.Abs(a[i]-b[i]) > 1e-12 {
			return false
		}
	}
	return true
}

func TestExpBucketsToBounds(t *testing.T) {
	for _, tc := range []struct {
		name       string
		e          expMerged
		wantBounds []float64
		wantCounts []float64
	}{
		{"scale 0 positive buckets are (1,2] (2,4] (4,8]",
			expMerged{scale: 0, pos: map[int64]float64{0: 1, 1: 2, 2: 3}},
			[]float64{1, 2, 4, 8}, []float64{0, 1, 2, 3, 0}},
		{"scale 1 halves the exponent: base sqrt2",
			expMerged{scale: 1, pos: map[int64]float64{0: 5}},
			[]float64{1, math.Sqrt2}, []float64{0, 5, 0}},
		{"scale -1 quadruples: base 4",
			expMerged{scale: -1, pos: map[int64]float64{0: 5}},
			[]float64{1, 4}, []float64{0, 5, 0}},
		{"negative buckets are mirrored, ascending",
			expMerged{scale: 0, neg: map[int64]float64{0: 1, 1: 2}},
			[]float64{-4, -2, -1}, []float64{0, 2, 1, 0}},
		{"the zero count is a point at 0",
			expMerged{scale: 0, zero: 5},
			[]float64{0, 0}, []float64{0, 5, 0}},
		{"a gap between populated buckets gets one empty bucket, not a dense run",
			expMerged{scale: 0, pos: map[int64]float64{0: 1, 3: 4}},
			[]float64{1, 2, 8, 16}, []float64{0, 1, 0, 4, 0}},
		{"negatives, zero and positives in one histogram",
			expMerged{scale: 0, neg: map[int64]float64{0: 1}, zero: 2, pos: map[int64]float64{0: 3}},
			[]float64{-2, -1, 0, 0, 1, 2}, []float64{0, 1, 0, 2, 0, 3, 0}},
		{"empty counts are dropped", expMerged{scale: 0, pos: map[int64]float64{0: 0}}, nil, nil},
	} {
		b, c := tc.e.bounds()
		if !approxSlice(b, tc.wantBounds) || !approxSlice(c, tc.wantCounts) {
			t.Errorf("%s:\n bounds %v want %v\n counts %v want %v", tc.name, b, tc.wantBounds, c, tc.wantCounts)
		}
		if len(b) > 0 && len(c) != len(b)+1 {
			t.Errorf("%s: %d counts for %d bounds", tc.name, len(c), len(b))
		}
	}
}

func TestExpQuantilesFromExponentialBuckets(t *testing.T) {
	// 1 in (1,2], 2 in (2,4], 3 in (4,8]
	e := expMerged{scale: 0, pos: map[int64]float64{0: 1, 1: 2, 2: 3}}
	b, c := e.bounds()
	for _, tc := range []struct{ q, want float64 }{
		{0.5, 4},    // rank 3 is the top edge of (2,4]
		{0.25, 2.5}, // rank 1.5: a quarter into (2,4]
		{1, 8},
	} {
		got, ok := HistogramQuantile(b, c, tc.q)
		if !ok || math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("q%v = %v (%v), want %v", tc.q, got, ok, tc.want)
		}
	}
	// the zero bucket answers 0 exactly; the negative side answers below it
	z := expMerged{scale: 0, neg: map[int64]float64{0: 1}, zero: 2, pos: map[int64]float64{0: 1}}
	b, c = z.bounds()
	if got, _ := HistogramQuantile(b, c, 0.5); got != 0 {
		t.Errorf("median of {-1.5, 0, 0, 1.5} = %v, want 0", got)
	}
	if got, _ := HistogramQuantile(b, c, 0.1); got >= 0 {
		t.Errorf("p10 = %v, want a negative value", got)
	}
}

func TestMergeExpRowsMixedScale(t *testing.T) {
	fine := expRow{scale: 2, pk: []int64{0, 1, 2, 3}, pv: []float64{1, 1, 1, 1}}
	coarse := expRow{scale: 0, pk: []int64{0, 1}, pv: []float64{4, 6}, zero: 1}
	got := mergeExpRows([]expRow{fine, coarse})
	if got.scale != 0 {
		t.Fatalf("scale = %d, want the minimum, 0", got.scale)
	}
	if got.pos[0] != 8 || got.pos[1] != 6 || got.zero != 1 {
		t.Errorf("merged = %+v: buckets 0..3 at scale 2 all fall in bucket 0 at scale 0", got)
	}
	if mergeExpRows([]expRow{fine}).scale != 2 {
		t.Error("one row keeps its own scale")
	}
	// the shift rounds toward negative infinity: -1>>1 = -1, -2>>1 = -1, -3>>1 = -2
	neg := mergeExpRows([]expRow{{scale: 1, nk: []int64{-1, -2, -3}, nv: []float64{1, 2, 4}}, {scale: 0, nk: []int64{-1}, nv: []float64{8}}})
	if neg.neg[-1] != 8+1+2 || neg.neg[-2] != 4 {
		t.Errorf("negative index downscale = %+v", neg.neg)
	}
}

func TestResolveAggExpHistogramAndSummary(t *testing.T) {
	exp := MetricInfo{Type: MetricExpHistogram}
	sum := MetricInfo{Type: MetricSummary}
	for _, tc := range []struct {
		name    string
		info    MetricInfo
		in, out Agg
		bad     bool
	}{
		{"exp default is p95 (owner decision O3)", exp, AggDefault, AggP95, false},
		{"exp any quantile", exp, AggQuantile, AggQuantile, false},
		{"exp p50 alias", exp, AggP50, AggP50, false},
		{"exp rate", exp, AggRate, AggRate, false},
		{"exp count", exp, AggCount, AggCount, false},
		{"exp avg", exp, AggAvg, AggAvg, false},
		{"exp sum", exp, AggSum, AggSum, false},
		{"exp min", exp, AggMin, AggMin, false},
		{"exp max", exp, AggMax, AggMax, false},
		{"exp last refused", exp, AggLast, 0, true},
		{"summary default is avg", sum, AggDefault, AggAvg, false},
		{"summary quantile is checked against the data, not here", sum, AggQuantile, AggQuantile, false},
		{"summary rate", sum, AggRate, AggRate, false},
		{"summary count", sum, AggCount, AggCount, false},
		{"summary increase", sum, AggIncrease, AggIncrease, false},
		{"summary sum", sum, AggSum, AggSum, false},
		{"summary min refused", sum, AggMin, 0, true},
		{"summary max refused", sum, AggMax, 0, true},
		{"summary last refused", sum, AggLast, 0, true},
	} {
		got, err := ResolveAgg(tc.info, tc.in)
		if tc.bad != (err != nil) || (!tc.bad && got != tc.out) {
			t.Errorf("%s: ResolveAgg = %v, %v", tc.name, got, err)
		}
		if tc.bad && !isInvalid(err) {
			t.Errorf("%s: not InvalidArgument: %v", tc.name, err)
		}
	}
}

func TestExpAndSummaryTypeNames(t *testing.T) {
	for _, tc := range []struct {
		s string
		t MetricType
		k CatalogKind
	}{{"exp_histogram", MetricExpHistogram, CatalogExpHistogram}, {"summary", MetricSummary, CatalogSummary}} {
		if metricTypeOf(tc.s) != tc.t || tc.t.String() != tc.s {
			t.Errorf("%q <-> %v", tc.s, tc.t)
		}
		if int(tc.t) != int(tc.k) || tc.t.table() != tc.k.table() {
			t.Errorf("%v differs from its catalog kind %v", tc.t, tc.k)
		}
	}
}

func TestMetricLookupCoversAllFiveKinds(t *testing.T) {
	st, err := MetricLookup{Scope: scope(), Name: "m"}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	assertParameterized(t, st)
	for _, tbl := range []string{"otel_metrics_gauge", "otel_metrics_sum", "otel_metrics_histogram", "otel_metrics_exponential_histogram", "otel_metrics_summary"} {
		if !strings.Contains(st.SQL, tbl) {
			t.Errorf("lookup omits %s:\n%s", tbl, st.SQL)
		}
	}
	info, err := ParseLookup(&fakeRows{rows: [][]any{lookupRow(MetricExpHistogram, "ms", false, 1, 4, 60)}}, MetricHistogram)
	if err != nil || info.Type != MetricExpHistogram || info.Temporality != TemporalityDelta {
		t.Errorf("a panel that says histogram reads the exp histogram the name is stored as: %+v, %v", info, err)
	}
}

func TestCatalogQueryableForEveryKind(t *testing.T) {
	for _, k := range catalogKinds {
		if !k.Queryable() {
			t.Errorf("%s is not queryable", k)
		}
	}
	if CatalogKind(0).Queryable() || CatalogKind(9).Queryable() {
		t.Error("an unknown kind is queryable")
	}
}

func expQuery(agg Agg, q float64) MetricQuery {
	return MetricQuery{Scope: scope(), Name: "lat", Info: MetricInfo{Type: MetricExpHistogram, IntervalSeconds: 60, Temporality: TemporalityCumulative}, Agg: agg, Quantile: q, StepSeconds: 60, GroupBy: []string{"metric.attr.pod"}}
}

func TestCompileExpHistogramQuantile(t *testing.T) {
	st, err := expQuery(AggQuantile, 0.9).Compile()
	if err != nil {
		t.Fatal(err)
	}
	assertParameterized(t, st)
	for _, want := range []string{"otel_metrics_exponential_histogram", "PositiveBucketCounts", "NegativeBucketCounts", "ZeroCount", "Scale", "mapSubtract", "sumMap"} {
		if !strings.Contains(st.SQL, want) {
			t.Errorf("statement lacks %s:\n%s", want, st.SQL)
		}
	}
	if strings.Contains(st.SQL, "ExplicitBounds") {
		t.Errorf("an exponential histogram has no ExplicitBounds:\n%s", st.SQL)
	}
	if n := strings.Count(st.SQL, "TimeUnix >= "); n != 2 {
		t.Errorf("want the lookback read and the range filter, got %d TimeUnix >= conditions\n%s", n, st.SQL)
	}
	q := expQuery(AggQuantile, 0.9)
	q.Reduce, q.StepSeconds, q.GroupBy = true, 0, nil
	st, err = q.Compile()
	if err != nil {
		t.Fatalf("reduce: %v", err)
	}
	assertParameterized(t, st)
	if _, err := expQuery(AggQuantile, 0).Compile(); !isInvalid(err) {
		t.Errorf("quantile 0 accepted: %v", err)
	}
	q = expQuery(AggQuantile, 0.9)
	q.Info.Series = 500
	st, err = q.Compile()
	if err != nil {
		t.Fatalf("capped: %v", err)
	}
	if strings.Contains(st.SQL, "abs(Value)") || !strings.Contains(st.SQL, "sum(Count)") {
		t.Errorf("an exponential histogram has no Value column: rank the series cap by Count:\n%s", st.SQL)
	}
}

func TestCompileExpHistogramCounterFamilies(t *testing.T) {
	for _, agg := range []Agg{AggRate, AggCount, AggIncrease, AggSum, AggAvg, AggMin, AggMax} {
		st, err := expQuery(agg, 0).Compile()
		if err != nil {
			t.Fatalf("%v: %v", agg, err)
		}
		assertParameterized(t, st)
		if !strings.Contains(st.SQL, "otel_metrics_exponential_histogram") || strings.Contains(st.SQL, "ExplicitBounds") {
			t.Errorf("%v reads the wrong table or identity:\n%s", agg, st.SQL)
		}
	}
}

func TestCompileSummary(t *testing.T) {
	summary := func(agg Agg, q float64) MetricQuery {
		return MetricQuery{Scope: scope(), Name: "rtt", Info: MetricInfo{Type: MetricSummary, IntervalSeconds: 60}, Agg: agg, Quantile: q, StepSeconds: 60, GroupBy: []string{"metric.attr.pod"}}
	}
	for _, agg := range []Agg{AggAvg, AggRate, AggCount, AggIncrease, AggSum} {
		st, err := summary(agg, 0).Compile()
		if err != nil {
			t.Fatalf("%v: %v", agg, err)
		}
		assertParameterized(t, st)
		// the summary table has no AggregationTemporality column: it is cumulative
		if strings.Contains(st.SQL, "AggregationTemporality") || !strings.Contains(st.SQL, "otel_metrics_summary") {
			t.Errorf("%v:\n%s", agg, st.SQL)
		}
	}
	st, err := summary(AggQuantile, 0.99).Compile()
	if err != nil {
		t.Fatal(err)
	}
	assertParameterized(t, st)
	if !strings.Contains(st.SQL, "`ValueAtQuantiles.Quantile`") || !strings.Contains(st.SQL, "`ValueAtQuantiles.Value`") {
		t.Errorf("quantile statement does not read ValueAtQuantiles:\n%s", st.SQL)
	}
	for _, agg := range []Agg{AggMin, AggMax, AggLast} {
		if _, err := summary(agg, 0).Compile(); !isInvalid(err) {
			t.Errorf("%v accepted over a summary: %v", agg, err)
		}
	}
	capped := summary(AggRate, 0)
	capped.Info.Series = 500
	if st, err = capped.Compile(); err != nil || strings.Contains(st.SQL, "abs(Value)") {
		t.Errorf("a capped summary ranks by Count: %v\n%s", err, st.SQL)
	}
}

// expWire is one row of expQuantileSQL: bucket, labels, scale, zero, pk, pv, nk, nv, series_total.
func expWire(min int, scale int32, zero float64, pk []int64, pv []float64) []any {
	return []any{at(min), []string{}, scale, zero, pk, pv, []int64{}, []float64{}, uint64(0)}
}

func TestEvalExpHistogramRunsThroughTheSharedPath(t *testing.T) {
	// bucket 1: 50 in (1,2], 50 in (2,4]; bucket 2: 25 and 75
	run := kindRunner(MetricExpHistogram, [][]any{
		expWire(1, 0, 0, []int64{0, 1}, []float64{50, 50}),
		expWire(2, 0, 0, []int64{0, 1}, []float64{25, 75}),
	})
	if _, err := EvalMetricPanel(context.Background(), run, scope(), linePanel(mser("A", "lat", "quantile", "exp_histogram")), evalOpts()); err == nil {
		t.Fatal("a quantile series without a level must be refused")
	}
	p := linePanel(PanelSeries{Label: "A", Field: "lat", Agg: "quantile", Quantile: 0.95, MetricType: "exp_histogram"})
	res, err := EvalMetricPanel(context.Background(), run, scope(), p, evalOpts())
	if err != nil {
		t.Fatal(err)
	}
	pts := res.Series[0].Points
	if len(pts) != 2 || math.Abs(pts[0].Value-3.8) > 1e-9 || math.Abs(pts[1].Value-(2+2*70.0/75)) > 1e-9 {
		t.Errorf("points = %+v", pts)
	}
	if m := res.Meta[0]; m.Info.Type != MetricExpHistogram || m.Agg != AggQuantile || m.ValueUnit != "s" {
		t.Errorf("meta = %+v", m)
	}
	if got, _ := ResolveAgg(res.Meta[0].Info, AggDefault); got != AggP95 {
		t.Errorf("default = %v, want p95", got)
	}
}

func TestEvalExpHistogramMixedScaleRowsMergeIntoOnePoint(t *testing.T) {
	// two scales in one bucket; at scale 0 they total 30 in (1,2] and 20 in (2,4]
	run := kindRunner(MetricExpHistogram, [][]any{
		expWire(1, 1, 0, []int64{0, 1, 2, 3}, []float64{10, 10, 5, 5}),
		expWire(1, 0, 0, []int64{0, 1}, []float64{10, 10}),
	})
	p := linePanel(PanelSeries{Label: "A", Field: "lat", Agg: "quantile", Quantile: 0.5, MetricType: "exp_histogram"})
	res, err := EvalMetricPanel(context.Background(), run, scope(), p, evalOpts())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Series) != 1 || len(res.Series[0].Points) != 1 {
		t.Fatalf("mixed scales must give one point per bucket: %+v", res.Series)
	}
	if got := res.Series[0].Points[0].Value; math.Abs(got-(1+25.0/30)) > 1e-9 {
		t.Errorf("p50 = %v, want %v", got, 1+25.0/30)
	}
}

// summaryWire is one row of summaryQuantileSQL: bucket, labels, value, series_total, nseries, nmissing.
func summaryWire(min int, v float64, nseries, missing uint64) []any {
	return []any{at(min), []string{}, v, uint64(0), nseries, missing}
}

func TestEvalSummaryQuantilePassthroughAndRefusals(t *testing.T) {
	p := linePanel(PanelSeries{Label: "A", Field: "rtt", Agg: "quantile", Quantile: 0.99, MetricType: "summary"})

	one := kindRunner(MetricSummary, [][]any{summaryWire(1, 0.9, 1, 0), summaryWire(2, 0.8, 1, 0)})
	res, err := EvalMetricPanel(context.Background(), one, scope(), p, evalOpts())
	if err != nil {
		t.Fatal(err)
	}
	if pts := res.Series[0].Points; len(pts) != 2 || pts[0].Value != 0.9 || pts[1].Value != 0.8 {
		t.Errorf("a stored quantile passes through unchanged: %+v", pts)
	}

	many := kindRunner(MetricSummary, [][]any{summaryWire(1, 0.85, 2, 0)})
	_, err = EvalMetricPanel(context.Background(), many, scope(), p, evalOpts())
	if !isInvalid(err) || !strings.Contains(err.Error(), "summary quantiles cannot be merged across series; group by the series' attributes or use a histogram") {
		t.Errorf("two series: err = %v", err)
	}

	missing := kindRunner(MetricSummary, [][]any{summaryWire(1, 0, 1, 1)})
	_, err = EvalMetricPanel(context.Background(), missing, scope(), p, evalOpts())
	if !isInvalid(err) || !strings.Contains(err.Error(), "does not store") {
		t.Errorf("a quantile the summary does not store: err = %v", err)
	}
}

func TestEvalSummaryAvgRateCountMeta(t *testing.T) {
	run := func(agg string) MetricPanelResult {
		r := kindRunner(MetricSummary, nil)
		series := func(ctx context.Context, st Stmt) (Rows, error) {
			if strings.Contains(st.SQL, "AS kind") {
				return r(ctx, st)
			}
			return &fakeRows{rows: [][]any{{at(1), []string{}, 0.4, uint64(0)}, {at(2), []string{}, 0.4, uint64(0)}}}, nil
		}
		res, err := EvalMetricPanel(context.Background(), series, scope(), linePanel(mser("A", "rtt", agg, "summary")), evalOpts())
		if err != nil {
			t.Fatalf("%s: %v", agg, err)
		}
		return res
	}
	if m := run("avg").Meta[0]; m.Agg != AggAvg || m.ValueUnit != "s" {
		t.Errorf("avg meta = %+v", m)
	}
	if m := run("rate").Meta[0]; m.Agg != AggRate || m.ValueUnit != "{count}/s" {
		t.Errorf("rate meta = %+v (a summary's rate is observations per second)", m)
	}
	if m := run("count").Meta[0]; m.ValueUnit != "{count}" {
		t.Errorf("count meta = %+v", m)
	}
	if res := run("avg"); len(res.Series) != 1 || len(res.Series[0].Points) != 2 {
		t.Errorf("series = %+v", res.Series)
	}
}
