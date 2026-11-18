package releasehealth

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// endpoint is one endpoint: requests and errors before and after the promotion.
func endpoint(name string, beforeReq, beforeErrs, afterReq, afterErrs int64) EndpointHealth {
	return EndpointHealth{Endpoint: name, Before: stats(beforeReq, beforeErrs), After: stats(afterReq, afterErrs)}
}

func stats(req, errs int64) WindowStats {
	w := WindowStats{Requests: req, P95Ms: 100}
	if req > 0 {
		w.ErrorRate = float64(errs) / float64(req)
	}
	return w
}

// withBaseline is e with its baseline_window figures.
func withBaseline(e EndpointHealth, req, errs int64) EndpointHealth {
	b := stats(req, errs)
	e.Baseline = &b
	return e
}

// service sums its endpoints into the service-level figures, as the windows'
// RED reads do.
func service(name string, eps ...EndpointHealth) ServiceHealth {
	var bReq, bErr, aReq, aErr float64
	for _, e := range eps {
		bReq += float64(e.Before.Requests)
		bErr += e.Before.ErrorRate * float64(e.Before.Requests)
		aReq += float64(e.After.Requests)
		aErr += e.After.ErrorRate * float64(e.After.Requests)
	}
	s := ServiceHealth{Service: name, Endpoints: eps,
		Before: WindowStats{Requests: int64(bReq), P95Ms: 100}, After: WindowStats{Requests: int64(aReq), P95Ms: 100}}
	if bReq > 0 {
		s.Before.ErrorRate = bErr / bReq
	}
	if aReq > 0 {
		s.After.ErrorRate = aErr / aReq
	}
	return s
}

// flat is s as a control plane answers it: service-level figures only.
func flat(s ServiceHealth) ServiceHealth {
	s.Endpoints = nil
	return s
}

func thresholds(t *testing.T, overrides map[string]string) Thresholds {
	t.Helper()
	th, err := Resolve(Options{Overrides: overrides})
	if err != nil {
		t.Fatal(err)
	}
	return th
}

func errorRateFindings(r Report) []Finding {
	var out []Finding
	for _, f := range r.Findings {
		if f.Code == release.HealthErrorRate {
			out = append(out, f)
		}
	}
	return out
}

// incidentService is reliant-api-server in control-plane v1.20.1-rc.1's
// window (control-plane #2820): Connect RPCs at 0% in both windows, and 16
// terminal.session websockets with 8 errors, all one user daemon whose
// machine did not answer, that the 15-minute before window held none of.
// terminalHistory is that endpoint's baseline_window figures, or nil when
// none were read; it had failed 9.2% of the time.
func incidentService(terminalHistory bool) ServiceHealth {
	terminal := endpoint("terminal.session", 0, 0, 16, 8)
	if terminalHistory {
		terminal = withBaseline(terminal, 1000, 92)
	}
	return service("reliant-api-server",
		withBaseline(endpoint("reliant.v1.ChatService/ListChats", 120, 0, 140, 0), 9000, 0),
		withBaseline(endpoint("reliant.v1.ProjectService/GetProject", 60, 0, 60, 0), 4000, 0),
		endpoint("reliant.v1.DaemonService/Heartbeat", 30, 0, 30, 0),
		terminal)
}

// The incident's exact shape passes, judged by the local rule (per
// endpoint, with or without the endpoint's history) and by a control
// plane's service-level figures alike; error_rate_min_errors=1, the rule
// before the floor, fails it as prod did.
func TestErrorRateIncidentShapeDoesNotFail(t *testing.T) {
	for name, s := range map[string]ServiceHealth{
		"per endpoint, with the endpoint's history":    incidentService(true),
		"per endpoint, without the endpoint's history": incidentService(false),
		"service-level, as a control plane answers":    flat(incidentService(false)),
	} {
		t.Run(name, func(t *testing.T) {
			if s.Before.ErrorRate != 0 || s.After.Requests != 246 || s.AfterErrors() != 8 {
				t.Fatalf("fixture drifted from the incident: %+v errors %d", s, s.AfterErrors())
			}
			in := input(t, true, s)
			r := Evaluate(in)
			if r.Verdict != Pass || len(r.Findings) != 0 {
				t.Errorf("verdict %s findings %+v; want PASS, nothing", r.Verdict, r.Findings)
			}

			in.Thresholds = thresholds(t, map[string]string{ThresholdErrorRateMinErrors: "1"})
			r = Evaluate(in)
			got := errorRateFindings(r)
			if r.Verdict != Fail || len(got) != 1 || got[0].Service != "reliant-api-server" || got[0].Policy != PolicyFail {
				t.Errorf("error_rate_min_errors=1: verdict %s findings %+v; want the old FAIL", r.Verdict, r.Findings)
			}
		})
	}
}

// Real regressions still fail at the defaults: one busy endpoint jumping,
// a rise across many endpoints none of which is busy enough to judge alone,
// and the same rise judged by a control plane's service-level figures.
func TestErrorRateRealRegressionFails(t *testing.T) {
	var many []EndpointHealth
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"} {
		many = append(many, endpoint("rpc/"+n, 30, 0, 25, 5)) // each below min_requests in both windows
	}
	tests := []struct {
		name string
		s    ServiceHealth
		want float64 // the rise the finding carries
	}{
		{"one busy endpoint", service("api", endpoint("busy", 500, 0, 500, 25), endpoint("quiet", 500, 0, 500, 0)), 0.025},
		{"one busy endpoint, judged against its long baseline", service("api",
			withBaseline(endpoint("busy", 20, 0, 500, 25), 5000, 0), endpoint("other", 500, 0, 500, 0)), 0.025},
		{"a rise across many sparse endpoints", service("api", many...), 0.2},
		{"service-wide, as a control plane answers", flat(service("api", many...)), 0.2},
		{"a new endpoint that fails", service("api", endpoint("old", 500, 0, 500, 0), endpoint("new", 0, 0, 100, 40)), 40.0 / 600},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := Evaluate(input(t, true, tc.s))
			got := errorRateFindings(r)
			if r.Verdict != Fail || len(got) != 1 || got[0].Policy != PolicyFail {
				t.Fatalf("verdict %s findings %+v; want FAIL on error_rate", r.Verdict, r.Findings)
			}
			if math.Abs(got[0].Value-tc.want) > 1e-9 {
				t.Errorf("rise = %v, want %v", got[0].Value, tc.want)
			}
			if per := strings.Contains(got[0].Message, "per endpoint"); per != (len(tc.s.Endpoints) > 0) {
				t.Errorf("message %q: per-endpoint label %v", got[0].Message, per)
			}
		})
	}
}

// The floor is inclusive: error_rate_min_errors errors can fail, one fewer
// cannot, whatever the rise.
func TestErrorRateMinErrorsBoundary(t *testing.T) {
	for errs, want := range map[int64]Verdict{9: Pass, 10: Fail} {
		s := service("api", endpoint("rpc", 500, 0, 500, errs))
		if r := Evaluate(input(t, true, s)); r.Verdict != want {
			t.Errorf("%d errors (+%.1f%%): verdict %s, want %s", errs, s.After.ErrorRate*100, r.Verdict, want)
		}
	}
}

// An endpoint that always failed at its rate, absent from a quiet before
// window, is no rise once its own history is the baseline: the class of the
// incident at a volume the floor cannot absorb. Without the history it is
// judged against the service's before rate, the rule without the window.
func TestErrorRateSparseEndpointUsesItsBaselineWindow(t *testing.T) {
	chronic := endpoint("jobs/poll", 0, 0, 100, 30)
	steady := endpoint("rpc", 400, 0, 400, 0)
	if r := Evaluate(input(t, true, service("api", withBaseline(chronic, 2000, 600), steady))); r.Verdict != Pass {
		t.Errorf("with its history: verdict %s findings %+v; want PASS", r.Verdict, r.Findings)
	}
	if r := Evaluate(input(t, true, service("api", chronic, steady))); r.Verdict != Fail {
		t.Errorf("without its history: verdict %s; want FAIL against the service's 0%%", r.Verdict)
	}
	// A history thinner than min_requests is no baseline either.
	if r := Evaluate(input(t, true, service("api", withBaseline(chronic, 40, 12), steady))); r.Verdict != Fail {
		t.Errorf("thin history: verdict %s; want FAIL", r.Verdict)
	}
	// It is worse than its history: still a rise.
	worse := endpoint("jobs/poll", 0, 0, 100, 90)
	if r := Evaluate(input(t, true, service("api", withBaseline(worse, 2000, 600), steady))); r.Verdict != Fail {
		t.Errorf("worse than its history: verdict %s; want FAIL", r.Verdict)
	}
}

func TestEndpointBaselineErrorRate(t *testing.T) {
	const service = 0.04
	tests := []struct {
		name string
		e    EndpointHealth
		min  int
		want float64
	}{
		{"its before window, at min_requests", withBaseline(endpoint("x", 50, 5, 1, 0), 1000, 500), 50, 0.1},
		{"its history, when the before window is thin", withBaseline(endpoint("x", 49, 49, 1, 0), 1000, 500), 50, 0.5},
		{"the service, with neither", withBaseline(endpoint("x", 49, 49, 1, 0), 49, 49), 50, service},
		{"the service, with no history read", endpoint("x", 0, 0, 1, 0), 50, service},
		{"any before traffic under require_baseline none", endpoint("x", 2, 1, 1, 0), 1, 0.5},
	}
	for _, tc := range tests {
		if got := tc.e.BaselineErrorRate(service, tc.min); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// require_baseline none takes any before traffic as an endpoint's baseline,
// as it does a service's.
func TestErrorRateBaselineNone(t *testing.T) {
	s := service("api", endpoint("flaky", 10, 5, 40, 20), endpoint("rpc", 500, 0, 500, 0))
	if r := Evaluate(input(t, true, s)); r.Verdict != Fail {
		t.Errorf("any: verdict %s; want FAIL against the service's rate", r.Verdict)
	}
	in := input(t, true, s)
	in.Thresholds = thresholds(t, map[string]string{ThresholdRequireBaseline: release.BaselineNone})
	if r := Evaluate(in); r.Verdict != Pass {
		t.Errorf("none: verdict %s findings %+v; want PASS against its own 50%%", r.Verdict, r.Findings)
	}
}

func TestErrorRateThresholds(t *testing.T) {
	l := fromList(defaults(t).List())
	if th := l[ThresholdErrorRateMinErrors]; th.Value != "10" || th.Source != SourceDefault {
		t.Errorf("error_rate_min_errors default = %+v", th)
	}
	if th := l[ThresholdBaselineWindow]; th.Value != "24h" || th.Source != SourceDefault {
		t.Errorf("baseline_window default = %+v", th)
	}
	shape := release.DefaultReleaseHealth()
	shape.ErrorRateMinErrors, shape.BaselineWindowSeconds = 3, int((48 * time.Hour).Seconds())
	th, err := Resolve(Options{Shape: &shape})
	if err != nil {
		t.Fatal(err)
	}
	if th.ErrorRateMinErrors != 3 || th.BaselineWindow != 48*time.Hour || th.Sources[ThresholdErrorRateMinErrors] != SourceReleaseShape {
		t.Errorf("from the shape: %+v", th)
	}
	th, err = Resolve(Options{Shape: &shape, Overrides: map[string]string{ThresholdErrorRateMinErrors: "1", ThresholdBaselineWindow: "6h"}})
	if err != nil || th.ErrorRateMinErrors != 1 || th.BaselineWindow != 6*time.Hour || th.Sources[ThresholdBaselineWindow] != SourceRequest {
		t.Errorf("request over shape: %+v %v", th, err)
	}
	if _, err := ParseOverrides([]string{"error_rate_min_errors=1", "baseline_window=12h"}); err != nil {
		t.Errorf("both are --threshold names: %v", err)
	}
	for _, bad := range []map[string]string{
		{ThresholdErrorRateMinErrors: "0"}, {ThresholdErrorRateMinErrors: "-1"}, {ThresholdErrorRateMinErrors: "many"},
		{ThresholdBaselineWindow: "5m"}, {ThresholdBaselineWindow: "169h"}, {ThresholdBaselineWindow: "a day"},
	} {
		if _, err := Resolve(Options{Overrides: bad}); !errors.Is(err, ErrInvalid) {
			t.Errorf("%v: err = %v, want ErrInvalid", bad, err)
		}
	}
}

// cpReport is a control plane's answer that predates error_rate_min_errors:
// service-level figures, an error_rate finding judged without the floor, and
// no such threshold listed.
func cpReport(t *testing.T, s ServiceHealth) Report {
	in := input(t, true, flat(s))
	in.Thresholds = thresholds(t, map[string]string{ThresholdErrorRateMinErrors: "1"})
	r := Evaluate(in)
	var ths []Threshold
	for _, th := range r.Thresholds {
		if th.Name != ThresholdErrorRateMinErrors && th.Name != ThresholdBaselineWindow {
			ths = append(ths, th)
		}
	}
	r.Thresholds = ths
	if r.Verdict != Fail {
		t.Fatalf("fixture: the control plane's verdict is %s, want FAIL", r.Verdict)
	}
	return r
}

// obs check holds a control plane's answer to the floor before the control
// plane re-pins: the incident's answer passes, and lists the threshold.
func TestWithErrorRateMinErrors(t *testing.T) {
	r := WithErrorRateMinErrors(cpReport(t, incidentService(false)), nil)
	if r.Verdict != Pass || len(r.Findings) != 0 {
		t.Errorf("incident: verdict %s findings %+v; want PASS", r.Verdict, r.Findings)
	}
	if th := fromList(r.Thresholds)[ThresholdErrorRateMinErrors]; th.Value != "10" || th.Source != SourceDefault {
		t.Errorf("threshold listed as %+v", th)
	}
	if i, j := indexOf(r.Thresholds, ThresholdMaxErrorRateIncrease), indexOf(r.Thresholds, ThresholdErrorRateMinErrors); j != i+1 {
		t.Errorf("listed at %d, want beside max_error_rate_increase (%d)", j, i)
	}

	// --threshold error_rate_min_errors=1 keeps the old verdict.
	if r := WithErrorRateMinErrors(cpReport(t, incidentService(false)), map[string]string{ThresholdErrorRateMinErrors: "1"}); r.Verdict != Fail ||
		fromList(r.Thresholds)[ThresholdErrorRateMinErrors].Source != SourceRequest {
		t.Errorf("override 1: verdict %s thresholds %+v", r.Verdict, r.Thresholds)
	}
	// A threshold the answer lists is the one applied.
	listed := cpReport(t, incidentService(false))
	listed.Thresholds = append(listed.Thresholds, Threshold{ThresholdErrorRateMinErrors, "8", SourceReleaseShape})
	if r := WithErrorRateMinErrors(listed, nil); r.Verdict != Fail {
		t.Errorf("a listed 8 holds 8 errors: verdict %s", r.Verdict)
	}
	// A real regression is untouched.
	broad := cpReport(t, service("api", endpoint("a", 500, 0, 500, 40)))
	if r := WithErrorRateMinErrors(broad, nil); r.Verdict != Fail || len(errorRateFindings(r)) != 1 {
		t.Errorf("40 errors: verdict %s findings %+v", r.Verdict, r.Findings)
	}
	// A finding whose service is not listed has nothing to count: kept.
	orphan := cpReport(t, incidentService(false))
	orphan.Services = nil
	if r := WithErrorRateMinErrors(orphan, nil); r.Verdict != Fail {
		t.Errorf("unlisted service: verdict %s", r.Verdict)
	}
}

// Evaluate's own report is already judged by the floor: the fold changes
// nothing over it.
func TestWithErrorRateMinErrorsIsIdempotentOverEvaluate(t *testing.T) {
	for _, s := range []ServiceHealth{incidentService(true), service("api", endpoint("a", 500, 0, 500, 40))} {
		r := Evaluate(input(t, true, s))
		got := WithErrorRateMinErrors(r, nil)
		if got.Verdict != r.Verdict || len(got.Findings) != len(r.Findings) || len(got.Thresholds) != len(r.Thresholds) {
			t.Errorf("changed Evaluate's report: %s %+v -> %s %+v", r.Verdict, r.Findings, got.Verdict, got.Findings)
		}
	}
}

func indexOf(ths []Threshold, name string) int {
	for i, th := range ths {
		if th.Name == name {
			return i
		}
	}
	return -1
}
