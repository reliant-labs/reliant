package obsquery

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// rowsFor serves one fixed result for every statement and records the one it
// was asked.
func rowsFor(rows Rows, err error, got *Stmt) Runner {
	return func(_ context.Context, st Stmt) (Rows, error) {
		if got != nil {
			*got = st
		}
		return rows, err
	}
}

func TestReadWrappersCompileRunAndParse(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("clickhouse is down")

	t.Run("ReadUserActivity", func(t *testing.T) {
		q := userActivityQuery()
		var st Stmt
		got, err := ReadUserActivity(ctx, rowsFor(&fakeRows{}, nil, &st), q)
		if err != nil || len(got) != 1 || got[0].EnvID != "env-prod" || got[0].Current != (UserActivityCounts{}) {
			t.Fatalf("no events: the scoped env is listed with zero counts: %+v, %v", got, err)
		}
		want, _ := q.Compile()
		if st.SQL != want.SQL || len(st.Params) != len(want.Params) {
			t.Errorf("the runner got a statement other than Compile's:\n%s", st.SQL)
		}
		if _, err := ReadUserActivity(ctx, rowsFor(nil, boom, nil), q); !errors.Is(err, boom) {
			t.Errorf("a failing runner: %v", err)
		}
		bad := q
		bad.Scope.Database = ""
		if _, err := ReadUserActivity(ctx, func(context.Context, Stmt) (Rows, error) {
			t.Fatal("a statement ran for an invalid query")
			return nil, nil
		}, bad); err == nil {
			t.Error("an invalid query must fail before running")
		}
	})

	t.Run("ReadExperimentReadout", func(t *testing.T) {
		q := experimentQuery()
		got, err := ReadExperimentReadout(ctx, rowsFor(&fakeRows{rows: [][]any{
			{"arm", "treatment", uint64(40), uint64(9), uint64(30), uint64(8), at(2), at(9)},
		}}, nil, nil), q)
		if err != nil || got.Assigned != 40 || len(got.Arms) != 2 {
			t.Errorf("readout = %+v, %v", got, err)
		}
		if _, err := ReadExperimentReadout(ctx, rowsFor(nil, boom, nil), q); !errors.Is(err, boom) {
			t.Errorf("a failing runner: %v", err)
		}
		bad := q
		bad.Scope.Database = ""
		if _, err := ReadExperimentReadout(ctx, rowsFor(&fakeRows{}, nil, nil), bad); err == nil {
			t.Error("an invalid query must fail before running")
		}
	})
}

func TestCatalogKindNames(t *testing.T) {
	for kind, want := range map[CatalogKind]string{
		CatalogGauge: "gauge", CatalogSum: "sum", CatalogHistogram: "histogram",
		CatalogExpHistogram: "exp_histogram", CatalogSummary: "summary", CatalogKind(99): "unknown",
	} {
		if got := kind.String(); got != want {
			t.Errorf("CatalogKind(%d).String() = %q, want %q", kind, got, want)
		}
	}
}

func TestDeclaredKeysCompile(t *testing.T) {
	st, err := DeclaredKeys{Scope: scope(), Name: "reqs", Type: MetricSum}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"arrayJoin(mapKeys(Attributes)) AS k", "t_org_a.otel_metrics_sum", "MetricName =", "GROUP BY k ORDER BY k"} {
		if !strings.Contains(st.SQL, want) {
			t.Errorf("SQL lacks %q:\n%s", want, st.SQL)
		}
	}
	assertParameterized(t, st, "reqs")
	if _, err := (DeclaredKeys{Scope: scope(), Name: "reqs"}).Compile(); !isInvalid(err) {
		t.Errorf("an unknown metric type: %v", err)
	}
	bad := scope()
	bad.Database = "a b"
	if _, err := (DeclaredKeys{Scope: bad, Name: "reqs", Type: MetricSum}).Compile(); err == nil {
		t.Error("an unsafe database name must be refused")
	}
}

func TestErrInvalidfIsErrInvalid(t *testing.T) {
	err := ErrInvalidf("range %d is too long", 9)
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "range 9 is too long") {
		t.Errorf("err = %v", err)
	}
}

func TestUptimeStatementsAreBoundPanelSQL(t *testing.T) {
	for name, sql := range map[string]string{
		"checks":    UptimeChecksSQL(),
		"summary":   UptimeSummarySQL(),
		"incidents": UptimeIncidentsSQL(),
		"timeline":  UptimeTimelineSQL(),
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(sql, "otel_metrics_gauge") || !strings.Contains(sql, "{from:DateTime64(9)}") || !strings.Contains(sql, "{env_ids:Array(String)}") {
				t.Errorf("not bound to the panel scope:\n%s", sql)
			}
			if strings.Count(sql, "(") != strings.Count(sql, ")") {
				t.Errorf("unbalanced parentheses:\n%s", sql)
			}
			if !strings.Contains(sql, release.UptimeMetricUp) {
				t.Errorf("does not read the up gauge %q", release.UptimeMetricUp)
			}
			head := strings.TrimSpace(sql)
			if !strings.HasPrefix(head, "WITH") {
				t.Errorf("a read-only statement is one WITH … SELECT, got %q", head[:min(20, len(head))])
			}
			for _, banned := range []string{"INSERT", "ALTER", "DROP", "TRUNCATE", ";"} {
				if strings.Contains(sql, banned) {
					t.Errorf("a read-only statement must not contain %q", banned)
				}
			}
		})
	}
	if c := UptimeChecksSQL(); !strings.Contains(c, "LIMIT 500") || !strings.Contains(c, `AS "Availability %"`) {
		t.Errorf("checks lacks its row cap or availability column:\n%s", c)
	}
	if !strings.Contains(UptimeChecksSQL(), "INTERVAL "+UptimeStaleAfter) || !strings.Contains(UptimeIncidentsSQL(), "INTERVAL "+UptimeLookback) {
		t.Error("the stale-after and lookback intervals are the exported constants")
	}
	if !strings.Contains(UptimeIncidentsSQL(), "f.failures >= 3") {
		t.Errorf("an incident opens at UptimeIncidentFailures (%d) failed probes in a row", UptimeIncidentFailures)
	}
	if !strings.Contains(UptimeTimelineSQL(), "{step:UInt32}") {
		t.Error("the timeline buckets by the panel's {step}")
	}
}

func TestPanelPredicateRejectionsAndForms(t *testing.T) {
	count := []PanelSeries{{Label: "n", Agg: "count"}}
	sc := Scope{Database: "t_org_a", Start: t0, End: t0.Add(time.Hour)}
	logs := func(where ...PanelPredicate) Panel {
		return Panel{ID: "p", Kind: "number", Signal: "logs", Series: count, Where: where}
	}
	compile := func(p Panel) (Stmt, error) {
		st, _, err := PanelQuery{Scope: sc, Panel: p, StepSeconds: 60}.Compile()
		return st, err
	}

	for name, p := range map[string]Panel{
		"severity eq":         logs(PanelPredicate{Field: "log.severity", Op: "eq", Value: "error"}),
		"severity in":         logs(PanelPredicate{Field: "log.severity", Op: "in", Values: []string{"warn", "error"}}),
		"severity not_in":     logs(PanelPredicate{Field: "log.severity", Op: "not_in", Values: []string{"debug"}}),
		"severity neq":        logs(PanelPredicate{Field: "log.severity", Op: "neq", Value: "info"}),
		"span kind in":        tp("number", count, []PanelPredicate{{Field: "span.kind", Op: "in", Values: []string{"SERVER", "client"}}}),
		"span status not_in":  tp("number", count, []PanelPredicate{{Field: "span.status", Op: "not_in", Values: []string{"ok"}}}),
		"span kind neq":       tp("number", count, []PanelPredicate{{Field: "span.kind", Op: "neq", Value: "internal"}}),
		"numeric neq":         tp("number", count, []PanelPredicate{{Field: "span.duration", Op: "neq", Value: "5"}}),
		"numeric exists":      tp("number", count, []PanelPredicate{{Field: "span.duration", Op: "exists"}}),
		"string exists":       tp("number", count, []PanelPredicate{{Field: "span.name", Op: "exists"}}),
		"attribute exists":    tp("number", count, []PanelPredicate{{Field: "span.attr.http.route", Op: "exists"}}),
		"resource attr group": tp("table", count, nil, "resource.attr.k8s.pod.name"),
	} {
		t.Run("accepts "+name, func(t *testing.T) {
			st, err := compile(p)
			if err != nil {
				t.Fatal(err)
			}
			assertParameterized(t, st)
		})
	}

	for name, tc := range map[string]struct {
		p    Panel
		want string
	}{
		"severity with an unsupported op": {logs(PanelPredicate{Field: "log.severity", Op: "match", Values: []string{"error"}}), "log.severity supports"},
		"severity in with a bad name":     {logs(PanelPredicate{Field: "log.severity", Op: "in", Values: []string{"warn", "loud"}}), "unknown severity"},
		"severity in with no values":      {logs(PanelPredicate{Field: "log.severity", Op: "in"}), "needs 1-"},
		"span kind with a regex":          {tp("number", count, []PanelPredicate{{Field: "span.kind", Op: "match", Value: "s.*"}}), "span.kind supports eq, neq, in and not_in"},
		"span kind in with no values":     {tp("number", count, []PanelPredicate{{Field: "span.kind", Op: "in"}}), "needs 1-"},
		"numeric with a regex":            {tp("number", count, []PanelPredicate{{Field: "span.duration", Op: "match", Value: "5"}}), "supports eq, neq and gte with a number"},
		"numeric eq with text":            {tp("number", count, []PanelPredicate{{Field: "span.duration", Op: "eq", Value: "fast"}}), "supports eq, neq and gte with a number"},
		"a value too long":                {tp("number", count, []PanelPredicate{{Field: "span.name", Op: "eq", Value: strings.Repeat("a", 2000)}}), "too long"},
		"too many predicates":             {tp("number", count, manyPredicates(MaxPanelPredicates+1)), "at most"},
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			_, err := compile(tc.p)
			if !isInvalid(err) || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want InvalidArgument mentioning %q", err, tc.want)
			}
		})
	}
}

func manyPredicates(n int) []PanelPredicate {
	out := make([]PanelPredicate, n)
	for i := range out {
		out[i] = PanelPredicate{Field: "span.name", Op: "eq", Value: "x"}
	}
	return out
}

func TestPanelSeriesAggregationsOverAnAttribute(t *testing.T) {
	sc := Scope{Database: "t_org_a", Start: t0, End: t0.Add(time.Hour)}
	for agg, fn := range map[string]string{
		"sum": "sumIf(", "avg": "sumIf(", "max": "maxIf(", "last": "argMaxIf(", "quantile": "quantileExactWeightedIf(0.5)(",
	} {
		t.Run(agg, func(t *testing.T) {
			series := PanelSeries{Label: "x", Agg: agg, Field: "span.attr.items"}
			if agg == "quantile" {
				series.Quantile = 0.5
			}
			st, _, err := PanelQuery{Scope: sc, Panel: tp("line", []PanelSeries{series}, nil), StepSeconds: 60}.Compile()
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(st.SQL, fn) || !strings.Contains(st.SQL, "toFloat64OrNull(SpanAttributes[") {
				t.Errorf("want %s over the attribute read as a number:\n%s", fn, st.SQL)
			}
			assertParameterized(t, st)
		})
	}
}

func TestSparklineStepBoundaries(t *testing.T) {
	prev := int64(0)
	for _, rng := range []time.Duration{time.Second, 5 * time.Minute, time.Hour, 6 * time.Hour, 24 * time.Hour, 7 * 24 * time.Hour, 30 * 24 * time.Hour} {
		step := SparklineStep(rng)
		if step < 1 || int64(rng.Seconds())/step > 200 {
			t.Errorf("SparklineStep(%v) = %d: a sparkline has at least 1s buckets and a bounded number of points", rng, step)
		}
		if step < prev {
			t.Errorf("SparklineStep(%v) = %d is finer than a shorter range's %d", rng, step, prev)
		}
		prev = step
	}
}

func TestMetricQueryHelpers(t *testing.T) {
	if got := (MetricQuery{Agg: AggP95}).QuantileLevel(); got != 0.95 {
		t.Errorf("p95 level = %v", got)
	}
	if got := (MetricQuery{Quantile: 0.5}).QuantileLevel(); got != 0.5 {
		t.Errorf("an explicit quantile = %v", got)
	}
	if got := niceStep(0); got != 1 {
		t.Errorf("niceStep(0) = %d, want 1", got)
	}
	if got := niceStep(90_000); got != 86400 {
		t.Errorf("niceStep(90000) = %d, want a day", got)
	}
}
