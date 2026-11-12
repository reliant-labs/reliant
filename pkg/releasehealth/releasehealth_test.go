package releasehealth

import (
	"errors"
	"strings"
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
		{"no before traffic: nothing to compare, so inconclusive", input(t, true, svc("api", 0, 1000, 0, 0, 0, 900)), Inconclusive, nil},
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

func ep(name string, beforeReq, afterReq int64, beforeP95, afterP95 float64) EndpointHealth {
	return EndpointHealth{Endpoint: name,
		Before: WindowStats{Requests: beforeReq, P95Ms: beforeP95},
		After:  WindowStats{Requests: afterReq, P95Ms: afterP95}}
}

// forge#1728: the deploy's own GetRelease polling filled admin-server's before
// window with fast calls and then stopped. The service p95 tripled; no endpoint
// got slower. Per-endpoint figures judge the endpoints, not the mix.
func TestLatencyIsJudgedPerEndpointNotByTrafficMix(t *testing.T) {
	mix := svc("admin-server", 1400, 300, 0, 0, 40, 120)
	mix.Endpoints = []EndpointHealth{
		ep("/deploy.v1.DeployService/GetRelease", 1164, 3, 30, 31),
		ep("/deploy.v1.DeployService/GetStatus", 120, 140, 334, 363),
		ep("/token.v1.AccessTokenInternalService/Introspect", 100, 150, 30, 36),
		ep("/token.v1.GitCredentialInternalService/GetUserAccessToken", 16, 7, 12, 14),
		ep("/new.v1.New/OnlyAfter", 0, 50, 0, 30), // no before: judged against the service's 40ms
	}
	if r := Evaluate(input(t, true, mix)); len(r.Findings) != 0 {
		t.Errorf("a traffic-mix shift is judged a latency regression: %+v", r.Findings)
	}
	if ratio, ok := mix.LatencyRatio(); !ok || ratio > 1.2 {
		t.Errorf("LatencyRatio = %v, %v; want the endpoints' ~1.1x", ratio, ok)
	}

	slow := svc("admin-server", 300, 300, 0, 0, 100, 110)
	slow.Endpoints = []EndpointHealth{
		ep("/a.v1.A/Hot", 200, 200, 100, 400),
		ep("/a.v1.A/Cold", 100, 100, 100, 100),
	}
	r := Evaluate(input(t, true, slow))
	if len(r.Findings) != 1 || r.Findings[0].Code != release.HealthLatency || r.Findings[0].Value != 3 {
		t.Fatalf("a busy endpoint 4x slower must be a latency finding at 3x (weighted): %+v", r.Findings)
	}
	if !strings.Contains(r.Findings[0].Message, "per endpoint") {
		t.Errorf("message does not say the ratio is per endpoint: %s", r.Findings[0].Message)
	}

	none := svc("api", 0, 100, 0, 0, 0, 900)
	none.Endpoints = []EndpointHealth{ep("/a.v1.A/New", 0, 100, 0, 900)}
	if _, ok := none.LatencyRatio(); ok {
		t.Error("a service with no before traffic at all still produced a latency ratio")
	}
}

// An endpoint with no before p95 has no baseline of its own. Unknown is not
// safe: it is judged against the service-level before p95, so a new, very slow
// route beside stable ones is a latency finding, and fails when asked.
func TestAfterOnlyEndpointIsJudgedAgainstServiceBaseline(t *testing.T) {
	s := svc("api", 1000, 1100, 0, 0, 100, 900)
	s.Endpoints = []EndpointHealth{
		ep("/a.v1.A/Stable", 1000, 1000, 100, 100),
		ep("/a.v1.A/New", 0, 100, 0, 20000),
	}
	ratio, ok := s.LatencyRatio()
	if !ok || ratio <= defaults(t).MaxP95Ratio {
		t.Fatalf("LatencyRatio = %v, %v; a 200x after-only endpoint must exceed the limit", ratio, ok)
	}
	r := Evaluate(input(t, true, s))
	if len(r.Findings) != 1 || r.Findings[0].Code != release.HealthLatency {
		t.Fatalf("a very slow after-only endpoint produced no latency finding: %+v", r.Findings)
	}
	if j := Rejudge(r, []string{release.HealthLatency}); j.Verdict == Pass {
		t.Errorf("a very slow after-only endpoint passes with fail_on=latency: %+v", j.Findings)
	}
}

// A route whose after p95 rounds to zero is no measurement. However busy it is,
// it must not dilute a slow after-only route out of the verdict.
func TestZeroP95EndpointDoesNotDiluteSlowAfterOnlyEndpoint(t *testing.T) {
	s := svc("api", 1000, 100100, 0, 0, 100, 100)
	s.Endpoints = []EndpointHealth{
		ep("/a.v1.A/Fast", 1000, 100000, 0, 0),
		ep("/a.v1.A/New", 0, 100, 0, 20000),
	}
	ratio, ok := s.LatencyRatio()
	if !ok || ratio <= defaults(t).MaxP95Ratio {
		t.Fatalf("LatencyRatio = %v, %v; a busy zero-p95 route diluted a 200x after-only endpoint", ratio, ok)
	}
	r := Evaluate(input(t, true, s))
	if len(r.Findings) != 1 || r.Findings[0].Code != release.HealthLatency {
		t.Fatalf("a slow after-only endpoint beside a zero-p95 route produced no latency finding: %+v", r.Findings)
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
	if got := InconclusiveCode(thin); got != ReasonLowTraffic {
		t.Errorf("thin code = %q, want %q", got, ReasonLowTraffic)
	}
	if got := InconclusiveCode(open); got != ReasonWindowOpen {
		t.Errorf("open code = %q, want %q", got, ReasonWindowOpen)
	}
}

// An app that exports no OpenTelemetry has no request data at all, however
// much traffic it serves. That is not "not enough traffic": measured on an
// adopted Express app, 120 real requests got exactly that reason. The reason
// says what is missing, names the services whose telemetry is their own, and
// what was judged without it.
func TestInconclusiveWhyNoRequestTelemetry(t *testing.T) {
	for _, services := range [][]ServiceHealth{nil, {svc("app", 0, 0, 0, 0, 0, 0)}} {
		r := WithUninstrumented(Evaluate(input(t, true, services...)), []string{"app"})
		if r.Verdict != Inconclusive || !r.NoRequestTelemetry() {
			t.Fatalf("verdict %s, no telemetry %v; want INCONCLUSIVE with no request telemetry", r.Verdict, r.NoRequestTelemetry())
		}
		if got := InconclusiveCode(r); got != ReasonNoRequestTelemetry {
			t.Errorf("code = %q, want %q", got, ReasonNoRequestTelemetry)
		}
		why := InconclusiveWhy(r)
		for _, want := range []string{"no request telemetry", "app exports none unless it is instrumented", "OTEL_EXPORTER_OTLP_ENDPOINT", "workload restarts, alerts and issues", "found nothing"} {
			if !strings.Contains(why, want) {
				t.Errorf("why does not say %q:\n%s", want, why)
			}
		}
		if strings.Contains(why, "not enough traffic") {
			t.Errorf("why blames traffic for missing telemetry:\n%s", why)
		}
	}
	// Some request data, however little, is low traffic.
	if r := Evaluate(input(t, true, svc("app", 0, 3, 0, 0, 10, 10))); r.NoRequestTelemetry() || InconclusiveCode(r) != ReasonLowTraffic {
		t.Errorf("3 requests: no telemetry %v, code %q; want low_traffic", r.NoRequestTelemetry(), InconclusiveCode(r))
	}
	// A restart is still a finding, and the reason points at it.
	in := input(t, true)
	in.Workloads = []Workload{{Workload: "app", Restarts: 2}}
	in.Thresholds.FailOn = nil
	if why := InconclusiveWhy(Evaluate(in)); !strings.Contains(why, "1 warning(s)") {
		t.Errorf("why with a warn finding = %q", why)
	}
}

// An env with no request telemetry at all (a static site, a workload that
// exports no spans) is not "low traffic": the reason says what is missing.
func TestInconclusiveWhyNamesMissingRequestTelemetry(t *testing.T) {
	none := Evaluate(input(t, true))
	if why := InconclusiveWhy(none); none.Verdict != Inconclusive || !strings.Contains(why, "no request telemetry") {
		t.Errorf("no services: %s %q", none.Verdict, why)
	}
	thin := Evaluate(input(t, true, svc("api", 10, 3, 0, 0, 100, 100)))
	if why := InconclusiveWhy(thin); !strings.Contains(why, "not enough traffic") {
		t.Errorf("thin traffic keeps its reason: %q", why)
	}
}

// While the after window is open, After.To is the moment it was read; the
// window ends at the promotion plus the judged window (forge_cli_report
// 9fe4605f: `--wait` reported an end that moved forward with every poll).
func TestAfterEndsIsTheWindowsEndNotTheReadTime(t *testing.T) {
	in := input(t, false, svc("api", 1000, 1000, 0, 0, 100, 100))
	in.Windows = Plan(t0, 15*time.Minute, nil, nil, t0.Add(4*time.Minute))
	open := Evaluate(in)
	if got, want := open.AfterEnds(), t0.Add(15*time.Minute); !got.Equal(want) {
		t.Errorf("open AfterEnds = %s, want %s (After.To = %s)", got, want, open.After.To)
	}
	if why := InconclusiveWhy(open); !strings.Contains(why, t0.Add(15*time.Minute).Format(time.RFC3339)) {
		t.Errorf("why names the window's end: %q", why)
	}
	in.Windows = Plan(t0, 15*time.Minute, nil, tp(t0.Add(5*time.Minute)), t0.Add(20*time.Minute))
	if got := Evaluate(in).AfterEnds(); !got.Equal(t0.Add(5 * time.Minute)) {
		t.Errorf("a window cut by the next promotion ends there: %s", got)
	}
}

func TestCriticalAlerts(t *testing.T) {
	crit := map[string]string{"severity": "critical"}
	t.Run("a critical alert opened before the promotion keeps a PASS from passing", func(t *testing.T) {
		in := input(t, true, svc("api", 1000, 1000, 0, 0, 100, 100))
		in.Alerts = []Alert{{Rule: "SecretStoreNotReady", FiringAt: t0.Add(-15 * time.Hour), Labels: crit}, {Rule: "Warn", FiringAt: t0.Add(-time.Hour)}}
		r := Evaluate(in)
		if r.Verdict != Inconclusive || len(r.StandingCritical) != 1 || len(r.Findings) != 0 {
			t.Fatalf("verdict %s standing %+v findings %+v", r.Verdict, r.StandingCritical, r.Findings)
		}
		if why := InconclusiveWhy(r); !strings.Contains(why, "SecretStoreNotReady") {
			t.Errorf("why = %q", why)
		}
		if j := Rejudge(r, nil); j.Verdict != Inconclusive {
			t.Errorf("rejudge verdict = %s", j.Verdict)
		}
	})
	t.Run("a critical alert opened after the promotion fails whatever fail_on says", func(t *testing.T) {
		in := input(t, true, svc("api", 1000, 1000, 0, 0, 100, 100))
		in.Alerts = []Alert{{Rule: "Down", FiringAt: t0.Add(time.Minute), Labels: crit}}
		in.Thresholds.FailOn = nil
		r := Evaluate(in)
		if r.Verdict != Fail {
			t.Fatalf("verdict = %s %+v", r.Verdict, r.Findings)
		}
		if j := Rejudge(r, []string{release.HealthLatency}); j.Verdict != Fail {
			t.Errorf("rejudge verdict = %s", j.Verdict)
		}
	})
	t.Run("WithStandingAlerts folds alerts the judged report did not carry", func(t *testing.T) {
		r := Evaluate(input(t, true, svc("api", 1000, 1000, 0, 0, 100, 100)))
		if r.Verdict != Pass {
			t.Fatalf("verdict = %s", r.Verdict)
		}
		firing := []Alert{
			{Rule: "SecretStoreNotReady", Fingerprint: "a", FiringAt: t0.Add(-time.Hour), Labels: crit},
			{Rule: "SecretStoreNotReady", Fingerprint: "a", FiringAt: t0.Add(-time.Hour), Labels: crit},
			{Rule: "Backup", FiringAt: t0.Add(-time.Hour), Labels: map[string]string{"severity": "warning"}},
		}
		w := WithStandingAlerts(r, firing)
		if w.Verdict != Inconclusive || len(w.StandingCritical) != 1 {
			t.Errorf("verdict %s standing %+v", w.Verdict, w.StandingCritical)
		}
		if r.Verdict != Pass || r.StandingCritical != nil {
			t.Error("WithStandingAlerts changed its input")
		}
	})
}

// A control plane's warn-level alert_firing finding (its ref lost on the
// wire) for a critical alert becomes a failing one.
func TestWithStandingAlertsFailsAPostPromotionCriticalAlert(t *testing.T) {
	r := Evaluate(input(t, true, svc("api", 1000, 1000, 0, 0, 100, 100)))
	r.Findings = []Finding{{Code: release.HealthAlertFiring, Policy: PolicyWarn, Message: "alert Down opened after the promotion and is still firing"}}
	r.FiringAlerts = []Alert{{Rule: "Down", FiringAt: t0.Add(time.Minute), Labels: map[string]string{"severity": "critical"}}}
	w := WithStandingAlerts(r, nil)
	if w.Verdict != Fail || len(w.Findings) != 1 || w.Findings[0].Policy != PolicyFail || w.Findings[0].Ref != "Down" {
		t.Fatalf("verdict %s findings %+v", w.Verdict, w.Findings)
	}
	if r.Findings[0].Policy != PolicyWarn {
		t.Error("WithStandingAlerts changed its input")
	}
	if u := WithAlertStateUnread(Evaluate(input(t, true, svc("api", 1000, 1000, 0, 0, 100, 100))), "denied"); u.Verdict != Inconclusive {
		t.Errorf("unread alert state: verdict %s", u.Verdict)
	}
}

// Checking an older promotion must not fail it for a critical alert a later
// promotion opened after the judged after window ended.
func TestWithStandingAlertsIsBoundedByTheAfterWindow(t *testing.T) {
	r := Evaluate(input(t, true, svc("api", 1000, 1000, 0, 0, 100, 100)))
	crit := map[string]string{"severity": "critical"}
	w := WithStandingAlerts(r, []Alert{
		{Rule: "Later", FiringAt: r.After.To.Add(time.Hour), Labels: crit},
		{Rule: "AtTheEnd", FiringAt: r.After.To, Labels: crit},
		{Rule: "NoTime", Labels: crit},
	})
	if w.Verdict != Pass || len(w.Findings) != 0 || len(w.StandingCritical) != 0 {
		t.Errorf("verdict %s findings %+v standing %+v", w.Verdict, w.Findings, w.StandingCritical)
	}
	if w := WithStandingAlerts(r, []Alert{{Rule: "In", FiringAt: r.After.To.Add(-time.Second), Labels: crit}}); w.Verdict != Fail {
		t.Errorf("an alert opened inside the after window: verdict %s", w.Verdict)
	}
}

// Evaluate attributes alerts to the release only inside [P, after.To), as
// WithStandingAlerts does: an older release does not fail for a later
// incident, critical or not.
func TestEvaluateAlertsAreBoundedByTheAfterWindow(t *testing.T) {
	in := input(t, true, svc("api", 1000, 1000, 0, 0, 100, 100))
	end := in.Windows.After.To
	crit := map[string]string{"severity": "critical"}
	in.Alerts = []Alert{
		{Rule: "AtTheEnd", FiringAt: end, Labels: crit},
		{Rule: "Later", FiringAt: end.Add(time.Hour), Labels: crit},
		{Rule: "LaterWarn", FiringAt: end.Add(time.Hour)},
		{Rule: "Before", FiringAt: t0.Add(-time.Hour), Labels: crit},
	}
	in.Thresholds.FailOn = nil
	r := Evaluate(in)
	if len(r.Findings) != 0 || len(r.FiringAlerts) != 0 {
		t.Errorf("findings %+v firing %+v", r.Findings, r.FiringAlerts)
	}
	if r.Verdict != Inconclusive || len(r.StandingCritical) != 1 || r.StandingCritical[0].Rule != "Before" {
		t.Errorf("verdict %s standing %+v", r.Verdict, r.StandingCritical)
	}
	if j := Rejudge(r, nil); j.Verdict != Inconclusive {
		t.Errorf("rejudge verdict %s", j.Verdict)
	}
}

// An alert whose rule is labelled release_health=exclude watches something
// other than the env's workloads (MachineLoadHigh on the build host, v1.12.0):
// it is never a finding or a standing critical alert, only listed.
func TestExcludedAlertsJudgeNoRelease(t *testing.T) {
	excl := map[string]string{"severity": "critical", "machine": "ws-1", LabelReleaseHealth: "Exclude "}
	t.Run("Evaluate lists it and judges without it", func(t *testing.T) {
		in := input(t, true, svc("api", 1000, 1000, 0, 0, 100, 100))
		in.Thresholds.FailOn = append(in.Thresholds.FailOn, release.HealthAlertFiring)
		in.Alerts = []Alert{
			{Rule: "MachineLoadHigh", Fingerprint: "a", FiringAt: t0.Add(time.Minute), Labels: excl},
			{Rule: "MachineDiskLow", Fingerprint: "b", FiringAt: t0.Add(-time.Hour), Labels: excl},
		}
		r := Evaluate(in)
		if r.Verdict != Pass || len(r.Findings) != 0 || len(r.FiringAlerts) != 0 || len(r.StandingCritical) != 0 || len(r.ExcludedAlerts) != 2 {
			t.Fatalf("verdict %s findings %+v firing %+v standing %+v excluded %+v", r.Verdict, r.Findings, r.FiringAlerts, r.StandingCritical, r.ExcludedAlerts)
		}
		if w := WithStandingAlerts(r, in.Alerts); w.Verdict != Pass || len(w.Findings) != 0 || len(w.StandingCritical) != 0 {
			t.Errorf("WithStandingAlerts counted it: verdict %s findings %+v standing %+v", w.Verdict, w.Findings, w.StandingCritical)
		}
	})
	t.Run("WithoutExcludedAlerts takes it out of a report judged with it", func(t *testing.T) {
		r := Evaluate(input(t, true, svc("api", 1000, 1000, 0, 0, 100, 100)))
		r.FiringAlerts = []Alert{{Rule: "MachineLoadHigh", Fingerprint: "a", FiringAt: t0.Add(time.Minute), Labels: excl}}
		r.StandingCritical = []Alert{{Rule: "MachineDiskLow", Fingerprint: "b", FiringAt: t0.Add(-time.Hour), Labels: excl}}
		// The ref lost on the wire: matched by its message.
		r.Findings = []Finding{{Code: release.HealthAlertFiring, Policy: PolicyFail, Message: "alert MachineLoadHigh opened after the promotion and is still firing"}}
		r.Verdict = Fail
		w := WithoutExcludedAlerts(r, nil)
		if w.Verdict != Pass || len(w.Findings) != 0 || len(w.FiringAlerts) != 0 || len(w.StandingCritical) != 0 || len(w.ExcludedAlerts) != 2 {
			t.Fatalf("verdict %s findings %+v firing %+v standing %+v excluded %+v", w.Verdict, w.Findings, w.FiringAlerts, w.StandingCritical, w.ExcludedAlerts)
		}
		if r.Verdict != Fail || len(r.Findings) != 1 || r.ExcludedAlerts != nil {
			t.Error("WithoutExcludedAlerts changed its input")
		}
	})
	t.Run("a rule with a counted alert keeps its finding", func(t *testing.T) {
		r := Evaluate(input(t, true, svc("api", 1000, 1000, 0, 0, 100, 100)))
		r.FiringAlerts = []Alert{
			{Rule: "Down", Fingerprint: "a", FiringAt: t0.Add(time.Minute), Labels: excl},
			{Rule: "Down", Fingerprint: "c", FiringAt: t0.Add(time.Minute), Labels: map[string]string{"severity": "critical"}},
		}
		r.Findings = []Finding{{Code: release.HealthAlertFiring, Ref: "Down", Policy: PolicyFail, Message: "alert Down opened after the promotion and is still firing"}}
		r.Verdict = Fail
		if w := WithoutExcludedAlerts(r, nil); w.Verdict != Fail || len(w.Findings) != 1 || len(w.FiringAlerts) != 1 || len(w.ExcludedAlerts) != 1 {
			t.Errorf("verdict %s findings %+v firing %+v excluded %+v", w.Verdict, w.Findings, w.FiringAlerts, w.ExcludedAlerts)
		}
	})
	t.Run("an alert already listed as excluded still takes its finding out", func(t *testing.T) {
		r := Evaluate(input(t, true, svc("api", 1000, 1000, 0, 0, 100, 100)))
		a := Alert{Rule: "MachineLoadHigh", Fingerprint: "a", FiringAt: t0.Add(time.Minute), Labels: excl}
		r.ExcludedAlerts = []Alert{a, a}
		r.Findings = []Finding{{Code: release.HealthAlertFiring, Ref: "MachineLoadHigh", Policy: PolicyFail, Message: "alert MachineLoadHigh opened after the promotion and is still firing"}}
		r.Verdict = Fail
		if w := WithoutExcludedAlerts(r, nil); w.Verdict != Pass || len(w.Findings) != 0 || len(w.ExcludedAlerts) != 1 {
			t.Errorf("verdict %s findings %+v excluded %+v", w.Verdict, w.Findings, w.ExcludedAlerts)
		}
	})
	t.Run("the live alert state names a rule the report does not list", func(t *testing.T) {
		r := Evaluate(input(t, true, svc("api", 1000, 1000, 0, 0, 100, 100)))
		r.Findings = []Finding{{Code: release.HealthAlertFiring, Policy: PolicyFail, Message: "alert MachineLoadHigh opened after the promotion and is still firing"}}
		r.Verdict = Fail
		if w := WithoutExcludedAlerts(r, nil); w.Verdict != Fail || len(w.Findings) != 1 {
			t.Errorf("no alert names the rule excluded, yet: verdict %s findings %+v", w.Verdict, w.Findings)
		}
		live := []Alert{{Rule: "MachineLoadHigh", Fingerprint: "a", FiringAt: t0.Add(time.Minute), Labels: excl}}
		if w := WithoutExcludedAlerts(r, live); w.Verdict != Pass || len(w.Findings) != 0 || len(w.ExcludedAlerts) != 1 {
			t.Errorf("verdict %s findings %+v excluded %+v", w.Verdict, w.Findings, w.ExcludedAlerts)
		}
		live = append(live, Alert{Rule: "MachineLoadHigh", Fingerprint: "b", FiringAt: t0.Add(time.Minute), Labels: map[string]string{"severity": "critical"}})
		if w := WithoutExcludedAlerts(r, live); w.Verdict != Fail || len(w.Findings) != 1 {
			t.Errorf("a counted live alert of the rule: verdict %s findings %+v", w.Verdict, w.Findings)
		}
	})
	t.Run("nothing excluded leaves the report as judged", func(t *testing.T) {
		r := Evaluate(input(t, true, svc("api", 1000, 1000, 0, 0, 100, 100)))
		r.Verdict = Inconclusive
		if w := WithoutExcludedAlerts(r, nil); w.Verdict != Inconclusive {
			t.Errorf("verdict %s", w.Verdict)
		}
	})
}
