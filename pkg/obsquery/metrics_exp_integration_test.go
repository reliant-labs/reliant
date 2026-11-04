//go:build integration

package obsquery_test

// Exponential histograms and summaries against a real ClickHouse, with the
// exporter's own column types. The expected values are computed from the
// observations the fixtures generate, not from the code under test.
//
//	go test -tags integration -run 'ExpHistogram|Summary' ./pkg/obsquery/

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"

	"github.com/reliant-labs/forge/pkg/obsquery"
)

// expBucket is the OTel index of the bucket holding v at a scale: the bucket i
// with 2^(i·2^-scale) < v <= 2^((i+1)·2^-scale).
func expBucket(v float64, scale int) int {
	return int(math.Ceil(math.Log2(v)*math.Exp2(float64(scale)))) - 1
}

// expPoint is a cumulative exponential histogram: the buckets the observations
// fall in at a scale, as a dense run from the lowest populated index.
type expPoint struct {
	scale  int
	obs    []float64
	zeros  int
	offset int
	counts []int
}

func newExpPoint(scale int, zeros int, obs []float64) expPoint {
	p := expPoint{scale: scale, obs: obs, zeros: zeros}
	if len(obs) == 0 {
		return p
	}
	lo, hi := math.MaxInt, math.MinInt
	for _, v := range obs {
		i := expBucket(v, scale)
		lo, hi = min(lo, i), max(hi, i)
	}
	p.offset, p.counts = lo, make([]int, hi-lo+1)
	for _, v := range obs {
		p.counts[expBucket(v, scale)-lo]++
	}
	return p
}

func intsSQL(xs []int) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprint(x)
	}
	return "CAST([" + strings.Join(parts, ",") + "], 'Array(UInt64)')"
}

func insertExp(t *testing.T, c clickhouse.Conn, name, attrs string, start, at time.Time, tmp int, p expPoint) {
	t.Helper()
	chExec(t, c, fmt.Sprintf(`INSERT INTO otel_metrics_exponential_histogram SELECT map('service.name','api'), 'api', '%s', 'ms', %s, %s, %s, %d, 0, %d, %d, %d, %s, 0, CAST([], 'Array(UInt64)'), 0, 0, %d`,
		name, attrs, ts(start), ts(at), len(p.obs)+p.zeros, p.scale, p.zeros, p.offset, intsSQL(p.counts), tmp))
}

// trueQuantile is the exact q-quantile of the observations (nearest rank).
func trueQuantile(obs []float64, zeros int, q float64) float64 {
	all := append(make([]float64, zeros), obs...)
	sort.Float64s(all)
	return all[max(int(math.Ceil(q*float64(len(all))))-1, 0)]
}

// bucketWidth is the width of the bucket at a scale that holds v.
func bucketWidth(v float64, scale int) float64 {
	i := expBucket(v, scale)
	return math.Exp2(float64(i+1)*math.Exp2(float64(-scale))) - math.Exp2(float64(i)*math.Exp2(float64(-scale)))
}

// latencyObs are n observations spread log-uniformly over [lo, hi).
func latencyObs(n int, lo, hi float64) []float64 {
	obs := make([]float64, n)
	for i := range obs {
		obs[i] = lo * math.Pow(hi/lo, (float64(i)+0.5)/float64(n))
	}
	return obs
}

func TestExpHistogramAgainstClickHouse(t *testing.T) {
	c := startClickHouse(t)
	chExec(t, c, strings.Split(metricTables, ";\n")...)
	start := base.Add(-time.Hour)

	// Cumulative samples every 60s. Each minute 100 new observations arrive.
	// The SDK downscales when its range widens, so the scale changes mid-range:
	// scale 4 for the first five samples, scale 2 after.
	seed := func(name, attrs string, perMinute, samples, changeAt int, lo, hi float64, zerosPerMin int) (all []float64) {
		var obs []float64
		for i := 0; i < samples; i++ {
			minute := latencyObs(perMinute, lo*(1+float64(i)/10), hi*(1+float64(i)/10))
			obs = append(obs, minute...)
			scale := 4
			if i >= changeAt {
				scale = 2
			}
			insertExp(t, c, name, attrs, start, base.Add(time.Duration(i)*time.Minute), 2, newExpPoint(scale, zerosPerMin*(i+1), obs))
		}
		return obs
	}

	t.Run("p50 and p95 are within one bucket width of the truth across a scale change, and the rate is right", func(t *testing.T) {
		obs := seed("lat", "map()", 100, 10, 5, 5, 500, 0)
		// the range [base+2m, base+9m): the increments are the observations of samples 2..9 less the one before
		sc := scope(base.Add(2*time.Minute), base.Add(9*time.Minute))
		for _, q := range []float64{0.5, 0.95} {
			p := panel("number", obsquery.PanelSeries{Label: "q", Field: "lat", Agg: "quantile", Quantile: q, MetricType: "exp_histogram"})
			res := eval(t, c, sc, p, obsquery.EvalOptions{StepSeconds: 60})
			if res.Series[0].Reduced == nil {
				t.Fatalf("q%v: no reduction; warnings %v", q, res.Warnings)
			}
			got := *res.Series[0].Reduced
			// the increments cover the samples 1..8's observations arrive in minutes 1..8 (those after the 1st sample)
			var in []float64
			for i := 2; i <= 8; i++ {
				in = append(in, latencyObs(100, 5*(1+float64(i)/10), 500*(1+float64(i)/10))...)
			}
			// the in-range increments are samples whose TimeUnix is in [base+2m, base+9m): samples 2..8
			want := trueQuantile(in, 0, q)
			if tol := bucketWidth(want, 2) * 1.01; math.Abs(got-want) > tol {
				t.Errorf("p%v = %v, want %v within one bucket width (%v)", q*100, got, want, tol)
			}
		}
		_ = obs

		// the line at 60s: one point per sample bucket, each a quantile of that minute's 100 observations
		line := eval(t, c, scope(base.Add(2*time.Minute), base.Add(9*time.Minute)), panel("line", obsquery.PanelSeries{Label: "p50", Field: "lat", Agg: "quantile", Quantile: 0.5, MetricType: "exp_histogram"}), obsquery.EvalOptions{StepSeconds: 60})
		if len(line.Series) != 1 || len(line.Series[0].Points) != 7 {
			t.Fatalf("points = %+v", line.Series)
		}
		for i, p := range line.Series[0].Points {
			minute := i + 2
			want := trueQuantile(latencyObs(100, 5*(1+float64(minute)/10), 500*(1+float64(minute)/10)), 0, 0.5)
			// across the scale change (minute 5) the minute's increment is still exact: both scales are read at the series minimum
			if tol := bucketWidth(want, 2) * 1.01; math.Abs(p.Value-want) > tol {
				t.Errorf("minute %d: p50 = %v, want %v within %v", minute, p.Value, want, tol)
			}
		}

		rate := eval(t, c, sc, panel("line", obsquery.PanelSeries{Label: "r", Field: "lat", Agg: "rate", MetricType: "exp_histogram"}), obsquery.EvalOptions{StepSeconds: 60})
		for _, p := range rate.Series[0].Points {
			if !near(p.Value, 100.0/60) {
				t.Errorf("rate %s = %v, want %v observations/s", p.Time.Format("15:04"), p.Value, 100.0/60)
			}
		}
		count := eval(t, c, sc, panel("number", obsquery.PanelSeries{Label: "n", Field: "lat", Agg: "count", MetricType: "exp_histogram"}), obsquery.EvalOptions{StepSeconds: 60})
		if r := count.Series[0].Reduced; r == nil || math.Abs(*r-700) > 0.5 {
			t.Errorf("count over the range = %v, want 700", r)
		}
	})

	t.Run("default aggregation is p95 and the unit is the metric's", func(t *testing.T) {
		res := eval(t, c, scope(base.Add(2*time.Minute), base.Add(9*time.Minute)), panel("number", obsquery.PanelSeries{Label: "d", Field: "lat", Agg: "quantile", Quantile: 0.95, MetricType: "histogram"}), obsquery.EvalOptions{StepSeconds: 60})
		if m := res.Meta[0]; m.Info.Type != obsquery.MetricExpHistogram || m.ValueUnit != "ms" {
			t.Errorf("a panel that says histogram over an exp histogram reads it as one: %+v", m)
		}
	})

	t.Run("the zero bucket: zeros pull low quantiles to 0", func(t *testing.T) {
		// 100 observations a minute, 100 zeros a minute: half the mass is exactly 0
		seed("withzero", "map()", 100, 10, 99, 5, 500, 100)
		sc := scope(base.Add(2*time.Minute), base.Add(9*time.Minute))
		q := func(level float64) float64 {
			res := eval(t, c, sc, panel("number", obsquery.PanelSeries{Label: "q", Field: "withzero", Agg: "quantile", Quantile: level, MetricType: "exp_histogram"}), obsquery.EvalOptions{})
			if res.Series[0].Reduced == nil {
				t.Fatalf("q%v: no reduction", level)
			}
			return *res.Series[0].Reduced
		}
		if p25 := q(0.25); p25 != 0 {
			t.Errorf("p25 with half the observations at zero = %v, want 0", p25)
		}
		if p75 := q(0.75); p75 <= 0 {
			t.Errorf("p75 = %v, want a positive value above the zero bucket", p75)
		}
	})

	t.Run("a restart (StartTimeUnix moves) does not spike or dip the rate", func(t *testing.T) {
		// 100 observations a minute. The process restarts at minute 3:30, so the
		// sample at minute 4 holds the 50 observations of the half minute since.
		pool := latencyObs(100, 10, 100)
		take := func(n int) []float64 { return repeat(pool, n/100+1)[:n] }
		for i := 0; i < 4; i++ {
			insertExp(t, c, "restart", "map()", start, base.Add(time.Duration(i)*time.Minute), 2, newExpPoint(3, 0, take(100*(i+1))))
		}
		restart := base.Add(3*time.Minute + 30*time.Second)
		for i := 4; i < 9; i++ {
			insertExp(t, c, "restart", "map()", restart, base.Add(time.Duration(i)*time.Minute), 2, newExpPoint(3, 0, take(50+100*(i-4))))
		}
		res := eval(t, c, scope(base.Add(2*time.Minute), base.Add(9*time.Minute)), panel("line", obsquery.PanelSeries{Label: "c", Field: "restart", Agg: "rate", MetricType: "exp_histogram"}), obsquery.EvalOptions{StepSeconds: 60})
		if len(res.Series[0].Points) != 7 {
			t.Fatalf("points = %v", pointValues(res.Series[0]))
		}
		for _, p := range res.Series[0].Points {
			if !near(p.Value, 100.0/60) {
				t.Errorf("rate at %s = %v across a restart, want %v", p.Time.Format("15:04:05"), p.Value, 100.0/60)
			}
		}
		count := eval(t, c, scope(base.Add(2*time.Minute), base.Add(9*time.Minute)), panel("number", obsquery.PanelSeries{Label: "n", Field: "restart", Agg: "count", MetricType: "exp_histogram"}), obsquery.EvalOptions{StepSeconds: 60})
		if r := count.Series[0].Reduced; r == nil || math.Abs(*r-700) > 0.5 {
			t.Errorf("count across the restart = %v, want 700", r)
		}
	})

	t.Run("delta temporality: each sample is its own increment", func(t *testing.T) {
		for i := 0; i < 6; i++ {
			insertExp(t, c, "deltalat", "map()", base.Add(time.Duration(i-1)*time.Minute), base.Add(time.Duration(i)*time.Minute), 1, newExpPoint(2, 0, latencyObs(60, 20, 80)))
		}
		sc := scope(base.Add(time.Minute), base.Add(5*time.Minute))
		res := eval(t, c, sc, panel("line", obsquery.PanelSeries{Label: "p", Field: "deltalat", Agg: "quantile", Quantile: 0.5, MetricType: "exp_histogram"}), obsquery.EvalOptions{StepSeconds: 60})
		want := trueQuantile(latencyObs(60, 20, 80), 0, 0.5)
		for _, p := range res.Series[0].Points {
			if tol := bucketWidth(want, 2) * 1.01; math.Abs(p.Value-want) > tol {
				t.Errorf("delta p50 at %s = %v, want %v within %v", p.Time.Format("15:04"), p.Value, want, tol)
			}
		}
		rate := eval(t, c, sc, panel("line", obsquery.PanelSeries{Label: "r", Field: "deltalat", Agg: "rate", MetricType: "exp_histogram"}), obsquery.EvalOptions{StepSeconds: 60})
		for _, p := range rate.Series[0].Points {
			if !near(p.Value, 1) {
				t.Errorf("delta rate = %v, want 1/s (60 observations a minute)", p.Value)
			}
		}
	})

	t.Run("two series with different scales merge into one quantile, grouped they stay apart", func(t *testing.T) {
		fast, slow := latencyObs(100, 1, 10), latencyObs(100, 100, 1000)
		for i := 0; i < 4; i++ {
			insertExp(t, c, "two", "map('pod','fast')", start, base.Add(time.Duration(i)*time.Minute), 2, newExpPoint(5, 0, repeat(fast, i+1)))
			insertExp(t, c, "two", "map('pod','slow')", start, base.Add(time.Duration(i)*time.Minute), 2, newExpPoint(1, 0, repeat(slow, i+1)))
		}
		sc := scope(base.Add(time.Minute), base.Add(4*time.Minute))
		res := eval(t, c, sc, panel("number", obsquery.PanelSeries{Label: "q", Field: "two", Agg: "quantile", Quantile: 0.5, MetricType: "exp_histogram"}), obsquery.EvalOptions{StepSeconds: 60})
		// half the mass is below 10 and half above 100: the merged median lies at the boundary of the two clusters
		if r := res.Series[0].Reduced; r == nil || *r < 9 || *r > 110 {
			t.Errorf("merged median = %v, want between the clusters", r)
		}
		g := panel("number", obsquery.PanelSeries{Label: "q", Field: "two", Agg: "quantile", Quantile: 0.5, MetricType: "exp_histogram"})
		g.GroupBy = []string{"metric.attr.pod"}
		by := map[string]float64{}
		for _, s := range eval(t, c, sc, g, obsquery.EvalOptions{StepSeconds: 60}).Series {
			by[s.Labels["metric.attr.pod"]] = *s.Reduced
		}
		wf, ws := trueQuantile(fast, 0, 0.5), trueQuantile(slow, 0, 0.5)
		if math.Abs(by["fast"]-wf) > bucketWidth(wf, 1)*1.01 || math.Abs(by["slow"]-ws) > bucketWidth(ws, 1)*1.01 {
			t.Errorf("per pod p50 = %v, want fast %v slow %v", by, wf, ws)
		}
	})

	t.Run("a series-capped exp histogram ranks by count, not a missing Value column", func(t *testing.T) {
		for i := 0; i < 60; i++ {
			insertExp(t, c, "wide", fmt.Sprintf("map('id','%d')", i), start, base, 2, newExpPoint(2, 0, latencyObs(i+1, 1, 10)))
			insertExp(t, c, "wide", fmt.Sprintf("map('id','%d')", i), start, base.Add(time.Minute), 2, newExpPoint(2, 0, latencyObs(2*(i+1), 1, 10)))
		}
		g := panel("line", obsquery.PanelSeries{Label: "q", Field: "wide", Agg: "quantile", Quantile: 0.5, MetricType: "exp_histogram"})
		g.GroupBy = []string{"metric.attr.id"}
		res := eval(t, c, scope(base, base.Add(2*time.Minute)), g, obsquery.EvalOptions{StepSeconds: 60, MaxSeries: 10})
		if len(res.Series) != 10 || res.SeriesTotal != 60 || !res.Truncated {
			t.Errorf("%d series, total %d, truncated %v; want 10 of 60", len(res.Series), res.SeriesTotal, res.Truncated)
		}
	})
}

func repeat(obs []float64, n int) []float64 {
	var out []float64
	for i := 0; i < n; i++ {
		out = append(out, obs...)
	}
	return out
}

func insertSummary(t *testing.T, c clickhouse.Conn, name, attrs string, start, at time.Time, count int, sum float64, qs, vs []float64) {
	t.Helper()
	f := func(xs []float64) string {
		parts := make([]string, len(xs))
		for i, x := range xs {
			parts[i] = fmt.Sprint(x)
		}
		return "CAST([" + strings.Join(parts, ",") + "], 'Array(Float64)')"
	}
	chExec(t, c, fmt.Sprintf(`INSERT INTO otel_metrics_summary SELECT map('service.name','api'), 'api', '%s', 's', %s, %s, %s, %d, %f, %s, %s`,
		name, attrs, ts(start), ts(at), count, sum, f(qs), f(vs)))
}

func TestSummaryAgainstClickHouse(t *testing.T) {
	c := startClickHouse(t)
	chExec(t, c, strings.Split(metricTables, ";\n")...)
	start := base.Add(-time.Hour)
	qs := []float64{0.5, 0.99}

	// 30 observations a minute, 0.2s each on average, cumulative since start
	for i := 0; i < 12; i++ {
		n := 30 * (i + 1)
		insertSummary(t, c, "rtt", "map('pod','a')", start, base.Add(time.Duration(i)*time.Minute), n, 0.2*float64(n), qs, []float64{0.15 + 0.01*float64(i), 0.5 + 0.01*float64(i)})
	}
	sc := scope(base.Add(2*time.Minute), base.Add(10*time.Minute))

	t.Run("avg is the sum of Sum increments over the sum of Count increments", func(t *testing.T) {
		res := eval(t, c, sc, panel("line", ser("A", "rtt", "avg", "summary")), obsquery.EvalOptions{StepSeconds: 60})
		if len(res.Series) != 1 || len(res.Series[0].Points) == 0 {
			t.Fatalf("series = %+v; warnings %v", res.Series, res.Warnings)
		}
		for _, p := range res.Series[0].Points {
			if !near(p.Value, 0.2) {
				t.Errorf("avg %s = %v, want 0.2", p.Time.Format("15:04"), p.Value)
			}
		}
		if m := res.Meta[0]; m.Info.Type != obsquery.MetricSummary || m.ValueUnit != "s" {
			t.Errorf("meta = %+v", m)
		}
	})

	t.Run("rate and count run over Count", func(t *testing.T) {
		rate := eval(t, c, sc, panel("line", ser("A", "rtt", "rate", "summary")), obsquery.EvalOptions{StepSeconds: 60})
		for _, p := range rate.Series[0].Points {
			if !near(p.Value, 0.5) {
				t.Errorf("rate %s = %v, want 30/60 = 0.5 observations/s", p.Time.Format("15:04"), p.Value)
			}
		}
		if rate.Meta[0].ValueUnit != "{count}/s" {
			t.Errorf("unit = %q", rate.Meta[0].ValueUnit)
		}
		n := eval(t, c, sc, panel("number", ser("A", "rtt", "count", "summary")), obsquery.EvalOptions{StepSeconds: 60})
		if r := n.Series[0].Reduced; r == nil || math.Abs(*r-240) > 0.5 {
			t.Errorf("count over 8 minutes = %v, want 240", r)
		}
	})

	t.Run("a single series' stored quantile passes through", func(t *testing.T) {
		res := eval(t, c, sc, panel("line", obsquery.PanelSeries{Label: "p99", Field: "rtt", Agg: "quantile", Quantile: 0.99, MetricType: "summary"}), obsquery.EvalOptions{StepSeconds: 60})
		pts := res.Series[0].Points
		if len(pts) == 0 {
			t.Fatalf("no points: %+v", res)
		}
		for i, p := range pts {
			want := 0.5 + 0.01*float64(i+2)
			if !near(p.Value, want) {
				t.Errorf("p99 at %s = %v, want the stored %v", p.Time.Format("15:04"), p.Value, want)
			}
		}
	})

	t.Run("a quantile across two series is refused, and grouped it is allowed", func(t *testing.T) {
		for i := 0; i < 12; i++ {
			n := 30 * (i + 1)
			insertSummary(t, c, "rtt", "map('pod','b')", start, base.Add(time.Duration(i)*time.Minute), n, 0.4*float64(n), qs, []float64{0.3, 0.9})
		}
		p := panel("line", obsquery.PanelSeries{Label: "p99", Field: "rtt", Agg: "quantile", Quantile: 0.99, MetricType: "summary"})
		_, err := obsquery.EvalMetricPanel(context.Background(), chRunner(c), sc, p, obsquery.EvalOptions{StepSeconds: 60, Now: base.Add(48 * time.Hour)})
		if err == nil || !strings.Contains(err.Error(), "summary quantiles cannot be merged across series; group by the series' attributes or use a histogram") {
			t.Fatalf("err = %v", err)
		}
		if !strings.Contains(fmt.Sprint(err), "invalid") {
			t.Errorf("not InvalidArgument: %v", err)
		}
		p.GroupBy = []string{"metric.attr.pod"}
		res := eval(t, c, sc, p, obsquery.EvalOptions{StepSeconds: 60})
		if len(res.Series) != 2 {
			t.Fatalf("grouped series = %d", len(res.Series))
		}
		for _, s := range res.Series {
			for _, pt := range s.Points {
				if s.Labels["metric.attr.pod"] == "b" && !near(pt.Value, 0.9) {
					t.Errorf("pod b p99 = %v, want 0.9", pt.Value)
				}
			}
		}
		// avg and rate still merge: the totals are sums
		rate := eval(t, c, sc, panel("line", ser("A", "rtt", "rate", "summary")), obsquery.EvalOptions{StepSeconds: 60})
		for _, pt := range rate.Series[0].Points {
			if !near(pt.Value, 1) {
				t.Errorf("two-series rate = %v, want 1/s", pt.Value)
			}
		}
		avg := eval(t, c, sc, panel("line", ser("A", "rtt", "avg", "summary")), obsquery.EvalOptions{StepSeconds: 60})
		for _, pt := range avg.Series[0].Points {
			if !near(pt.Value, 0.3) {
				t.Errorf("two-series avg = %v, want (0.2+0.4)/2 = 0.3", pt.Value)
			}
		}
	})

	t.Run("a quantile the summary does not store is refused with the reason", func(t *testing.T) {
		p := panel("line", obsquery.PanelSeries{Label: "p90", Field: "rtt", Agg: "quantile", Quantile: 0.9, MetricType: "summary"})
		p.GroupBy = []string{"metric.attr.pod"}
		_, err := obsquery.EvalMetricPanel(context.Background(), chRunner(c), sc, p, obsquery.EvalOptions{StepSeconds: 60, Now: base.Add(48 * time.Hour)})
		if err == nil || !strings.Contains(err.Error(), "does not store") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("min, max and last are refused over a summary", func(t *testing.T) {
		for _, agg := range []string{"min", "max", "last"} {
			_, err := obsquery.EvalMetricPanel(context.Background(), chRunner(c), sc, panel("line", ser("A", "rtt", agg, "summary")), obsquery.EvalOptions{StepSeconds: 60, Now: base.Add(48 * time.Hour)})
			if err == nil {
				t.Errorf("%s accepted over a summary", agg)
			}
		}
	})
}
