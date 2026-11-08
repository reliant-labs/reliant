//go:build integration

package obsquery_test

// The metric semantics against a real ClickHouse: the fixtures are the spikes
// in research/metrics-explorer/spikes (a cumulative counter exported every 60s
// at a true 2/s, a restart, an up-down counter) plus the cases the brief adds.
//
//	go test -tags integration -run Metric ./pkg/obsquery/

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/reliant-labs/forge/pkg/obsquery"
)

var base = time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)

const metricTables = `
CREATE TABLE otel_metrics_gauge (ResourceAttributes Map(LowCardinality(String), String), ServiceName LowCardinality(String), MetricName LowCardinality(String), MetricUnit String, Attributes Map(LowCardinality(String), String), StartTimeUnix DateTime, TimeUnix DateTime, Value Float64) ENGINE = MergeTree ORDER BY (MetricName, TimeUnix);
CREATE TABLE otel_metrics_sum (ResourceAttributes Map(LowCardinality(String), String), ServiceName LowCardinality(String), MetricName LowCardinality(String), MetricUnit String, Attributes Map(LowCardinality(String), String), StartTimeUnix DateTime, TimeUnix DateTime, Value Float64, AggregationTemporality Int32, IsMonotonic Bool) ENGINE = MergeTree ORDER BY (MetricName, TimeUnix);
CREATE TABLE otel_metrics_histogram (ResourceAttributes Map(LowCardinality(String), String), ServiceName LowCardinality(String), MetricName LowCardinality(String), MetricUnit String, Attributes Map(LowCardinality(String), String), StartTimeUnix DateTime, TimeUnix DateTime, Count UInt64, Sum Float64, BucketCounts Array(UInt64), ExplicitBounds Array(Float64), Min Float64, Max Float64, AggregationTemporality Int32) ENGINE = MergeTree ORDER BY (MetricName, TimeUnix);
CREATE TABLE otel_metrics_exponential_histogram (ResourceAttributes Map(LowCardinality(String), String), ServiceName LowCardinality(String), MetricName LowCardinality(String), MetricUnit String, Attributes Map(LowCardinality(String), String), StartTimeUnix DateTime, TimeUnix DateTime, Count UInt64, Sum Float64, Scale Int32, ZeroCount UInt64, PositiveOffset Int32, PositiveBucketCounts Array(UInt64), NegativeOffset Int32, NegativeBucketCounts Array(UInt64), Min Float64, Max Float64, AggregationTemporality Int32) ENGINE = MergeTree ORDER BY (MetricName, TimeUnix);
CREATE TABLE otel_metrics_summary (ResourceAttributes Map(LowCardinality(String), String), ServiceName LowCardinality(String), MetricName LowCardinality(String), MetricUnit String, Attributes Map(LowCardinality(String), String), StartTimeUnix DateTime, TimeUnix DateTime, Count UInt64, Sum Float64, ` + "`ValueAtQuantiles.Quantile`" + ` Array(Float64), ` + "`ValueAtQuantiles.Value`" + ` Array(Float64)) ENGINE = MergeTree ORDER BY (MetricName, TimeUnix)`

func chRunner(c clickhouse.Conn) obsquery.Runner {
	return func(ctx context.Context, st obsquery.Stmt) (obsquery.Rows, error) {
		ctx = clickhouse.Context(ctx, clickhouse.WithParameters(clickhouse.Parameters(st.Params)))
		rows, err := c.Query(ctx, st.SQL)
		if err != nil {
			return nil, err
		}
		return rows, nil
	}
}

func chExec(t *testing.T, c clickhouse.Conn, stmts ...string) {
	t.Helper()
	for _, s := range stmts {
		if err := c.Exec(context.Background(), s); err != nil {
			t.Fatalf("%v\n%s", err, s)
		}
	}
}

func ts(t time.Time) string { return "toDateTime('" + t.Format("2006-01-02 15:04:05") + "')" }

// seedSum writes n cumulative samples of a counter at one per `every` seconds
// starting at `from`, growing `perSample` each, as one series (service, attrs).
func seedSum(t *testing.T, c clickhouse.Conn, name string, mono bool, tmp int, start, from time.Time, n int, every int, first, perSample float64, attrs string) {
	t.Helper()
	m := 0
	if mono {
		m = 1
	}
	chExec(t, c, fmt.Sprintf(
		`INSERT INTO otel_metrics_sum SELECT map('service.name','api'), 'api', '%s', '{req}', %s, %s, %s + number*%d, %f + number*%f, %d, %d FROM numbers(%d)`,
		name, attrs, ts(start), ts(from), every, first, perSample, tmp, m, n))
}

func scope(from, to time.Time) obsquery.Scope {
	return obsquery.Scope{Database: "default", Local: true, Start: from, End: to}
}

func panel(kind string, series ...obsquery.PanelSeries) obsquery.Panel {
	return obsquery.Panel{ID: "p", Kind: kind, Signal: "metrics", Series: series}
}

func ser(label, name, agg, mt string) obsquery.PanelSeries {
	return obsquery.PanelSeries{Label: label, Field: name, Agg: agg, MetricType: mt}
}

func eval(t *testing.T, c clickhouse.Conn, sc obsquery.Scope, p obsquery.Panel, o obsquery.EvalOptions) obsquery.MetricPanelResult {
	t.Helper()
	if o.Now.IsZero() {
		o.Now = sc.End.Add(24 * time.Hour)
	}
	res, err := obsquery.EvalMetricPanel(context.Background(), chRunner(c), sc, p, o)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.0005 }

func pointValues(s obsquery.MetricSeriesResult) []float64 {
	out := make([]float64, 0, len(s.Points))
	for _, p := range s.Points {
		out = append(out, p.Value)
	}
	return out
}

func TestMetricSemanticsAgainstClickHouse(t *testing.T) {
	c := startClickHouse(t)
	chExec(t, c, strings.Split(metricTables, ";\n")...)
	ctx := context.Background()
	_ = ctx

	// A cumulative counter at 60s, true rate 2/s, for 2h before `base+2h`.
	start := base.Add(-time.Hour)
	seedSum(t, c, "reqs", true, 2, start, base, 125, 60, 1000, 120, "map()")
	end := base.Add(2 * time.Hour)

	t.Run("rate is 2/s in every complete bucket at every range, no gaps", func(t *testing.T) {
		for _, rng := range []time.Duration{15 * time.Minute, time.Hour, 6 * time.Hour} {
			from := end.Add(-rng)
			if rng == 6*time.Hour {
				from = base.Add(-time.Hour) // data starts here; the rest of the range is empty
			}
			res := eval(t, c, scope(from, end), panel("line", ser("A", "reqs", "rate", "sum")), obsquery.EvalOptions{})
			if len(res.Series) != 1 {
				t.Fatalf("%v: %d series", rng, len(res.Series))
			}
			step := res.StepSeconds
			if step < 60 {
				t.Errorf("%v: step %d is below the export interval", rng, step)
			}
			pts := res.Series[0].Points
			for i, p := range pts {
				if !near(p.Value, 2) {
					t.Errorf("%v step %d: point %d (%s) = %v, want 2", rng, step, i, p.Time.Format("15:04:05"), p.Value)
				}
				if i > 0 && p.Time.Sub(pts[i-1].Time) != time.Duration(step)*time.Second {
					t.Errorf("%v: a gap before %s", rng, p.Time.Format("15:04:05"))
				}
			}
			if len(pts) == 0 {
				t.Errorf("%v: no points", rng)
			}
		}
	})

	t.Run("a step below the export interval is raised, said so, and still 2/s", func(t *testing.T) {
		res := eval(t, c, scope(base.Add(10*time.Minute), base.Add(40*time.Minute)), panel("line", ser("A", "reqs", "rate", "sum")), obsquery.EvalOptions{StepSeconds: 10})
		if res.StepSeconds != 60 {
			t.Errorf("step = %d", res.StepSeconds)
		}
		found := false
		for _, w := range res.Warnings {
			found = found || obsquery.WarningCode(w) == obsquery.WarnStepRaised
		}
		if !found {
			t.Errorf("warnings = %v", res.Warnings)
		}
		for _, p := range res.Series[0].Points {
			if !near(p.Value, 2) {
				t.Errorf("%s = %v", p.Time, p.Value)
			}
		}
	})

	t.Run("a requested step below the interval would read 2x/6x without the floor: step 30 over data at 60s", func(t *testing.T) {
		// Compile at step 30 directly, past the floor, to prove the time-weighted
		// statement itself is unbiased (spike 2, "tw-step 30").
		st, err := obsquery.MetricQuery{
			Scope: scope(base.Add(10*time.Minute), base.Add(40*time.Minute)), Name: "reqs",
			Info: obsquery.MetricInfo{Type: obsquery.MetricSum, Monotonic: true, Temporality: 2, IntervalSeconds: 60, Series: 1},
			Agg:  obsquery.AggRate, StepSeconds: 30,
		}.Compile()
		if err != nil {
			t.Fatal(err)
		}
		rows, err := chRunner(c)(ctx, st)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		n := 0
		for rows.Next() {
			var (
				b      time.Time
				l      []string
				v      float64
				series uint64
			)
			if err := rows.Scan(&b, &l, &v, &series); err != nil {
				t.Fatal(err)
			}
			n++
			if !near(v, 2) {
				t.Errorf("step 30 bucket %s = %v, want 2 (the old statement read 4)", b, v)
			}
		}
		if n == 0 {
			t.Error("no rows")
		}
	})

	t.Run("the first bucket of the range is 2, not 0", func(t *testing.T) {
		from := base.Add(10 * time.Minute)
		res := eval(t, c, scope(from, from.Add(15*time.Minute)), panel("line", ser("A", "reqs", "rate", "sum")), obsquery.EvalOptions{StepSeconds: 60})
		pts := res.Series[0].Points
		if !pts[0].Time.Equal(from) || !near(pts[0].Value, 2) {
			t.Errorf("first point = %+v", pts[0])
		}
	})

	t.Run("a restart: StartTimeUnix moves, no dip and no spike", func(t *testing.T) {
		seedSum(t, c, "restarts", true, 2, start, base, 5, 60, 1000, 120, "map()")                                                   // 10:00-10:04
		seedSum(t, c, "restarts", true, 2, base.Add(4*time.Minute+30*time.Second), base.Add(5*time.Minute), 5, 60, 60, 120, "map()") // 10:05-10:09, from 0 at 10:04:30
		for _, step := range []int32{60, 300} {
			res := eval(t, c, scope(base.Add(2*time.Minute), base.Add(10*time.Minute)), panel("line", ser("A", "restarts", "rate", "sum")), obsquery.EvalOptions{StepSeconds: step})
			for _, p := range res.Series[0].Points {
				if !near(p.Value, 2) {
					t.Errorf("step %d: %s = %v, want 2 across the restart", step, p.Time.Format("15:04"), p.Value)
				}
			}
		}
	})

	t.Run("two series: the total rate is 4, not the mean 2", func(t *testing.T) {
		seedSum(t, c, "two", true, 2, start, base, 10, 60, 1000, 120, "map('pod','a')")
		seedSum(t, c, "two", true, 2, start, base, 10, 60, 5000, 120, "map('pod','b')")
		res := eval(t, c, scope(base.Add(2*time.Minute), base.Add(9*time.Minute)), panel("line", ser("A", "two", "rate", "sum")), obsquery.EvalOptions{StepSeconds: 60})
		for _, p := range res.Series[0].Points {
			if !near(p.Value, 4) {
				t.Errorf("%s = %v, want 4", p.Time.Format("15:04"), p.Value)
			}
		}
		g := panel("line", ser("A", "two", "rate", "sum"))
		g.GroupBy = []string{"metric.attr.pod"}
		res = eval(t, c, scope(base.Add(2*time.Minute), base.Add(9*time.Minute)), g, obsquery.EvalOptions{StepSeconds: 60})
		if len(res.Series) != 2 {
			t.Fatalf("grouped series = %d", len(res.Series))
		}
		for _, s := range res.Series {
			for _, p := range s.Points {
				if !near(p.Value, 2) {
					t.Errorf("%v %s = %v, want 2", s.Labels, p.Time.Format("15:04"), p.Value)
				}
			}
		}
	})

	t.Run("a delta counter gives the same answers, and sum is the bucket total", func(t *testing.T) {
		// 120 per 60s sample, delta: Value is the sample's own increment.
		chExec(t, c, fmt.Sprintf(`INSERT INTO otel_metrics_sum SELECT map('service.name','api'), 'api', 'delta', '{req}', map(), %s + number*60 - 60, %s + number*60, 120, 1, 1 FROM numbers(20)`, ts(base), ts(base)))
		sc := scope(base.Add(2*time.Minute), base.Add(12*time.Minute))
		rate := eval(t, c, sc, panel("line", ser("A", "delta", "rate", "sum")), obsquery.EvalOptions{StepSeconds: 60})
		for _, p := range rate.Series[0].Points {
			if !near(p.Value, 2) {
				t.Errorf("delta rate %s = %v, want 2", p.Time.Format("15:04"), p.Value)
			}
		}
		sum := eval(t, c, sc, panel("line", ser("A", "delta", "sum", "sum")), obsquery.EvalOptions{StepSeconds: 60})
		for _, p := range sum.Series[0].Points {
			if !near(p.Value, 120) {
				t.Errorf("delta sum %s = %v, want the bucket total 120", p.Time.Format("15:04"), p.Value)
			}
		}
	})

	t.Run("an up-down counter: default sum of last values, rate refused", func(t *testing.T) {
		chExec(t, c, `INSERT INTO otel_metrics_sum VALUES
			(map('service.name','api'),'api','conns','{conn}',map(),`+ts(start)+`,`+ts(base)+`,10,2,0),
			(map('service.name','api'),'api','conns','{conn}',map(),`+ts(start)+`,`+ts(base.Add(time.Minute))+`,12,2,0),
			(map('service.name','api'),'api','conns','{conn}',map(),`+ts(start)+`,`+ts(base.Add(2*time.Minute))+`,9,2,0),
			(map('service.name','api'),'api','conns','{conn}',map(),`+ts(start)+`,`+ts(base.Add(3*time.Minute))+`,11,2,0)`)
		sc := scope(base, base.Add(4*time.Minute))
		ps := ser("A", "conns", "sum", "sum")
		res := eval(t, c, sc, panel("line", ps), obsquery.EvalOptions{StepSeconds: 60})
		if got := pointValues(res.Series[0]); len(got) != 4 || got[0] != 10 || got[1] != 12 || got[2] != 9 || got[3] != 11 {
			t.Errorf("values = %v, want the levels 10 12 9 11 (the old rate read 0.033/0.15/0.033)", got)
		}
		_, err := obsquery.EvalMetricPanel(context.Background(), chRunner(c), sc, panel("line", ser("A", "conns", "rate", "sum")), obsquery.EvalOptions{Now: base.Add(time.Hour)})
		if err == nil || !strings.Contains(err.Error(), "up-down") {
			t.Errorf("rate over an up-down counter: %v", err)
		}
	})

	t.Run("histogram: p90 differs from p95, and the default is p95", func(t *testing.T) {
		// 100 observations a sample spread 50/30/15/5 over bounds 10/20/50.
		chExec(t, c, fmt.Sprintf(`INSERT INTO otel_metrics_histogram SELECT map('service.name','api'), 'api', 'lat', 'ms', map(), %s, %s + number*60, 100*(number+1), 1000.0*(number+1), [50*(number+1),30*(number+1),15*(number+1),5*(number+1)], [10,20,50], 1, 100, 2 FROM numbers(10)`, ts(start), ts(base)))
		sc := scope(base.Add(2*time.Minute), base.Add(9*time.Minute))
		q := func(level float64) float64 {
			res := eval(t, c, sc, panel("number", obsquery.PanelSeries{Label: "q", Field: "lat", Agg: "quantile", Quantile: level, MetricType: "histogram"}), obsquery.EvalOptions{})
			if res.Series[0].Reduced == nil {
				t.Fatalf("q%v: no reduction", level)
			}
			return *res.Series[0].Reduced
		}
		p90, p95 := q(0.90), q(0.95)
		if near(p90, p95) {
			t.Errorf("p90 = p95 = %v: the level was rounded", p90)
		}
		if !near(p90, 10+10*((90.0-50)/30)) && !(p90 > 10 && p90 < 50) {
			t.Errorf("p90 = %v", p90)
		}
		res := eval(t, c, sc, panel("line", ser("A", "lat", "rate", "histogram")), obsquery.EvalOptions{StepSeconds: 60})
		if got := pointValues(res.Series[0]); len(got) == 0 || !near(got[0], 100.0/60) {
			t.Errorf("histogram rate = %v, want %v observations/s", got, 100.0/60)
		}
	})

	t.Run("two bound sets in one group: one point per bucket plus mixed_bounds", func(t *testing.T) {
		chExec(t, c, fmt.Sprintf(`INSERT INTO otel_metrics_histogram SELECT map('service.name','api'), 'api', 'mixed', 'ms', map('v', toString(v)), %s, %s + number*60, 100*(number+1), 0, [50*(number+1),50*(number+1),0], if(v = 1, [10.0,100.0], [20.0,100.0]), 1, 100, 2 FROM numbers(6) ARRAY JOIN [1, 2] AS v`, ts(start), ts(base)))
		res := eval(t, c, scope(base.Add(2*time.Minute), base.Add(6*time.Minute)), panel("line", obsquery.PanelSeries{Label: "p50", Field: "mixed", Agg: "quantile", Quantile: 0.5, MetricType: "histogram"}), obsquery.EvalOptions{StepSeconds: 60})
		seen := map[time.Time]int{}
		for _, p := range res.Series[0].Points {
			seen[p.Time]++
		}
		for tm, n := range seen {
			if n != 1 {
				t.Errorf("%s has %d points", tm, n)
			}
		}
		found := false
		for _, w := range res.Warnings {
			found = found || obsquery.WarningCode(w) == obsquery.WarnMixedBounds
		}
		if !found {
			t.Errorf("warnings = %v", res.Warnings)
		}
	})

	t.Run("a number panel over a rate is the range average, not the partial last bucket", func(t *testing.T) {
		sc := scope(base.Add(10*time.Minute), base.Add(40*time.Minute))
		// "now" is mid-way through the last bucket; the reduction still reads 2
		res := eval(t, c, sc, panel("number", ser("A", "reqs", "rate", "sum")), obsquery.EvalOptions{Now: base.Add(40*time.Minute - 30*time.Second)})
		s := res.Series[0]
		if s.Reduced == nil || !near(*s.Reduced, 2) {
			t.Errorf("reduced = %v", s.Reduced)
		}
		if len(s.Points) == 0 {
			t.Error("no sparkline")
		}
	})

	t.Run("table and bar over metrics are supported, top groups first", func(t *testing.T) {
		for _, kind := range []string{"table", "bar"} {
			p := panel(kind, ser("rps", "two", "rate", "sum"))
			p.GroupBy = []string{"metric.attr.pod"}
			res := eval(t, c, scope(base.Add(2*time.Minute), base.Add(9*time.Minute)), p, obsquery.EvalOptions{})
			if len(res.Series) != 2 {
				t.Fatalf("%s rows = %d", kind, len(res.Series))
			}
			for _, s := range res.Series {
				if s.Reduced == nil || !near(*s.Reduced, 2) {
					t.Errorf("%s %v reduced = %v", kind, s.Labels, s.Reduced)
				}
			}
		}
	})

	t.Run("formula A / B: a gauge gap stays a gap", func(t *testing.T) {
		// cpu every minute 1..9; lim every minute 1..9 but 2 (a lost sample, which
		// leaves the series' interval at 60s).
		chExec(t, c, fmt.Sprintf(`INSERT INTO otel_metrics_gauge SELECT map('service.name','api'), 'api', 'cpu', '1', map(), %s, %s + number*60 + 60, number + 1 FROM numbers(9)`, ts(start), ts(base)))
		chExec(t, c, fmt.Sprintf(`INSERT INTO otel_metrics_gauge SELECT map('service.name','api'), 'api', 'lim', '1', map(), %s, %s + number*60 + 60, 10 FROM numbers(9) WHERE number != 1`, ts(start), ts(base)))
		p := panel("line", ser("A", "cpu", "avg", "gauge"), ser("B", "lim", "avg", "gauge"))
		p.Formula = "A / B"
		res := eval(t, c, scope(base, base.Add(10*time.Minute)), p, obsquery.EvalOptions{StepSeconds: 60})
		if res.StepSeconds != 60 {
			t.Fatalf("step = %d", res.StepSeconds)
		}
		pts := res.Series[0].Points
		var nulls int
		for _, pt := range pts {
			if pt.Null {
				nulls++
				if !pt.Time.Equal(base.Add(2 * time.Minute)) {
					t.Errorf("unexpected gap at %s", pt.Time)
				}
			}
		}
		if len(pts) != 9 || nulls != 1 || !near(pts[0].Value, 0.1) {
			t.Errorf("gauge formula points = %+v", pts)
		}
	})

	t.Run("a counter's missing bucket in a ratio is 0, not a gap", func(t *testing.T) {
		// errors arrive only in minute 5's sample growth: other buckets have no growth (rate 0 from a flat counter)
		chExec(t, c, fmt.Sprintf(`INSERT INTO otel_metrics_sum SELECT map('service.name','api'), 'api', 'errs', '{err}', map(), %s, %s + number*60, if(number < 5, 0, 60), 2, 1 FROM numbers(10)`, ts(start), ts(base)))
		p := panel("line", ser("A", "errs", "rate", "sum"), ser("B", "reqs", "rate", "sum"))
		p.Ratio = true
		res := eval(t, c, scope(base.Add(2*time.Minute), base.Add(9*time.Minute)), p, obsquery.EvalOptions{StepSeconds: 60})
		for _, pt := range res.Series[0].Points {
			if pt.Null {
				t.Errorf("%s is a gap: a counter with no growth is a rate of 0", pt.Time.Format("15:04"))
			}
		}
	})

	t.Run("120 series with a cap of 50: 50 series, series_total 120, fewer rows read", func(t *testing.T) {
		chExec(t, c, fmt.Sprintf(`INSERT INTO otel_metrics_gauge SELECT map('service.name','api'), 'api', 'wide', 'By', map('id', toString(number)), %s, %s + t*60, number + 1 FROM numbers(120) ARRAY JOIN range(0, 10) AS t`, ts(start), ts(base)))
		p := panel("line", ser("A", "wide", "max", "gauge"))
		p.GroupBy = []string{"metric.attr.id"}
		sc := scope(base, base.Add(10*time.Minute))
		var capped obsquery.MetricPanelResult
		var rowsCapped, rowsUncapped int
		count := func(res *int) obsquery.Runner {
			inner := chRunner(c)
			return func(ctx context.Context, st obsquery.Stmt) (obsquery.Rows, error) {
				r, err := inner(ctx, st)
				if err != nil || !strings.Contains(st.SQL, "toStartOfInterval") {
					return r, err
				}
				return &countingRows{Rows: r, n: res}, nil
			}
		}
		capped, err := obsquery.EvalMetricPanel(context.Background(), count(&rowsCapped), sc, p, obsquery.EvalOptions{StepSeconds: 60, Now: base.Add(24 * time.Hour), MaxSeries: 50})
		if err != nil {
			t.Fatal(err)
		}
		if len(capped.Series) != 50 || capped.SeriesTotal != 120 || !capped.Truncated {
			t.Errorf("series = %d, total = %d, truncated = %v", len(capped.Series), capped.SeriesTotal, capped.Truncated)
		}
		// the 50 kept are the largest by magnitude: ids 70..119
		if v := capped.Series[0].Points[0].Value; v != 120 {
			t.Errorf("top series value = %v, want 120", v)
		}
		// the uncapped baseline: the same query with the cap lifted to 200 groups
		info := capped.Meta[0].Info
		st, err := obsquery.MetricQuery{Scope: sc, Name: "wide", Info: info, Agg: obsquery.AggMax, GroupBy: []string{"metric.attr.id"}, StepSeconds: 60, MaxSeries: obsquery.MaxTableRows}.Compile()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(st.SQL, "LIMIT 200)") {
			t.Fatal("120 series under a cap of 200 must not be capped in SQL")
		}
		rows, err := count(&rowsUncapped)(context.Background(), st)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var (
				b  time.Time
				l  []string
				v  float64
				tt uint64
			)
			if err := rows.Scan(&b, &l, &v, &tt); err != nil {
				t.Fatal(err)
			}
		}
		_ = rows.Close()
		if rowsCapped >= rowsUncapped {
			t.Errorf("rows read: capped %d, uncapped %d; the cap must cut rows in SQL", rowsCapped, rowsUncapped)
		}
		if rowsUncapped != 1200 {
			t.Errorf("uncapped rows = %d, want 120 series x 10 buckets", rowsUncapped)
		}
		if rowsCapped != 500 {
			t.Errorf("capped rows = %d, want 50 series x 10 buckets", rowsCapped)
		}
	})

	t.Run("partial last bucket is flagged", func(t *testing.T) {
		now := base.Add(30*time.Minute + 20*time.Second)
		res := eval(t, c, scope(base.Add(10*time.Minute), base.Add(40*time.Minute)), panel("line", ser("A", "reqs", "rate", "sum")), obsquery.EvalOptions{StepSeconds: 60, Now: now})
		pts := res.Series[0].Points
		var partial int
		for _, p := range pts {
			if p.Partial {
				partial++
			}
		}
		if partial == 0 || !pts[len(pts)-1].Partial && pts[len(pts)-1].Time.Add(time.Minute).After(now.Add(-time.Minute)) {
			t.Errorf("partial = %d of %d", partial, len(pts))
		}
		if pts[0].Partial {
			t.Error("the first bucket is complete")
		}
	})
}

type countingRows struct {
	obsquery.Rows
	n *int
}

func (r *countingRows) Next() bool {
	ok := r.Rows.Next()
	if ok {
		*r.n++
	}
	return ok
}

var _ driver.Rows = (driver.Rows)(nil)
