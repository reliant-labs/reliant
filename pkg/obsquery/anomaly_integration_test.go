//go:build integration

package obsquery_test

// ServiceAnomaly against a real ClickHouse: four weeks of rpc.server.* metrics
// for a handful of services, evaluated on a Monday at 09:17 (the weekly peak).
//
//	go test -tags integration -run Anomal ./pkg/obsquery/

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"

	"github.com/reliant-labs/forge/pkg/obsquery"
)

// anomalyNow is a Monday, in the hour every fixture's traffic peaks.
var anomalyNow = time.Date(2026, 10, 26, 9, 17, 0, 0, time.UTC)

// svcShape is one service's traffic: requests per minute off-peak and at the
// Monday 09:00 peak, its usual error fraction, and what happens inside the
// observed window (09:00 to 09:15 on the 26th).
type svcShape struct {
	name, env   string
	off, peak   int     // requests per minute
	errFrac     float64 // server errors, as a fraction of requests
	userErrFrac float64 // errors of class `user`, which never count
	obsErrFrac  float64 // errFrac inside the observed window (0: unchanged)
	obsSlow     bool    // latency 10x inside the observed window
	age         time.Duration
}

// seedAnomalyService writes one cumulative sample a minute from anomalyNow-age
// to anomalyNow for the service's three metrics.
func seedAnomalyService(t *testing.T, c clickhouse.Conn, s svcShape) {
	t.Helper()
	start := anomalyNow.Add(-s.age)
	n := int(anomalyNow.Sub(start)/time.Minute) + 1
	obsFrom := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	inObs := fmt.Sprintf("ts >= toDateTime(%d)", obsFrom.Unix())
	// A few requests of jitter keep every spread honest.
	reqs := fmt.Sprintf("toUInt64(if(toDayOfWeek(ts) = 1 AND toHour(ts) = 9, %d, %d) + cityHash64(toString(ts), '%s') %% 3)", s.peak, s.off, s.name)
	frac := fmt.Sprintf("%g", s.errFrac)
	if s.obsErrFrac > 0 {
		frac = fmt.Sprintf("if(%s, %g, %g)", inObs, s.obsErrFrac, s.errFrac)
	}
	res := fmt.Sprintf("map('service.name', '%s', 'deployment.environment.name', '%s')", s.name, s.env)
	minutes := fmt.Sprintf("SELECT toDateTime(%d) + number * 60 AS ts FROM numbers(%d)", start.Unix(), n)
	counter := func(metric, attrs, perMinute string) {
		exec := fmt.Sprintf(`INSERT INTO otel_metrics_sum
SELECT %s, '%s', '%s', '{req}', %s, toDateTime(%d), ts, cum, 2, 1
FROM (SELECT ts, sum(v) OVER (ORDER BY ts ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS cum
      FROM (SELECT ts, %s AS v FROM (%s)))`, res, s.name, metric, attrs, start.Unix(), perMinute, minutes)
		chExec(t, c, exec)
	}
	counter("rpc.server.calls", "map('procedure', '/a.v1.S/M')", reqs)
	if s.errFrac > 0 || s.obsErrFrac > 0 {
		counter("rpc.server.errors", "map('procedure', '/a.v1.S/M', 'error_class', 'internal')",
			fmt.Sprintf("toUInt64(round(%s * %s))", reqs, frac))
	}
	if s.userErrFrac > 0 {
		counter("rpc.server.errors", "map('procedure', '/a.v1.S/M', 'error_class', 'user')",
			fmt.Sprintf("toUInt64(round(%s * %g))", reqs, s.userErrFrac))
	}
	// Latency, as delta histograms over bounds 0.05 0.1 0.25 0.5 1 2.5: 90% of
	// requests in (0.05, 0.1], 9% in (0.1, 0.25], 1% in (0.25, 0.5] (p95 about
	// 180 ms); ten times slower, 90% in (0.5, 1] (p95 about 1.8 s).
	fast := "[toUInt64(0), toUInt64(round(r * 0.90)), toUInt64(round(r * 0.09)), toUInt64(round(r * 0.01)), 0, 0, 0]"
	slow := "[toUInt64(0), 0, 0, 0, toUInt64(round(r * 0.90)), toUInt64(round(r * 0.09)), toUInt64(round(r * 0.01))]"
	counts := fast
	if s.obsSlow {
		counts = fmt.Sprintf("if(%s, %s, %s)", inObs, slow, fast)
	}
	chExec(t, c, fmt.Sprintf(`INSERT INTO otel_metrics_histogram
SELECT %s, '%s', 'rpc.server.duration', 's', map('procedure', '/a.v1.S/M'), toDateTime(%d), ts, r, r * 0.07, %s, [0.05, 0.1, 0.25, 0.5, 1, 2.5], 0, 0, 1
FROM (SELECT ts, %s AS r FROM (%s))`, res, s.name, start.Unix(), counts, reqs, minutes))
}

func TestServiceAnomalyAgainstClickHouse(t *testing.T) {
	c := startClickHouse(t)
	chExec(t, c, strings.Split(metricTables, ";\n")...)

	// Data from just over 28 days back, so Monday 09:00 has four past weeks.
	const day, old = 24 * time.Hour, 28*24*time.Hour + time.Hour
	shapes := []svcShape{
		// Monday peak: ten times the off-peak traffic, the usual 0.5% errors, unchanged
		// in the observed window. A flat baseline would call this a surge.
		{name: "peak", env: "prod", off: 30, peak: 300, errFrac: 0.005, age: old},
		// A real 5x error-rate jump (0.5% -> 2.5%) on peak traffic.
		{name: "jump", env: "prod", off: 30, peak: 300, errFrac: 0.005, obsErrFrac: 0.025, age: old},
		// The same jump on a service that serves 1 request a minute: 15 in the window.
		{name: "quiet", env: "prod", off: 1, peak: 1, errFrac: 0.01, obsErrFrac: 0.6, age: old},
		// A jump from 0.5% to 1.2%: 2.4x of a small number, under the 1 point floor.
		{name: "smalldelta", env: "prod", off: 30, peak: 300, errFrac: 0.005, obsErrFrac: 0.012, age: old},
		// The jump again, from a service that has existed for 3 days.
		{name: "young", env: "prod", off: 30, peak: 300, errFrac: 0.005, obsErrFrac: 0.025, age: 3 * day},
		// A flood of errors of class `user` (the caller's fault), which are not the service's.
		{name: "usererrs", env: "prod", off: 30, peak: 300, errFrac: 0.005, userErrFrac: 0.4, age: old},
		// p95 ten times slower, with errors and traffic as usual.
		{name: "slow", env: "prod", off: 30, peak: 300, errFrac: 0.005, obsSlow: true, age: old},
		// Another environment's service with the jump: out of scope.
		{name: "jump", env: "staging", off: 30, peak: 300, errFrac: 0.005, obsErrFrac: 0.025, age: old},
	}
	for _, s := range shapes {
		seedAnomalyService(t, c, s)
	}

	sc := obsquery.Scope{
		Database: "default", Local: true,
		Filters: []obsquery.AttrFilter{{Key: "deployment.environment.name", Op: "eq", Value: "prod"}},
	}
	q := obsquery.AnomalyQuery{Scope: sc, Params: obsquery.DefaultAnomalyParams(), Now: anomalyNow}
	res, err := obsquery.EvalAnomalies(context.Background(), chRunner(c), q)
	if err != nil {
		t.Fatal(err)
	}

	fired := map[string][]string{}
	for _, a := range res.Anomalies {
		fired[a.Service] = append(fired[a.Service], a.Signal)
	}
	for _, v := range fired {
		sort.Strings(v)
	}
	want := map[string]string{
		"jump": obsquery.AnomalySignalErrorRate,
		"slow": obsquery.AnomalySignalP95,
	}
	for svc, w := range want {
		if got := strings.Join(fired[svc], ","); got != w {
			t.Errorf("%s fired %q, want %q", svc, got, w)
		}
	}
	for _, svc := range []string{"peak", "quiet", "smalldelta", "young", "usererrs"} {
		if got := fired[svc]; len(got) != 0 {
			t.Errorf("%s fired %v, want silence", svc, got)
		}
	}
	if len(fired) != len(want) {
		t.Errorf("fired for %v, want exactly %v", fired, want)
	}

	// What the judge saw.
	by := map[string]obsquery.ServiceREDInput{}
	for _, in := range res.Inputs {
		by[in.Service] = in
	}
	if len(by) != 7 {
		t.Errorf("judged %d services, want the 7 prod ones (staging's jump is out of scope): %v", len(by), keys(by))
	}
	peak := by["peak"]
	if peak.Observed.Requests < 4000 || peak.Observed.Requests > 4600 {
		t.Errorf("peak's window has %v requests, want about 15 x 302", peak.Observed.Requests)
	}
	if r := peak.Observed.ErrorRate(); r < 0.005 || r > 0.008 {
		t.Errorf("peak's observed error rate = %v, want about 0.0066 (2 errors of 301 requests a minute)", r)
	}
	if len(peak.Baseline) != 16 {
		t.Errorf("peak's baseline has %d slots, want 4 weeks x 4 slots of the 09:00 hour", len(peak.Baseline))
	}
	for _, s := range peak.Baseline {
		if s.Requests < 4000 {
			t.Errorf("a Monday-09:00 baseline slot has %v requests: the baseline read the wrong hour of the week", s.Requests)
			break
		}
	}
	if y := by["young"]; y.HistoryDays() != 3 {
		t.Errorf("young has %d days of history, want 3", y.HistoryDays())
	}
	if p := by["peak"]; p.HistoryDays() != 28 {
		t.Errorf("peak has %d days of history, want the 28 the lookback reads", p.HistoryDays())
	}
	if !by["peak"].Observed.HasP95 || by["slow"].Observed.P95 < 0.5 || by["peak"].Observed.P95 > 0.25 {
		t.Errorf("p95: peak %v slow %v", by["peak"].Observed.P95, by["slow"].Observed.P95)
	}
	if u := by["usererrs"].Observed; u.Errors > 0.01*u.Requests*2 {
		t.Errorf("usererrs counted %v errors of %v requests: class `user` must not count", u.Errors, u.Requests)
	}

	// The jump's payload names what happened.
	for _, a := range res.Anomalies {
		if a.Service != "jump" {
			continue
		}
		if !a.Up || a.Observed < 0.02 || a.Observed > 0.03 || a.Baseline < 0.005 || a.Baseline > 0.008 || a.Limit <= a.Baseline || a.Limit >= a.Observed {
			t.Errorf("jump finding = %+v", a)
		}
		w := a.Why("prod", q)
		if w.DedupeKey != "service_anomaly/jump/-" || len(w.Next) == 0 || w.HistoryDays != 28 {
			t.Errorf("why = %+v", w)
		}
	}

	// The same minute a week earlier is inside its own baseline for every
	// service: three past weeks of data, and nothing had happened yet.
	q.Now = time.Date(2026, 10, 19, 9, 17, 0, 0, time.UTC)
	if res, err = obsquery.EvalAnomalies(context.Background(), chRunner(c), q); err != nil {
		t.Fatal(err)
	}
	for _, a := range res.Anomalies {
		t.Errorf("the same minute a week earlier fired %+v", a)
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
