package obsquery

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// anomalyRunner answers EvalAnomalies' statements the way ClickHouse would
// for one service, "api", that has existed for 30 days: every statement is
// told apart by what it reads (the first-seen lookup, a metric's lookup, or
// its series) and by its shape (a "toStartOfInterval" bucket is a baseline
// slot read; without it, the observed window's reduction).
type anomalyRunner struct {
	t          *testing.T
	q          AnomalyQuery
	first      time.Time
	baseReqs   float64
	baseErrs   float64
	obsReqs    float64
	obsErrs    float64
	interval   uint64 // the calls counter's export interval
	failOn     string // a substring of a statement that fails
	statements []string
}

func (r *anomalyRunner) metric(st Stmt) string {
	for _, v := range st.Params {
		if strings.HasPrefix(v, "rpc.server.") {
			return v
		}
	}
	return ""
}

func (r *anomalyRunner) run(_ context.Context, st Stmt) (Rows, error) {
	r.statements = append(r.statements, st.SQL)
	if r.failOn != "" && strings.Contains(st.SQL, r.failOn) {
		return nil, errors.New("clickhouse is down")
	}
	switch {
	case strings.Contains(st.SQL, "AS service, min(TimeUnix) AS first"):
		if r.first.IsZero() {
			return &anyRows{}, nil
		}
		return &anyRows{rows: [][]any{{"api", r.first}}}, nil
	case strings.Contains(st.SQL, "AS kind"):
		switch r.metric(st) {
		case "rpc.server.duration":
			return &fakeRows{rows: [][]any{lookupRow(MetricHistogram, "s", false, 2, 1, 60)}}, nil
		case "rpc.server.calls":
			return &fakeRows{rows: [][]any{lookupRow(MetricSum, "{req}", true, 2, 1, r.interval)}}, nil
		}
		return &fakeRows{rows: [][]any{lookupRow(MetricSum, "{req}", true, 2, 1, 60)}}, nil
	}
	slotted := strings.Contains(st.SQL, "toStartOfInterval")
	w := r.q.windows(r.q.now())
	var buckets []time.Time
	if slotted {
		for _, sc := range w.baselines {
			for ts := sc.Start; ts.Before(sc.End); ts = ts.Add(w.step) {
				buckets = append(buckets, ts)
			}
		}
	} else {
		buckets = []time.Time{w.observed.Start}
	}
	var rows [][]any
	for i, b := range buckets {
		labels := []string{"api"}
		switch r.metric(st) {
		case "rpc.server.calls":
			v := r.obsReqs
			if slotted {
				v = r.baseReqs + float64(i%3)
			}
			rows = append(rows, []any{b, labels, v, uint64(1)})
		case "rpc.server.errors":
			v := r.obsErrs
			if slotted {
				v = r.baseErrs + float64(i%3)
			}
			rows = append(rows, []any{b, labels, v, uint64(1)})
		case "rpc.server.duration":
			// 100 requests, all inside (0.1, 0.25]: a p95 of about 0.24s, however many windows are merged.
			rows = append(rows, []any{b, labels, []float64{0.05, 0.1, 0.25}, []float64{0, 0, 100, 0}, uint64(1)})
		}
	}
	return &anyRows{rows: rows}, nil
}

func newAnomalyRunner(t *testing.T) (*anomalyRunner, AnomalyQuery) {
	t.Helper()
	q := AnomalyQuery{Scope: Scope{Database: "t_org"}, Params: DefaultAnomalyParams(), Now: anomalyNow}
	return &anomalyRunner{
		t: t, q: q, first: anomalyNow.Add(-30 * 24 * time.Hour),
		baseReqs: 4000, baseErrs: 20, obsReqs: 4000, obsErrs: 20, interval: 60,
	}, q
}

func TestEvalAnomaliesJudgesEveryServiceTheLookupFound(t *testing.T) {
	r, q := newAnomalyRunner(t)
	r.obsErrs = 400 // 10% against a baseline of about 0.5%
	res, err := EvalAnomalies(context.Background(), r.run, q)
	if err != nil {
		t.Fatal(err)
	}
	w := q.windows(anomalyNow)
	if !res.WindowStart.Equal(w.observed.Start) || !res.WindowEnd.Equal(w.observed.End) {
		t.Errorf("window = %v..%v, want %v..%v", res.WindowStart, res.WindowEnd, w.observed.Start, w.observed.End)
	}
	if len(res.Inputs) != 1 {
		t.Fatalf("inputs = %+v", res.Inputs)
	}
	in := res.Inputs[0]
	if in.Service != "api" || in.BaselineWeeks != 4 || len(in.Baseline) != 16 || in.Observed.Requests != 4000 || in.Observed.Errors != 400 || !in.Observed.HasP95 {
		t.Errorf("input = %+v", in)
	}
	if got := signals(res.Anomalies); got != AnomalySignalErrorRate {
		t.Fatalf("fired %q, want error_rate only: %+v", got, res.Anomalies)
	}
	a := res.Anomalies[0]
	if a.Service != "api" || !a.Up || a.Observed != 0.1 || a.BaselineWeeks != 4 || a.HistoryDays != 30 {
		t.Errorf("anomaly = %+v", a)
	}
	if res.Truncated || len(res.Warnings) != 0 {
		t.Errorf("truncated %v warnings %v on a clean run", res.Truncated, res.Warnings)
	}
}

func TestEvalAnomaliesFindsNothingWhenTheWeekIsNormal(t *testing.T) {
	r, q := newAnomalyRunner(t)
	res, err := EvalAnomalies(context.Background(), r.run, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Anomalies) != 0 || len(res.Inputs) != 1 {
		t.Errorf("anomalies %+v inputs %d", res.Anomalies, len(res.Inputs))
	}
}

func TestEvalAnomaliesJudgesNothingForAServiceWithNoWholeWeek(t *testing.T) {
	r, q := newAnomalyRunner(t)
	r.first = anomalyNow.Add(-3 * 24 * time.Hour) // existed for 3 days: no baseline week at all
	r.obsErrs = 400
	res, err := EvalAnomalies(context.Background(), r.run, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Inputs) != 1 || res.Inputs[0].BaselineWeeks != 0 || len(res.Anomalies) != 0 {
		t.Errorf("a young service is listed but never judged: %+v", res)
	}
}

func TestEvalAnomaliesWithNoServicesStopsAfterTheLookup(t *testing.T) {
	r, q := newAnomalyRunner(t)
	r.first = time.Time{}
	res, err := EvalAnomalies(context.Background(), r.run, q)
	if err != nil || len(res.Inputs) != 0 || len(res.Anomalies) != 0 {
		t.Fatalf("res %+v err %v", res, err)
	}
	if len(r.statements) != 1 || !strings.Contains(r.statements[0], "AS first") {
		t.Errorf("a lookup that found no service must be the only statement run, got %d", len(r.statements))
	}
}

func TestEvalAnomaliesReportsFailuresWhereTheyHappen(t *testing.T) {
	for _, tc := range []struct {
		name   string
		failOn string
		want   string
	}{
		{"the first-seen lookup", "AS service, min(TimeUnix) AS first", "service anomaly: first-seen lookup: clickhouse is down"},
		{"a metric lookup", "AS kind", "clickhouse is down"},
		{"a series read", "toStartOfInterval", "service anomaly: calls over"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, q := newAnomalyRunner(t)
			r.failOn = tc.failOn
			_, err := EvalAnomalies(context.Background(), r.run, q)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

func TestEvalAnomaliesRefusesInvalidLimitsBeforeAnyQuery(t *testing.T) {
	r, q := newAnomalyRunner(t)
	q.Params.K = 0
	_, err := EvalAnomalies(context.Background(), func(context.Context, Stmt) (Rows, error) {
		t.Fatal("a statement ran under invalid params")
		return nil, nil
	}, q)
	if !isInvalid(err) {
		t.Errorf("err = %v, want InvalidArgument", err)
	}
	_ = r
}

func TestEvalAnomaliesRefusesMetricsExportedSlowerThanItsWindow(t *testing.T) {
	r, q := newAnomalyRunner(t)
	r.interval = 1800 // every 30 minutes: a 15 minute window has no sample in half its slots
	_, err := EvalAnomalies(context.Background(), r.run, q)
	if err == nil || !strings.Contains(err.Error(), "export every 1800s, too long for a 15m0s window") {
		t.Errorf("err = %v", err)
	}
}

func TestEvalAnomaliesTreatsAnAbsentMetricAsNoData(t *testing.T) {
	_, q := newAnomalyRunner(t)
	absent := func(_ context.Context, st Stmt) (Rows, error) {
		switch {
		case strings.Contains(st.SQL, "AS service, min(TimeUnix) AS first"):
			return &anyRows{rows: [][]any{{"api", anomalyNow.Add(-30 * 24 * time.Hour)}}}, nil
		case strings.Contains(st.SQL, "AS kind"):
			return &fakeRows{}, nil // no table holds the metric
		}
		t.Fatalf("a series was read for a metric with no data: %s", st.SQL)
		return nil, nil
	}
	res, err := EvalAnomalies(context.Background(), absent, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Anomalies) != 0 || len(res.Inputs) != 1 || res.Inputs[0].Observed.Requests != 0 {
		t.Errorf("res = %+v", res)
	}
}

func TestAnomalyQueryNowDefaultsToTheClock(t *testing.T) {
	var q AnomalyQuery
	if d := time.Since(q.now()); d < 0 || d > time.Minute || q.now().Location() != time.UTC {
		t.Errorf("a zero Now is the current time in UTC, got %v ago", d)
	}
	q.Now = time.Date(2026, 1, 1, 12, 0, 0, 0, time.FixedZone("x", 3600))
	if got := q.now(); got.Hour() != 11 || got.Location() != time.UTC {
		t.Errorf("Now is read in UTC: %v", got)
	}
}

func TestREDSampleAndInputHelpers(t *testing.T) {
	if got := (REDSample{Requests: 200, Errors: 5}).ErrorRate(); got != 0.025 {
		t.Errorf("ErrorRate = %v", got)
	}
	if got := (REDSample{Errors: 5}).ErrorRate(); got != 0 {
		t.Errorf("no requests, no rate: %v", got)
	}
	in := ServiceREDInput{FirstSeen: anomalyNow.Add(-50 * time.Hour), Now: anomalyNow}
	if got := in.HistoryDays(); got != 2 {
		t.Errorf("HistoryDays = %d, want 2 whole days", got)
	}
	for name, in := range map[string]ServiceREDInput{
		"never seen":         {Now: anomalyNow},
		"seen in the future": {FirstSeen: anomalyNow.Add(time.Hour), Now: anomalyNow},
	} {
		if got := in.HistoryDays(); got != 0 {
			t.Errorf("%s: HistoryDays = %d, want 0", name, got)
		}
	}
}

func TestAnomalyParamsValidateRefusesEachBadLimit(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*AnomalyParams)
		want string
	}{
		{"a window that does not divide the hour", func(p *AnomalyParams) { p.Window = 7 * time.Minute }, "divides an hour"},
		{"a window under a minute", func(p *AnomalyParams) { p.Window = 30 * time.Second }, "divides an hour"},
		{"a negative delay", func(p *AnomalyParams) { p.Delay = -time.Second }, "delay must not be negative"},
		{"no weeks", func(p *AnomalyParams) { p.Weeks = 0 }, "weeks must be 1 to"},
		{"more weeks than the metrics range", func(p *AnomalyParams) { p.Weeks = 9 }, "weeks must be 1 to"},
		{"no k", func(p *AnomalyParams) { p.K = 0 }, "k must be positive"},
		{"a negative floor", func(p *AnomalyParams) { p.MinErrors = -1 }, "floors not negative"},
		{"a ratio under 1", func(p *AnomalyParams) { p.P95Ratio = 0.5 }, "ratios must be at least 1"},
		{"no baseline samples", func(p *AnomalyParams) { p.MinBaselineSamples = 0 }, "at least one baseline sample"},
		{"more baseline weeks than weeks read", func(p *AnomalyParams) { p.MinBaselineWeeks = 9 }, "baseline weeks must be 1 to"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := DefaultAnomalyParams()
			tc.mut(&p)
			err := p.Validate()
			if !isInvalid(err) || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want InvalidArgument mentioning %q", err, tc.want)
			}
		})
	}
	if err := DefaultAnomalyParams().Validate(); err != nil {
		t.Errorf("the defaults must validate: %v", err)
	}
}

func TestAnomalyFormattingAndNextCommands(t *testing.T) {
	for _, tc := range []struct {
		unit string
		v    float64
		want string
	}{
		{"fraction", 0.0256, "2.56%"},
		{"s", 0.1804, "180 ms"},
		{"requests", 4000.4, "4000 requests"},
	} {
		if got := fmtAnomaly(tc.unit, tc.v); got != tc.want {
			t.Errorf("fmtAnomaly(%q, %v) = %q, want %q", tc.unit, tc.v, got, tc.want)
		}
	}
	p := DefaultAnomalyParams()
	for signal, want := range map[string]string{
		AnomalySignalP95:        "--agg p95",
		AnomalySignalErrorRate:  "--agg increase",
		AnomalySignalThroughput: "--agg rate",
	} {
		next := anomalyNext("prod", Anomaly{Service: "api", Signal: signal}, p)
		if len(next) < 3 || !strings.Contains(strings.Join(next, "\n"), want) || next[len(next)-1] != "forge obs check prod" {
			t.Errorf("%s: next = %v, want the commands to the evidence, one with %q", signal, next, want)
		}
		for _, c := range next {
			if !strings.HasPrefix(c, "forge obs ") {
				t.Errorf("%s: %q is not a forge command", signal, c)
			}
		}
	}
}

func TestAnomalyWhyForEachSignalCarriesItsThresholds(t *testing.T) {
	q := AnomalyQuery{Scope: Scope{Database: "t_org"}, Params: DefaultAnomalyParams(), Now: anomalyNow}
	for _, tc := range []struct {
		a    Anomaly
		keys []string
		dir  string
	}{
		{Anomaly{Service: "api", Signal: AnomalySignalP95, Unit: "s", Observed: 1.8, Baseline: 0.18, Up: true}, []string{"p95_ratio", "min_latency_delta_seconds"}, "above"},
		{Anomaly{Service: "api", Signal: AnomalySignalThroughput, Unit: "requests", Observed: 10, Baseline: 400, Up: false}, []string{"throughput_ratio"}, "below"},
	} {
		w := tc.a.Why("prod", q)
		for _, k := range append(tc.keys, "k", "min_requests") {
			if _, ok := w.Thresholds[k]; !ok {
				t.Errorf("%s: thresholds %v lack %q", tc.a.Signal, w.Thresholds, k)
			}
		}
		if _, ok := w.Thresholds["min_errors"]; ok && tc.a.Signal != AnomalySignalErrorRate {
			t.Errorf("%s: carries the error-rate floors", tc.a.Signal)
		}
		if got := w.Context.Findings[0].Message; !strings.Contains(got, tc.dir+" its usual") {
			t.Errorf("%s: message %q does not say %q", tc.a.Signal, got, tc.dir)
		}
	}
}
