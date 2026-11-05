package obsquery

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeRows is a slice of rows; Scan assigns by the destination's type, so a
// test lists the exact values the real columns would carry.
type fakeRows struct {
	rows [][]any
	i    int
}

func (f *fakeRows) Next() bool { f.i++; return f.i <= len(f.rows) }
func (f *fakeRows) Err() error { return nil }
func (f *fakeRows) Close() error {
	return nil
}
func (f *fakeRows) Scan(dest ...any) error {
	row := f.rows[f.i-1]
	if len(dest) != len(row) {
		return fmt.Errorf("scan: %d destinations for %d columns", len(dest), len(row))
	}
	for i, d := range dest {
		switch p := d.(type) {
		case *uint8:
			*p = row[i].(uint8)
		case *int32:
			*p = row[i].(int32)
		case *uint64:
			*p = row[i].(uint64)
		case *string:
			*p = row[i].(string)
		case *float64:
			*p = row[i].(float64)
		case *time.Time:
			*p = row[i].(time.Time)
		case *[]string:
			*p = row[i].([]string)
		case *[]float64:
			*p = row[i].([]float64)
		default:
			return fmt.Errorf("scan: unsupported destination %T", d)
		}
	}
	return nil
}

// lookupRow is one MetricLookup row: kind, unit, mono, temporality, mixed,
// series, points, first, last, interval.
func lookupRow(kind MetricType, unit string, mono bool, temporality int32, series, interval uint64) []any {
	m := uint8(0)
	if mono {
		m = 1
	}
	return []any{uint8(kind), unit, m, temporality, uint8(0), series, uint64(100), t0, t0.Add(time.Hour), interval}
}

type fakeDB struct {
	t       *testing.T
	lookups map[string][][]any // by metric name param value
	series  map[string][][]any // by metric name
	calls   []Stmt
}

func (db *fakeDB) run(_ context.Context, st Stmt) (Rows, error) {
	db.calls = append(db.calls, st)
	name := ""
	for _, v := range st.Params {
		if _, ok := db.lookups[v]; ok {
			name = v
		}
	}
	switch {
	case strings.Contains(st.SQL, "AS kind"):
		return &fakeRows{rows: db.lookups[name]}, nil
	case strings.Contains(st.SQL, "mapKeys"):
		return &fakeRows{rows: [][]any{{"route"}}}, nil
	}
	return &fakeRows{rows: db.series[name]}, nil
}

func at(min int) time.Time { return t0.Add(time.Duration(min) * time.Minute) }

func linePanel(series ...PanelSeries) Panel {
	return Panel{ID: "p", Kind: "line", Signal: "metrics", Series: series}
}

func mser(label, field, agg, mt string) PanelSeries {
	return PanelSeries{Label: label, Field: field, Agg: agg, MetricType: mt}
}

func evalOpts() EvalOptions { return EvalOptions{Now: at(600)} }

func TestEvalMetricPanelExplainRunsNothing(t *testing.T) {
	db := &fakeDB{t: t}
	p := linePanel(mser("A", "reqs", "rate", "sum"), mser("B", "heap", "avg", "gauge"))
	p.GroupBy = []string{"metric.attr.route"}
	o := evalOpts()
	o.Explain = true
	res, err := EvalMetricPanel(context.Background(), func(context.Context, Stmt) (Rows, error) {
		t.Fatal("explain ran a statement")
		return nil, nil
	}, scope(), p, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Statements) < 4 {
		t.Fatalf("explain statements = %d, want lookups and series for both metrics: %+v", len(res.Statements), res.Statements)
	}
	for _, s := range res.Statements {
		assertParameterized(t, s.Stmt)
	}
	_ = db
}

func TestEvalMetricPanelRateLineWarnsWhenStepRaised(t *testing.T) {
	db := &fakeDB{
		lookups: map[string][][]any{"reqs": {lookupRow(MetricSum, "{req}", true, 2, 1, 60)}},
		series:  map[string][][]any{"reqs": {{at(1), []string{}, 2.0, uint64(0)}, {at(2), []string{}, 2.0, uint64(0)}}},
	}
	o := evalOpts()
	o.StepSeconds = 10
	res, err := EvalMetricPanel(context.Background(), db.run, scope(), linePanel(mser("A", "reqs", "rate", "sum")), o)
	if err != nil {
		t.Fatal(err)
	}
	if res.StepSeconds != 60 {
		t.Errorf("step = %d, want the 60s export interval", res.StepSeconds)
	}
	if !hasWarning(res, WarnStepRaised) {
		t.Errorf("no step_raised warning: %v", res.Warnings)
	}
	if got := res.Meta[0]; got.ValueUnit != "{req}/s" || got.Agg != AggRate || got.Info.Type != MetricSum {
		t.Errorf("meta = %+v", got)
	}
	if len(res.Series) != 1 || len(res.Series[0].Points) != 2 || res.Series[0].Points[0].Value != 2 {
		t.Errorf("series = %+v", res.Series)
	}
}

func hasWarning(r MetricPanelResult, code string) bool {
	for _, w := range r.Warnings {
		if WarningCode(w) == code {
			return true
		}
	}
	return false
}

func TestEvalMetricPanelUpDownRateIsInvalid(t *testing.T) {
	db := &fakeDB{lookups: map[string][][]any{"conns": {lookupRow(MetricSum, "{conn}", false, 2, 1, 60)}}}
	_, err := EvalMetricPanel(context.Background(), db.run, scope(), linePanel(mser("A", "conns", "rate", "sum")), evalOpts())
	if !isInvalid(err) {
		t.Fatalf("err = %v, want InvalidArgument", err)
	}
	// `sum` over the same up-down counter is its level, and valid
	if _, err := EvalMetricPanel(context.Background(), db.run, scope(), linePanel(mser("A", "conns", "sum", "sum")), evalOpts()); err != nil {
		t.Fatal(err)
	}
}

// A metric with no data in the scope and range is an empty series with a
// warning, never an error: the console draws "no data in this window".
func TestEvalMetricPanelAbsentMetricIsEmptyNotAnError(t *testing.T) {
	db := &fakeDB{lookups: map[string][][]any{}}
	for _, kind := range []string{"line", "number", "table"} {
		p := linePanel(mser("A", "nope", "avg", "gauge"))
		p.Kind = kind
		res, err := EvalMetricPanel(context.Background(), db.run, scope(), p, evalOpts())
		if err != nil {
			t.Fatalf("%s: err = %v, want an empty result", kind, err)
		}
		if len(res.Series) != 0 || !hasWarning(res, WarnMetricAbsent) || len(res.Absent) != 1 || res.Absent[0] != "nope" {
			t.Errorf("%s: series = %+v, warnings = %v, absent = %v", kind, res.Series, res.Warnings, res.Absent)
		}
		if len(res.Meta) != 1 || res.Meta[0].Info.Type != MetricGauge || res.Meta[0].Agg != AggAvg {
			t.Errorf("%s: meta = %+v (the declared kind and aggregation)", kind, res.Meta)
		}
	}
}

func TestEvalMetricPanelAbsentSeriesLeavesTheOthersDrawn(t *testing.T) {
	db := &fakeDB{
		lookups: map[string][][]any{"heap": {lookupRow(MetricGauge, "By", false, 0, 1, 60)}},
		series:  map[string][][]any{"heap": {{at(1), []string{}, 1.0, uint64(0)}}},
	}
	res, err := EvalMetricPanel(context.Background(), db.run, scope(), linePanel(mser("A", "heap", "max", "gauge"), mser("B", "gone", "max", "gauge")), evalOpts())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Series) != 1 || res.Series[0].Label != "A" || !hasWarning(res, WarnMetricAbsent) {
		t.Errorf("series = %+v, warnings = %v", res.Series, res.Warnings)
	}
	for _, st := range db.calls {
		if !strings.Contains(st.SQL, "AS kind") && slices.Contains(paramValues(st), "gone") {
			t.Errorf("an absent metric's series was compiled and run: %s", st.SQL)
		}
	}
}

func TestEvalMetricPanelAbsentMetricStillValidatesItsDeclaredAggregation(t *testing.T) {
	db := &fakeDB{lookups: map[string][][]any{}}
	_, err := EvalMetricPanel(context.Background(), db.run, scope(), linePanel(mser("A", "nope", "rate", "gauge")), evalOpts())
	if !isInvalid(err) {
		t.Fatalf("err = %v, want InvalidArgument: a rate of a gauge is malformed whether or not it has data", err)
	}
}

func TestEvalMetricPanelAbsentCounterIsZeroInARatio(t *testing.T) {
	db := &fakeDB{
		lookups: map[string][][]any{"reqs": {lookupRow(MetricSum, "{req}", true, 2, 1, 60)}},
		series:  map[string][][]any{"reqs": {{at(1), []string{}, 4.0, uint64(0)}, {at(2), []string{}, 5.0, uint64(0)}}},
	}
	p := linePanel(mser("A", "errs", "rate", "sum"), mser("B", "reqs", "rate", "sum"))
	p.Ratio = true
	res, err := EvalMetricPanel(context.Background(), db.run, scope(), p, evalOpts())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Series) != 1 || len(res.Series[0].Points) != 2 || res.Series[0].Points[0].Null || res.Series[0].Points[0].Value != 0 {
		t.Errorf("an error counter that never fired is an error rate of 0: %+v", res.Series)
	}
}

func TestEvalMetricPanelWarnsWhenTheDeclaredKindIsNotStored(t *testing.T) {
	db := &fakeDB{
		lookups: map[string][][]any{"msgs": {lookupRow(MetricGauge, "1", false, 0, 1, 60)}},
		series:  map[string][][]any{"msgs": {{at(1), []string{}, 1.0, uint64(0)}}},
	}
	res, err := EvalMetricPanel(context.Background(), db.run, scope(), linePanel(mser("A", "msgs", "max", "sum")), evalOpts())
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarning(res, WarnKindMismatch) || res.Meta[0].Info.Type != MetricGauge {
		t.Errorf("warnings = %v, meta = %+v", res.Warnings, res.Meta[0])
	}
}

func paramValues(st Stmt) []string {
	out := make([]string, 0, len(st.Params))
	for _, v := range st.Params {
		out = append(out, v)
	}
	return out
}

func TestEvalMetricPanelFormulaMissingPointsGaugeVsCounter(t *testing.T) {
	db := &fakeDB{
		lookups: map[string][][]any{
			"cpu":   {lookupRow(MetricGauge, "1", false, 0, 1, 60)},
			"limit": {lookupRow(MetricGauge, "1", false, 0, 1, 60)},
			"errs":  {lookupRow(MetricSum, "{err}", true, 2, 1, 60)},
			"reqs":  {lookupRow(MetricSum, "{req}", true, 2, 1, 60)},
		},
		series: map[string][][]any{
			"cpu":   {{at(1), []string{}, 1.0, uint64(0)}, {at(2), []string{}, 2.0, uint64(0)}, {at(3), []string{}, 3.0, uint64(0)}},
			"limit": {{at(1), []string{}, 4.0, uint64(0)}, {at(3), []string{}, 6.0, uint64(0)}}, // missing at(2)
			"errs":  {{at(1), []string{}, 1.0, uint64(0)}, {at(3), []string{}, 3.0, uint64(0)}}, // missing at(2): no errors
			"reqs":  {{at(1), []string{}, 4.0, uint64(0)}, {at(2), []string{}, 5.0, uint64(0)}, {at(3), []string{}, 6.0, uint64(0)}},
		},
	}
	gauge := linePanel(mser("A", "cpu", "avg", "gauge"), mser("B", "limit", "avg", "gauge"))
	gauge.Formula = "A / B"
	res, err := EvalMetricPanel(context.Background(), db.run, scope(), gauge, evalOpts())
	if err != nil {
		t.Fatal(err)
	}
	pts := res.Series[0].Points
	if res.Series[0].Label != "formula" || len(pts) != 3 {
		t.Fatalf("series = %+v", res.Series)
	}
	if pts[0].Null || math.Abs(pts[0].Value-0.25) > 1e-9 || !pts[1].Null || pts[2].Null {
		t.Errorf("a gauge formula with B missing must be a gap, not 0 or Inf: %+v", pts)
	}

	counts := linePanel(mser("A", "errs", "rate", "sum"), mser("B", "reqs", "rate", "sum"))
	counts.Ratio = true
	res, err = EvalMetricPanel(context.Background(), db.run, scope(), counts, evalOpts())
	if err != nil {
		t.Fatal(err)
	}
	pts = res.Series[0].Points
	if res.Series[0].Label != "ratio" || pts[1].Null || pts[1].Value != 0 {
		t.Errorf("no errors in a bucket is an error rate of 0, not a gap: %+v", pts)
	}
}

func TestEvalMetricPanelDivisionByZeroIsNull(t *testing.T) {
	db := &fakeDB{
		lookups: map[string][][]any{"a": {lookupRow(MetricGauge, "1", false, 0, 1, 60)}, "b": {lookupRow(MetricGauge, "1", false, 0, 1, 60)}},
		series:  map[string][][]any{"a": {{at(1), []string{}, 1.0, uint64(0)}}, "b": {{at(1), []string{}, 0.0, uint64(0)}}},
	}
	p := linePanel(mser("A", "a", "avg", "gauge"), mser("B", "b", "avg", "gauge"))
	p.Formula = "A / B"
	res, err := EvalMetricPanel(context.Background(), db.run, scope(), p, evalOpts())
	if err != nil {
		t.Fatal(err)
	}
	if pt := res.Series[0].Points[0]; !pt.Null || pt.Value != 0 {
		t.Errorf("1/0 = %+v, want a null point", pt)
	}
}

func TestEvalMetricPanelPartialBucket(t *testing.T) {
	db := &fakeDB{
		lookups: map[string][][]any{"reqs": {lookupRow(MetricSum, "{req}", true, 2, 1, 60)}},
		series:  map[string][][]any{"reqs": {{at(1), []string{}, 2.0, uint64(0)}, {at(59), []string{}, 2.0, uint64(0)}}},
	}
	o := EvalOptions{StepSeconds: 60, Now: at(59).Add(30 * time.Second)}
	res, err := EvalMetricPanel(context.Background(), db.run, scope(), linePanel(mser("A", "reqs", "rate", "sum")), o)
	if err != nil {
		t.Fatal(err)
	}
	pts := res.Series[0].Points
	if pts[0].Partial || !pts[1].Partial {
		t.Errorf("only the last bucket, still filling, is partial: %+v", pts)
	}
}

func TestEvalMetricPanelNumberIsTheRangeReductionPlusSparkline(t *testing.T) {
	db := &fakeDB{
		lookups: map[string][][]any{"reqs": {lookupRow(MetricSum, "{req}", true, 2, 1, 60)}},
		series:  map[string][][]any{"reqs": {{at(0), []string{}, 2.0, uint64(0)}}},
	}
	p := linePanel(mser("A", "reqs", "rate", "sum"))
	p.Kind = "number"
	res, err := EvalMetricPanel(context.Background(), db.run, scope(), p, evalOpts())
	if err != nil {
		t.Fatal(err)
	}
	s := res.Series[0]
	if s.Reduced == nil || *s.Reduced != 2 {
		t.Fatalf("reduced = %v", s.Reduced)
	}
	if len(s.Points) == 0 {
		t.Error("an ungrouped number panel keeps its sparkline")
	}
	var reduce, spark int
	for _, st := range res.Statements {
		switch {
		case strings.Contains(st.Purpose, "over the range"):
			reduce++
		case strings.Contains(st.Purpose, "sparkline"):
			spark++
		}
	}
	if reduce != 1 || spark != 1 {
		t.Errorf("statements: %d reductions, %d sparklines: %+v", reduce, spark, res.Statements)
	}
}

func TestEvalMetricPanelTableOverMetricsIsSupportedAndSorted(t *testing.T) {
	db := &fakeDB{
		lookups: map[string][][]any{"reqs": {lookupRow(MetricSum, "{req}", true, 2, 3, 60)}},
		series: map[string][][]any{"reqs": {
			{at(0), []string{"a"}, 1.0, uint64(0)}, {at(0), []string{"b"}, 5.0, uint64(0)}, {at(0), []string{"c"}, 3.0, uint64(0)},
		}},
	}
	p := linePanel(mser("rps", "reqs", "rate", "sum"))
	p.Kind = "table"
	p.GroupBy = []string{"metric.attr.route"}
	p.Limit = 2
	res, err := EvalMetricPanel(context.Background(), db.run, scope(), p, evalOpts())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Series) != 2 || res.Series[0].Labels["metric.attr.route"] != "b" || res.Series[1].Labels["metric.attr.route"] != "c" {
		t.Errorf("table rows = %+v", res.Series)
	}
	if !res.Truncated || res.SeriesTotal != 3 {
		t.Errorf("truncated = %v, total = %d; want true, 3", res.Truncated, res.SeriesTotal)
	}
}

func TestEvalMetricPanelReportsSeriesTotalFromSQL(t *testing.T) {
	db := &fakeDB{
		lookups: map[string][][]any{"reqs": {lookupRow(MetricSum, "{req}", true, 2, 120, 60)}},
		series:  map[string][][]any{"reqs": {{at(1), []string{"a"}, 1.0, uint64(120)}, {at(1), []string{"b"}, 2.0, uint64(120)}}},
	}
	p := linePanel(mser("A", "reqs", "rate", "sum"))
	p.GroupBy = []string{"metric.attr.route"}
	o := evalOpts()
	o.MaxSeries = 2
	res, err := EvalMetricPanel(context.Background(), db.run, scope(), p, o)
	if err != nil {
		t.Fatal(err)
	}
	if res.SeriesTotal != 120 || !res.Truncated || len(res.Series) != 2 {
		t.Errorf("total = %d, truncated = %v, series = %d", res.SeriesTotal, res.Truncated, len(res.Series))
	}
	var capped bool
	for _, s := range res.Statements {
		capped = capped || strings.Contains(s.Stmt.SQL, "LIMIT 2)")
	}
	if !capped {
		t.Error("the cap was not applied in SQL")
	}
}

func TestEvalMetricPanelStoredAsTwoKinds(t *testing.T) {
	db := &fakeDB{
		lookups: map[string][][]any{"m": {lookupRow(MetricGauge, "1", false, 0, 1, 60), lookupRow(MetricSum, "1", true, 2, 1, 60)}},
		series:  map[string][][]any{"m": {{at(1), []string{}, 1.0, uint64(0)}}},
	}
	res, err := EvalMetricPanel(context.Background(), db.run, scope(), linePanel(mser("A", "m", "avg", "sum")), evalOpts())
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarning(res, WarnStoredAsNKinds) || res.Meta[0].Info.Type != MetricSum {
		t.Errorf("warnings = %v, meta = %+v (the series' metricType picks the kind)", res.Warnings, res.Meta[0])
	}
}

func TestEvalMetricPanelQuantileIsExactAndMergesMixedBounds(t *testing.T) {
	db := &fakeDB{
		lookups: map[string][][]any{"lat": {lookupRow(MetricHistogram, "ms", false, 2, 2, 60)}},
		series: map[string][][]any{"lat": {
			// one (bucket, group) with two bound sets: one point, and a warning
			{at(1), []string{}, []float64{10, 100}, []float64{50, 50, 0}, uint64(0)},
			{at(1), []string{}, []float64{20, 100}, []float64{50, 50, 0}, uint64(0)},
		}},
	}
	p := linePanel(PanelSeries{Label: "p90", Field: "lat", Agg: "quantile", Quantile: 0.9, MetricType: "histogram"})
	res, err := EvalMetricPanel(context.Background(), db.run, scope(), p, evalOpts())
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarning(res, WarnMixedBounds) {
		t.Errorf("warnings = %v", res.Warnings)
	}
	if len(res.Series) != 1 || len(res.Series[0].Points) != 1 {
		t.Fatalf("one point per bucket, got %+v", res.Series)
	}
	if res.Meta[0].Quantile != 0.9 || res.Meta[0].Agg != AggQuantile {
		t.Errorf("the exact level survives: %+v", res.Meta[0])
	}
}

func TestEvalMetricPanelHistogramDefaultsToP95(t *testing.T) {
	db := &fakeDB{lookups: map[string][][]any{"lat": {lookupRow(MetricHistogram, "ms", false, 2, 1, 60)}}}
	p := linePanel(PanelSeries{Label: "x", Field: "lat", Agg: "avg", MetricType: "histogram"})
	res, err := EvalMetricPanel(context.Background(), db.run, scope(), p, evalOpts())
	if err != nil || res.Meta[0].Agg != AggAvg {
		t.Fatalf("an explicit avg is honoured: %+v %v", res.Meta, err)
	}
	if got, _ := ResolveAgg(res.Meta[0].Info, AggDefault); got != AggP95 {
		t.Errorf("default = %v, want p95 (owner decision O3)", got)
	}
}

func TestPanelSeriesAgg(t *testing.T) {
	for _, tc := range []struct {
		agg  string
		q    float64
		want Agg
		bad  bool
	}{
		{"rate", 0, AggRate, false}, {"increase", 0, AggIncrease, false}, {"sum", 0, AggSum, false},
		{"min", 0, AggMin, false}, {"max", 0, AggMax, false}, {"avg", 0, AggAvg, false},
		{"last", 0, AggLast, false}, {"count", 0, AggCount, false},
		{"quantile", 0.9, AggQuantile, false}, {"quantile", 0, 0, true}, {"quantile", 1.2, 0, true},
		{"count_distinct", 0, 0, true},
	} {
		got, q, err := PanelSeriesAgg(PanelSeries{Agg: tc.agg, Quantile: tc.q})
		if tc.bad != (err != nil) || (!tc.bad && (got != tc.want || q != tc.q)) {
			t.Errorf("%s/%v: %v %v %v", tc.agg, tc.q, got, q, err)
		}
	}
}

func TestMergeHistogramsUnionOfBounds(t *testing.T) {
	// identical bounds just sum
	b, c, mixed := MergeHistograms([][]float64{{10, 20}, {10, 20}}, [][]float64{{1, 2, 3}, {4, 5, 6}})
	if mixed || !equalFloats(b, []float64{10, 20}) || !equalFloats(c, []float64{5, 7, 9}) {
		t.Errorf("same bounds: %v %v %v", b, c, mixed)
	}
	// {10,100} and {20,100}: union {10,20,100}. Source 1's (10,100] count of 90
	// spreads over (10,20] and (20,100] by width 10:80; source 2's (0,20] of 40
	// spreads over (0,10] and (10,20] by width 10:10.
	b, c, mixed = MergeHistograms(
		[][]float64{{10, 100}, {20, 100}},
		[][]float64{{0, 90, 5}, {40, 0, 0}},
	)
	if !mixed || !equalFloats(b, []float64{10, 20, 100}) {
		t.Fatalf("union = %v mixed = %v", b, mixed)
	}
	want := []float64{20, 10 + 20, 80 * 0.9 * 1.0, 5}
	want[2] = 90 * 80.0 / 90.0
	want[1] = 90*10.0/90.0 + 40*10.0/20.0
	want[0] = 40 * 10.0 / 20.0
	for i := range want {
		if math.Abs(c[i]-want[i]) > 1e-9 {
			t.Errorf("counts[%d] = %v, want %v (all: %v)", i, c[i], want[i], c)
		}
	}
	var total float64
	for _, v := range c {
		total += v
	}
	if math.Abs(total-135) > 1e-9 {
		t.Errorf("merging must conserve observations: %v", total)
	}
	if b, c, _ := MergeHistograms(nil, nil); b != nil || c != nil {
		t.Error("nothing merges to nothing")
	}
}

func TestMetricLookupParseAndWindow(t *testing.T) {
	st, err := MetricLookup{Scope: scope(), Name: "m"}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	assertParameterized(t, st)
	if !strings.Contains(st.SQL, "IsMonotonic") || !strings.Contains(st.SQL, "AggregationTemporality") {
		t.Errorf("lookup omits what the semantics need:\n%s", st.SQL)
	}
	info, err := ParseLookup(&fakeRows{rows: [][]any{
		lookupRow(MetricGauge, "1", false, 0, 1, 30),
		lookupRow(MetricSum, "By", true, 2, 7, 60),
	}}, MetricSum)
	if err != nil {
		t.Fatal(err)
	}
	if info.Type != MetricSum || !info.Monotonic || info.Temporality != TemporalityCumulative || info.Series != 7 || info.IntervalSeconds != 60 || info.StoredKinds != 2 || info.Unit != "By" {
		t.Errorf("info = %+v", info)
	}
	// an estimated interval is floored to a nice step: a hole in a 60s series reads 69s
	hole, _ := ParseLookup(&fakeRows{rows: [][]any{lookupRow(MetricGauge, "1", false, 0, 1, 69)}}, MetricGauge)
	if hole.IntervalSeconds != 60 {
		t.Errorf("interval of a 69s mean gap = %d, want 60", hole.IntervalSeconds)
	}
	first, _ := ParseLookup(&fakeRows{rows: [][]any{lookupRow(MetricGauge, "1", false, 0, 1, 30), lookupRow(MetricSum, "By", true, 2, 7, 60)}}, MetricUnknown)
	if first.Type != MetricGauge {
		t.Errorf("the first kind is read when none is asked for: %+v", first)
	}
	if _, err := ParseLookup(&fakeRows{}, MetricSum); !errors.Is(err, ErrMetricNotFound) {
		t.Errorf("empty lookup = %v", err)
	}
}

func TestMetricQueryReadsALookbackAndDropsRowsBeforeStart(t *testing.T) {
	q := MetricQuery{
		Scope: scope(), Name: "m", Info: MetricInfo{Type: MetricSum, Monotonic: true, IntervalSeconds: 60},
		Agg: AggRate, StepSeconds: 30,
	}
	st, err := q.Compile()
	if err != nil {
		t.Fatal(err)
	}
	lookback := fmt.Sprint(t0.Add(-120 * time.Second).Unix())
	start := fmt.Sprint(t0.Unix())
	var sawLookback, sawStart bool
	for _, v := range st.Params {
		sawLookback = sawLookback || v == lookback
		sawStart = sawStart || v == start
	}
	if !sawLookback || !sawStart {
		t.Errorf("params = %v: want a read from start-2*interval (%s) and an outer filter from start (%s)", st.Params, lookback, start)
	}
	if !strings.Contains(st.SQL, "sum(inc) / sum(dt)") {
		t.Errorf("rate is not time-weighted:\n%s", st.SQL)
	}
	if strings.Contains(st.SQL, "sum(inc) / {p") {
		t.Errorf("rate still divides by the step:\n%s", st.SQL)
	}
}

func TestMetricQueryResetAndDeltaRules(t *testing.T) {
	counter, _ := MetricQuery{Scope: scope(), Name: "m", Info: MetricInfo{Type: MetricSum, Monotonic: true, IntervalSeconds: 60}, Agg: AggRate, StepSeconds: 60}.Compile()
	for _, want := range []string{"StartTimeUnix != ps", "c < pc", "tmp = 1"} {
		if !strings.Contains(counter.SQL, want) {
			t.Errorf("counter statement lacks %q:\n%s", want, counter.SQL)
		}
	}
	level, _ := MetricQuery{Scope: scope(), Name: "m", Info: MetricInfo{Type: MetricSum, Monotonic: false}, Agg: AggSum, StepSeconds: 60}.Compile()
	if !strings.Contains(level.SQL, "sumIf(Value, AggregationTemporality = 1)") {
		t.Errorf("a delta sum's level is the bucket total:\n%s", level.SQL)
	}
}
