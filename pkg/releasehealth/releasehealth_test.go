package releasehealth

import (
	"errors"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

var t0 = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

func tp(t time.Time) *time.Time { return &t }

func defaults(t *testing.T) Thresholds {
	t.Helper()
	th, err := Resolve(Options{})
	if err != nil {
		t.Fatal(err)
	}
	return th
}

func svc(name string, beforeReq, afterReq int64, beforeErr, afterErr, beforeP95, afterP95 float64) ServiceHealth {
	return ServiceHealth{Service: name,
		Before: WindowStats{Requests: beforeReq, ErrorRate: beforeErr, P95Ms: beforeP95},
		After:  WindowStats{Requests: afterReq, ErrorRate: afterErr, P95Ms: afterP95}}
}

func input(t *testing.T, complete bool, services ...ServiceHealth) Input {
	return Input{
		Subject:  Subject{Release: "v2", PromotedAt: t0},
		Windows:  Windows{Before: Range{t0.Add(-15 * time.Minute), t0}, After: Range{t0, t0.Add(15 * time.Minute)}, AfterComplete: complete},
		Services: services, Thresholds: defaults(t),
	}
}

func TestVerdicts(t *testing.T) {
	tests := []struct {
		name     string
		in       Input
		verdict  Verdict
		findings []string
	}{
		{"error-rate jump fails", input(t, true, svc("api", 1000, 1000, 0.001, 0.05, 100, 100)), Fail, []string{release.HealthErrorRate}},
		{"equal traffic passes", input(t, true, svc("api", 1000, 1000, 0.01, 0.01, 100, 100)), Pass, nil},
		{"under min_requests is inconclusive", input(t, true, svc("api", 1000, 49, 0.0, 0.5, 100, 900)), Inconclusive, nil},
		{"window still open with no failure is inconclusive", input(t, false, svc("api", 1000, 1000, 0.01, 0.01, 100, 100)), Inconclusive, nil},
		{"a clear failure fails while the window is open", input(t, false, svc("api", 1000, 1000, 0, 0.2, 100, 100)), Fail, []string{release.HealthErrorRate}},
		{"latency alone only warns by default", input(t, true, svc("api", 1000, 1000, 0, 0, 100, 400)), Pass, []string{release.HealthLatency}},
		{"increase exactly at the limit does not fire", input(t, true, svc("api", 1000, 1000, 0.01, 0.02, 100, 100)), Pass, nil},
		{"no before traffic: latency is not judged", input(t, true, svc("api", 0, 1000, 0, 0, 0, 900)), Pass, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := Evaluate(tc.in)
			if r.Verdict != tc.verdict {
				t.Errorf("verdict = %s, want %s (%+v)", r.Verdict, tc.verdict, r.Findings)
			}
			var got []string
			for _, f := range r.Findings {
				got = append(got, f.Code)
			}
			if len(got) != len(tc.findings) {
				t.Fatalf("findings = %v, want %v", got, tc.findings)
			}
			for i := range got {
				if got[i] != tc.findings[i] {
					t.Errorf("findings = %v, want %v", got, tc.findings)
				}
			}
		})
	}
}

func TestLatencyFindingWarnsByDefaultAndFailsWhenAsked(t *testing.T) {
	in := input(t, true, svc("api", 1000, 1000, 0, 0, 100, 400))
	r := Evaluate(in)
	if len(r.Findings) != 1 || r.Findings[0].Policy != PolicyWarn {
		t.Fatalf("%+v", r.Findings)
	}
	j := Rejudge(r, []string{release.HealthLatency})
	if j.Verdict != Fail || j.Findings[0].Policy != PolicyFail {
		t.Errorf("rejudge: %s %+v", j.Verdict, j.Findings)
	}
	if got := fromList(j.Thresholds)[ThresholdFailOn]; got.Source != SourceRequest || got.Value != "latency" {
		t.Errorf("fail_on = %+v", got)
	}
	// Rejudge never mutates its input.
	if r.Findings[0].Policy != PolicyWarn {
		t.Error("Rejudge changed the original report")
	}
}

func TestRejudgeKeepsMinRequestsInconclusive(t *testing.T) {
	r := Evaluate(input(t, true, svc("api", 1000, 10, 0, 0, 100, 100)))
	if r.Verdict != Inconclusive {
		t.Fatalf("verdict = %s", r.Verdict)
	}
	if j := Rejudge(r, nil); j.Verdict != Inconclusive {
		t.Errorf("rejudge verdict = %s", j.Verdict)
	}
}

func TestIssueAlertAndWorkloadFindings(t *testing.T) {
	in := input(t, true, svc("api", 1000, 1000, 0, 0, 100, 100))
	in.NewIssues = []Issue{
		{ID: "ISS-1", Service: "api", Title: "boom", Events: 5, FirstSeenAt: t0.Add(time.Minute), FirstVersion: "v2"},
		{ID: "ISS-2", Events: 2, FirstSeenAt: t0.Add(time.Minute), FirstVersion: "v2"}, // below new_issue_min_events
		{ID: "ISS-3", Events: 9, FirstSeenAt: t0.Add(-time.Hour), FirstVersion: "v2"},  // first seen before P
		{ID: "ISS-4", Events: 9, FirstSeenAt: t0.Add(time.Minute), FirstVersion: "v1"}, // another release's
		{ID: "ISS-5", Events: 3, FirstSeenAt: t0.Add(time.Minute), FirstVersion: "v2"}, // exactly the minimum
	}
	in.RegressedIssues = []Issue{{ID: "ISS-9", Title: "old bug", Events: 4, RegressedAt: t0.Add(time.Minute)}, {ID: "ISS-8", RegressedAt: t0.Add(-time.Hour)}}
	in.Alerts = []Alert{{Rule: "HighLatency", FiringAt: t0.Add(2 * time.Minute)}, {Rule: "Old", FiringAt: t0.Add(-time.Minute)}}
	in.Workloads = []Workload{{Workload: "api", Restarts: 2, LastTerminatedReason: "OOMKilled"}, {Workload: "web"}}
	r := Evaluate(in)

	var codes []string
	for _, f := range r.Findings {
		codes = append(codes, f.Code+":"+f.Ref+":"+f.Policy)
	}
	want := []string{
		"new_issue:ISS-5:fail", "new_issue:ISS-1:fail", "regressed_issue:ISS-9:fail",
		"alert_firing:HighLatency:fail", "workload_restarts:api:warn",
	}
	if len(codes) != len(want) {
		t.Fatalf("findings = %v, want %v", codes, want)
	}
	for i := range want {
		if codes[i] != want[i] {
			t.Errorf("findings = %v, want %v", codes, want)
			break
		}
	}
	if r.Verdict != Fail {
		t.Errorf("verdict = %s", r.Verdict)
	}
}

func TestWindowsAreBoundedByNeighboursAndNow(t *testing.T) {
	w := 15 * time.Minute
	tests := []struct {
		name       string
		prev, next *time.Time
		now        time.Time
		before     Range
		after      Range
		afterDone  bool
	}{
		{"T+5m with w=15m is not complete", nil, nil, t0.Add(5 * time.Minute),
			Range{t0.Add(-w), t0}, Range{t0, t0.Add(5 * time.Minute)}, false},
		{"exactly P+w is complete", nil, nil, t0.Add(w),
			Range{t0.Add(-w), t0}, Range{t0, t0.Add(w)}, true},
		{"long after: the window is w, not now", nil, nil, t0.Add(3 * time.Hour),
			Range{t0.Add(-w), t0}, Range{t0, t0.Add(w)}, true},
		{"the previous promotion cuts the before window", tp(t0.Add(-4 * time.Minute)), nil, t0.Add(time.Hour),
			Range{t0.Add(-4 * time.Minute), t0}, Range{t0, t0.Add(w)}, true},
		{"a previous promotion outside w changes nothing", tp(t0.Add(-time.Hour)), nil, t0.Add(time.Hour),
			Range{t0.Add(-w), t0}, Range{t0, t0.Add(w)}, true},
		{"the next promotion cuts the after window and completes it", nil, tp(t0.Add(6 * time.Minute)), t0.Add(7 * time.Minute),
			Range{t0.Add(-w), t0}, Range{t0, t0.Add(6 * time.Minute)}, true},
		{"a next promotion in the future of now: still bounded by now", nil, tp(t0.Add(10 * time.Minute)), t0.Add(2 * time.Minute),
			Range{t0.Add(-w), t0}, Range{t0, t0.Add(2 * time.Minute)}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Plan(t0, w, tc.prev, tc.next, tc.now)
			if got.Before != tc.before || got.After != tc.after || got.AfterComplete != tc.afterDone {
				t.Errorf("Plan = %+v\nwant before %v after %v complete %v", got, tc.before, tc.after, tc.afterDone)
			}
		})
	}
}

func fromList(l []Threshold) map[string]Threshold {
	m := map[string]Threshold{}
	for _, th := range l {
		m[th.Name] = th
	}
	return m
}

func TestThresholdSources(t *testing.T) {
	def := fromList(defaults(t).List())
	for n, th := range def {
		if th.Source != SourceDefault {
			t.Errorf("%s source = %s, want default", n, th.Source)
		}
	}

	shape := release.DefaultReleaseHealth()
	shape.MinRequests = 200
	shape.WindowSeconds = 1800
	shape.FailOn = []string{release.HealthLatency}
	th, err := Resolve(Options{Shape: &shape})
	if err != nil {
		t.Fatal(err)
	}
	l := fromList(th.List())
	if l[ThresholdMinRequests].Source != SourceReleaseShape || l[ThresholdMinRequests].Value != "200" || l[ThresholdWindow].Value != "30m" {
		t.Errorf("shape thresholds: %+v", l)
	}

	th, err = Resolve(Options{Shape: &shape, Window: time.Hour, FailOn: []string{release.HealthErrorRate, release.HealthErrorRate},
		Overrides: map[string]string{ThresholdMaxErrorRateIncrease: "0.05"}})
	if err != nil {
		t.Fatal(err)
	}
	l = fromList(th.List())
	check := func(name, value, source string) {
		t.Helper()
		if l[name].Value != value || l[name].Source != source {
			t.Errorf("%s = %+v, want %s from %s", name, l[name], value, source)
		}
	}
	check(ThresholdWindow, "1h", SourceRequest)
	check(ThresholdFailOn, "error_rate", SourceRequest)
	check(ThresholdMaxErrorRateIncrease, "0.05", SourceRequest)
	check(ThresholdMinRequests, "200", SourceReleaseShape)
	check(ThresholdMaxP95Ratio, "1.5", SourceReleaseShape)

	// A shape equal to the defaults is the defaults.
	same := release.DefaultReleaseHealth()
	th, _ = Resolve(Options{Shape: &same})
	if fromList(th.List())[ThresholdMinRequests].Source != SourceDefault {
		t.Error("a default-valued shape is reported as the default")
	}
}

func TestResolveRefusesBadInput(t *testing.T) {
	for name, o := range map[string]Options{
		"unknown threshold": {Overrides: map[string]string{"nope": "1"}},
		"not a number":      {Overrides: map[string]string{ThresholdMinRequests: "many"}},
		"ratio below one":   {Overrides: map[string]string{ThresholdMaxP95Ratio: "0.9"}},
		"window too long":   {Window: 7 * time.Hour},
		"window too short":  {Window: time.Second},
		"unknown fail-on":   {FailOn: []string{"vibes"}},
	} {
		if _, err := Resolve(o); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if _, err := ParseOverrides([]string{"max_p95_ratio"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("malformed pair: %v", err)
	}
	if _, err := ParseOverrides([]string{"window=1h"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("window is --window: %v", err)
	}
	if m, err := ParseOverrides([]string{"min_requests=10"}); err != nil || m[ThresholdMinRequests] != "10" {
		t.Errorf("%v %v", m, err)
	}
	if got, err := ParseFailOn("new_issue, error_rate,new_issue"); err != nil || len(got) != 2 {
		t.Errorf("ParseFailOn = %v %v", got, err)
	}
	if _, err := ParseFailOn("new_issue,bogus"); !errors.Is(err, ErrInvalid) {
		t.Errorf("bogus: %v", err)
	}
}

func TestInconclusiveWhy(t *testing.T) {
	open := Evaluate(input(t, false, svc("api", 1000, 1000, 0, 0, 100, 100)))
	if InconclusiveWhy(open) == "" || open.Verdict != Inconclusive {
		t.Errorf("open: %s %q", open.Verdict, InconclusiveWhy(open))
	}
	thin := Evaluate(input(t, true, svc("api", 1000, 3, 0, 0, 100, 100)))
	if got := InconclusiveWhy(thin); got == "" || thin.Verdict != Inconclusive {
		t.Errorf("thin: %s %q", thin.Verdict, got)
	}
}
