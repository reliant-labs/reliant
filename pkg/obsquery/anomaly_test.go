package obsquery

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/alerting"
)

var anomalyNow = time.Date(2026, 10, 26, 9, 17, 0, 0, time.UTC) // a Monday

// baselineOf is 16 slots (4 weeks of 4 slots) of one value, with a little
// natural jitter in the error count so the spread is not exactly zero.
func baselineOf(requests, errors float64, p95 float64) []REDSample {
	out := make([]REDSample, 16)
	for i := range out {
		out[i] = REDSample{Requests: requests, Errors: errors + float64(i%3), P95: p95, HasP95: p95 > 0}
	}
	return out
}

func judge(t *testing.T, in ServiceREDInput) []Anomaly {
	t.Helper()
	in.Service, in.Now = "api", anomalyNow
	if in.FirstSeen.IsZero() {
		in.FirstSeen = anomalyNow.Add(-28 * 24 * time.Hour)
	}
	return JudgeService(in, DefaultAnomalyParams())
}

func signals(as []Anomaly) string {
	var s []string
	for _, a := range as {
		s = append(s, a.Signal)
	}
	return strings.Join(s, ",")
}

func TestJudgeServiceSeasonalPeakDoesNotFire(t *testing.T) {
	// Monday 09:15 is the weekly peak: 4000 requests per 15 minutes, against a
	// service that serves a tenth of that off-peak. The baseline is the same
	// hour of the previous weeks, so a peak observation sits inside it. The
	// error rate of 0.5% and the p95 of 120 ms are the usual ones for the hour.
	in := ServiceREDInput{
		Observed: REDSample{Requests: 4100, Errors: 21, P95: 0.125, HasP95: true},
		Baseline: baselineOf(4000, 19, 0.12),
	}
	if got := judge(t, in); len(got) != 0 {
		t.Fatalf("a normal Monday peak fired %+v", got)
	}

	// The same observation against an off-peak baseline IS a surge: this is
	// what a flat comparison would have to tell apart.
	in.Baseline = baselineOf(400, 2, 0.12)
	got := judge(t, in)
	if signals(got) != AnomalySignalThroughput || !got[0].Up {
		t.Fatalf("a tenfold surge over the hour's usual fired %q, want throughput up", signals(got))
	}
}

func TestJudgeServiceErrorRateJump(t *testing.T) {
	// 5x the usual error rate: 0.5% -> 2.5%, over 4000 requests.
	in := ServiceREDInput{
		Observed: REDSample{Requests: 4000, Errors: 100, P95: 0.12, HasP95: true},
		Baseline: baselineOf(4000, 20, 0.12),
	}
	got := judge(t, in)
	if signals(got) != AnomalySignalErrorRate {
		t.Fatalf("fired %q, want error_rate only", signals(got))
	}
	a := got[0]
	if !a.Up || a.Observed != 0.025 || a.Baseline < 0.0049 || a.Baseline > 0.0061 || a.Spread <= 0 || a.Limit <= a.Baseline || a.Limit >= a.Observed {
		t.Errorf("finding = %+v", a)
	}
	if a.HistoryDays != 28 || a.BaselineSamples != 16 || a.Requests != 4000 {
		t.Errorf("history/samples/requests = %d/%d/%v", a.HistoryDays, a.BaselineSamples, a.Requests)
	}
}

func TestJudgeServiceFloors(t *testing.T) {
	cases := []struct {
		name string
		in   ServiceREDInput
		want string
	}{
		{"a rate over the delta on 4 errors is under the error floor",
			ServiceREDInput{Observed: REDSample{Requests: 100, Errors: 4}, Baseline: baselineOfFlat(100, 0)}, ""},
		{"error rate under the absolute delta: 0.5% -> 1.2% is a 5x of nothing",
			ServiceREDInput{Observed: REDSample{Requests: 5000, Errors: 60}, Baseline: baselineOf(5000, 25, 0)}, ""},
		{"a rate jump on too few requests",
			ServiceREDInput{Observed: REDSample{Requests: 30, Errors: 15}, Baseline: baselineOf(30, 0, 0)}, ""},
		{"a spread of zero does not make one failure an anomaly",
			ServiceREDInput{Observed: REDSample{Requests: 60, Errors: 1}, Baseline: baselineOfFlat(60, 0)}, ""},
		{"p95 up 1.4x is under the ratio floor",
			ServiceREDInput{Observed: REDSample{Requests: 500, P95: 0.28, HasP95: true}, Baseline: baselineOf(500, 0, 0.2)}, ""},
		{"p95 up 3x but only 6 ms is under the delta floor",
			ServiceREDInput{Observed: REDSample{Requests: 500, P95: 0.009, HasP95: true}, Baseline: baselineOf(500, 0, 0.003)}, ""},
		{"p95 up 3x and 400 ms fires",
			ServiceREDInput{Observed: REDSample{Requests: 500, P95: 0.6, HasP95: true}, Baseline: baselineOf(500, 0, 0.2)}, AnomalySignalP95},
		{"throughput 1.5x is under the ratio",
			ServiceREDInput{Observed: REDSample{Requests: 600}, Baseline: baselineOf(400, 0, 0)}, ""},
		{"traffic gone quiet fires throughput down",
			ServiceREDInput{Observed: REDSample{Requests: 12}, Baseline: baselineOf(400, 0, 0)}, AnomalySignalThroughput},
		{"a service that is always quiet never fires a drop",
			ServiceREDInput{Observed: REDSample{Requests: 0}, Baseline: baselineOf(8, 0, 0)}, ""},
		{"too few baseline slots",
			ServiceREDInput{Observed: REDSample{Requests: 4000, Errors: 400}, Baseline: baselineOf(4000, 20, 0)[:5]}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := signals(judge(t, c.in)); got != c.want {
				t.Errorf("fired %q, want %q", got, c.want)
			}
		})
	}
}

func baselineOfFlat(requests, errors float64) []REDSample {
	out := make([]REDSample, 16)
	for i := range out {
		out[i] = REDSample{Requests: requests, Errors: errors}
	}
	return out
}

func TestJudgeServiceNeedsSevenDaysOfHistory(t *testing.T) {
	in := ServiceREDInput{
		Observed: REDSample{Requests: 4000, Errors: 400},
		Baseline: baselineOf(4000, 20, 0),
	}
	for _, c := range []struct {
		age  time.Duration
		want bool
	}{
		{0, false}, {3 * 24 * time.Hour, false}, {7*24*time.Hour - time.Minute, false},
		{7 * 24 * time.Hour, true}, {28 * 24 * time.Hour, true},
	} {
		in.FirstSeen, in.Service, in.Now = anomalyNow.Add(-c.age), "api", anomalyNow
		if c.age == 0 {
			in.FirstSeen = time.Time{} // never seen: no metrics to judge
		}
		if fired := len(JudgeService(in, DefaultAnomalyParams())) > 0; fired != c.want {
			t.Errorf("history %s: fired = %v, want %v", c.age, fired, c.want)
		}
	}
}

func TestMedianMAD(t *testing.T) {
	med, mad := medianMAD([]float64{1, 1, 2, 2, 4, 6, 9})
	if med != 2 || mad != 1 {
		t.Errorf("median/MAD = %v/%v, want 2/1", med, mad)
	}
	if med, mad := medianMAD([]float64{3, 5}); med != 4 || mad != 1 {
		t.Errorf("even-length median/MAD = %v/%v, want 4/1", med, mad)
	}
	// One terrible week must not move the baseline: that is why it is a median.
	if med, _ := medianMAD([]float64{0.01, 0.01, 0.01, 0.01, 0.01, 0.01, 0.9, 0.9}); med != 0.01 {
		t.Errorf("median with an outlier week = %v, want 0.01", med)
	}
}

func TestAnomalyWindowsAlignToTheSameHourOfTheWeek(t *testing.T) {
	q := AnomalyQuery{Scope: Scope{Database: "t_org"}, Params: DefaultAnomalyParams(), Now: anomalyNow}
	w := q.windows(anomalyNow)
	if got, want := w.observed.End, anomalyNow.Add(-2*time.Minute); !got.Equal(want) {
		t.Errorf("observed end = %s, want %s (now less the export delay)", got, want)
	}
	if d := w.observed.End.Sub(w.observed.Start); d != 15*time.Minute {
		t.Errorf("observed window = %s", d)
	}
	if len(w.baselines) != 4 {
		t.Fatalf("%d baseline windows, want 4 weeks", len(w.baselines))
	}
	hour := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	for i, sc := range w.baselines {
		shift := time.Duration(i+1) * 7 * 24 * time.Hour
		if !sc.Start.Equal(hour.Add(-shift)) || !sc.End.Equal(hour.Add(time.Hour-shift)) {
			t.Errorf("week %d: %s - %s, want the 09:00-10:00 hour shifted %s", i+1, sc.Start, sc.End, shift)
		}
		if sc.Start.Weekday() != time.Monday {
			t.Errorf("week %d starts on a %s", i+1, sc.Start.Weekday())
		}
	}
	if !w.history.Start.Equal(w.baselines[3].Start) || !w.history.End.Equal(anomalyNow) {
		t.Errorf("history = %s - %s", w.history.Start, w.history.End)
	}

	// A window that crosses the hour reads both hours.
	w = q.windows(time.Date(2026, 10, 26, 9, 10, 0, 0, time.UTC))
	if got := w.baselines[0].End.Sub(w.baselines[0].Start); got != 2*time.Hour {
		t.Errorf("a window across the hour reads %s of baseline, want 2h", got)
	}
	// Four weeks of history fit the metrics range.
	if err := w.history.validate(MaxRangeMetrics); err != nil {
		t.Errorf("history range refused: %v", err)
	}
}

func TestAnomalyParamsValidate(t *testing.T) {
	if err := DefaultAnomalyParams().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*AnomalyParams){
		"window not dividing the hour": func(p *AnomalyParams) { p.Window = 7 * time.Minute },
		"sub-minute window":            func(p *AnomalyParams) { p.Window = 30 * time.Second },
		"five weeks":                   func(p *AnomalyParams) { p.Weeks = 5 },
		"no weeks":                     func(p *AnomalyParams) { p.Weeks = 0 },
		"zero k":                       func(p *AnomalyParams) { p.K = 0 },
		"ratio under one":              func(p *AnomalyParams) { p.P95Ratio = 0.5 },
		"no baseline samples":          func(p *AnomalyParams) { p.MinBaselineSamples = 0 },
	} {
		p := DefaultAnomalyParams()
		mut(&p)
		if err := p.Validate(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestAnomalyFirstSeenStatement(t *testing.T) {
	q := AnomalyQuery{Scope: Scope{Database: "t_org", EnvRestricted: true, EnvIDs: []string{"env-1"}}, Params: DefaultAnomalyParams(), Now: anomalyNow}
	st, err := q.FirstSeenStmt()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"min(TimeUnix) AS first", "t_org.otel_metrics_sum", "MetricName =", "ResourceAttributes['reliant.env.id']", "GROUP BY service"} {
		if !strings.Contains(st.SQL, want) {
			t.Errorf("first-seen SQL lacks %q:\n%s", want, st.SQL)
		}
	}
	vals := map[string]bool{}
	for _, v := range st.Params {
		vals[v] = true
	}
	if !vals["rpc.server.calls"] || !vals["env-1"] {
		t.Errorf("params = %v: the metric name and env must be bound, never inlined", st.Params)
	}
	if strings.Contains(st.SQL, "rpc.server.calls") || strings.Contains(st.SQL, "env-1") {
		t.Errorf("the statement inlines a value:\n%s", st.SQL)
	}
	bad := q
	bad.Scope.Database = "default"
	if _, err := bad.FirstSeenStmt(); err == nil {
		t.Error("the server's own database was accepted on a non-local scope")
	}
}

func TestAnomalyWhyCarriesTheSharedPayload(t *testing.T) {
	q := AnomalyQuery{Scope: Scope{Database: "t_org"}, Params: DefaultAnomalyParams(), Now: anomalyNow}
	in := ServiceREDInput{
		Observed: REDSample{Requests: 4000, Errors: 100}, Baseline: baselineOf(4000, 20, 0),
		Service: "api", Now: anomalyNow, FirstSeen: anomalyNow.Add(-28 * 24 * time.Hour),
	}
	got := JudgeService(in, q.Params)
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	w := got[0].Why("prod", q)
	if w.Detector != alerting.DetectorServiceAnomaly || w.Signal != AnomalySignalErrorRate || w.DedupeKey != "service_anomaly/api/-" {
		t.Errorf("identity = %q %q %q", w.Detector, w.Signal, w.DedupeKey)
	}
	if w.Observed == nil || *w.Observed != 0.025 || w.Baseline == nil || w.Spread == nil || w.Unit != "fraction" {
		t.Errorf("figures = %v %v %v %q", w.Observed, w.Baseline, w.Spread, w.Unit)
	}
	if w.Window != "15m0s" || w.HistoryDays != 28 || w.Since.IsZero() || w.Method == "" || w.BaselineWindow == "" {
		t.Errorf("window = %q since %v history %d method %q baseline %q", w.Window, w.Since, w.HistoryDays, w.Method, w.BaselineWindow)
	}
	if w.Thresholds["k"] != 4 || w.Thresholds["min_requests"] != 50 || w.Thresholds["min_errors"] != 5 || w.Thresholds["min_error_delta"] != 0.01 {
		t.Errorf("thresholds = %v", w.Thresholds)
	}
	if len(w.Context.Findings) != 1 || w.Context.Findings[0].Service != "api" || w.Context.Findings[0].Limit <= 0 || w.Context.Findings[0].Before == nil {
		t.Errorf("findings = %+v", w.Context.Findings)
	}
	if len(w.Next) < 3 || !strings.HasPrefix(w.Next[0], "forge obs traces prod --service api --errors") {
		t.Errorf("next = %v", w.Next)
	}
	for _, n := range w.Next {
		if !strings.HasPrefix(n, "forge obs ") || strings.Contains(n, "  ") {
			t.Errorf("next command %q is not runnable as written", n)
		}
	}
	raw, err := w.JSON()
	if err != nil {
		t.Fatal(err)
	}
	back, err := alerting.ParseWhy(raw)
	if err != nil || back.Detector != w.Detector || *back.Observed != *w.Observed {
		t.Errorf("round trip = %+v, %v", back, err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil || generic["detector"] != "service_anomaly" {
		t.Errorf("payload = %s", raw)
	}
}

func TestAnomalyWhyNextPerSignal(t *testing.T) {
	q := AnomalyQuery{Scope: Scope{Database: "t_org"}, Params: DefaultAnomalyParams(), Now: anomalyNow}
	for signal, want := range map[string]string{
		AnomalySignalP95:        "--slow",
		AnomalySignalThroughput: "rpc.server.calls",
	} {
		w := Anomaly{Service: "api", Signal: signal, Unit: "s", Up: true}.Why("prod", q)
		if !strings.Contains(strings.Join(w.Next, "\n"), want) {
			t.Errorf("%s next = %v, want one with %q", signal, w.Next, want)
		}
	}
}
