package releasehealth

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// A thin before window (forge bug d1dc1977): a service with fewer than
// min_requests requests before the promotion has no baseline to compare its
// error rate and latency against, so it proves nothing good about the
// release. The absolute findings (an issue, a firing alert) still fail it.
func TestThinBaseline(t *testing.T) {
	codes := func(r Report) []string {
		var out []string
		for _, f := range r.Findings {
			out = append(out, f.Code)
		}
		return out
	}

	t.Run("thin before, healthy after is inconclusive", func(t *testing.T) {
		r := Evaluate(input(t, true, svc("api", 15, 1000, 0, 0, 100, 100)))
		if r.Verdict != Inconclusive {
			t.Fatalf("verdict = %s, want INCONCLUSIVE (%+v)", r.Verdict, r.Findings)
		}
		if got := InconclusiveCode(r); got != ReasonThinBaseline {
			t.Errorf("reason code = %q, want %q", got, ReasonThinBaseline)
		}
		why := InconclusiveWhy(r)
		if !strings.Contains(why, "api had 15 requests before the promotion") || !strings.Contains(why, "min_requests (50)") {
			t.Errorf("the reason must name the thin before window and its count: %s", why)
		}
		if !slices.Contains(r.NotEvaluated, "service:api") {
			t.Errorf("not_evaluated = %v, want service:api", r.NotEvaluated)
		}
	})

	t.Run("an empty before window is inconclusive", func(t *testing.T) {
		r := Evaluate(input(t, true, svc("api", 0, 1000, 0, 0, 0, 900)))
		if r.Verdict != Inconclusive || !strings.Contains(InconclusiveWhy(r), "api had 0 requests before the promotion") {
			t.Errorf("verdict = %s: %s", r.Verdict, InconclusiveWhy(r))
		}
	})

	t.Run("a thin baseline judges no comparative finding", func(t *testing.T) {
		r := Evaluate(input(t, true, svc("api", 15, 1000, 0, 0.5, 100, 900)))
		if r.Verdict != Inconclusive || len(r.Findings) != 0 {
			t.Errorf("verdict = %s findings = %v, want INCONCLUSIVE and none", r.Verdict, codes(r))
		}
	})

	t.Run("thin before with a new issue fails", func(t *testing.T) {
		in := input(t, true, svc("api", 15, 1000, 0, 0, 100, 100))
		in.NewIssues = []Issue{{ID: "ISS-1", Service: "api", Title: "boom", Events: 12, FirstSeenAt: t0.Add(time.Minute), FirstVersion: "v2"}}
		if r := Evaluate(in); r.Verdict != Fail || !slices.Equal(codes(r), []string{release.HealthNewIssue}) {
			t.Errorf("verdict = %s findings = %v, want FAIL on new_issue", r.Verdict, codes(r))
		}
	})

	t.Run("thin before with a regressed issue fails", func(t *testing.T) {
		in := input(t, true, svc("api", 0, 1000, 0, 0, 100, 100))
		in.RegressedIssues = []Issue{{ID: "ISS-2", Service: "api", Title: "back", Events: 4, RegressedAt: t0.Add(time.Minute)}}
		if r := Evaluate(in); r.Verdict != Fail {
			t.Errorf("verdict = %s findings = %v, want FAIL", r.Verdict, codes(r))
		}
	})

	t.Run("thin before with a firing alert fails", func(t *testing.T) {
		in := input(t, true, svc("api", 0, 1000, 0, 0, 100, 100))
		in.Alerts = []Alert{{Rule: "Down", Fingerprint: "a", FiringAt: t0.Add(time.Minute)}}
		if r := Evaluate(in); r.Verdict != Fail {
			t.Errorf("verdict = %s findings = %v, want FAIL", r.Verdict, codes(r))
		}
	})

	t.Run("healthy both windows is unchanged", func(t *testing.T) {
		r := Evaluate(input(t, true, svc("api", 1000, 1000, 0.01, 0.01, 100, 100)))
		if r.Verdict != Pass || len(r.NotEvaluated) != 0 {
			t.Errorf("verdict = %s not_evaluated = %v, want PASS and none", r.Verdict, r.NotEvaluated)
		}
	})

	t.Run("a comparable service still passes; the thin one is listed", func(t *testing.T) {
		r := Evaluate(input(t, true, svc("api", 1000, 1000, 0, 0, 100, 100), svc("new", 3, 400, 0, 0, 0, 50)))
		if r.Verdict != Pass {
			t.Errorf("verdict = %s, want PASS", r.Verdict)
		}
		if !slices.Equal(r.NotEvaluated, []string{"service:new"}) {
			t.Errorf("not_evaluated = %v, want [service:new]", r.NotEvaluated)
		}
	})

	t.Run("min_requests decides what is thin", func(t *testing.T) {
		in := input(t, true, svc("api", 15, 1000, 0, 0, 100, 100))
		in.Thresholds.MinRequests = 10
		if r := Evaluate(in); r.Verdict != Pass {
			t.Errorf("15 requests before with min_requests 10: verdict = %s, want PASS", r.Verdict)
		}
	})

	t.Run("a backend's not_evaluated is kept", func(t *testing.T) {
		in := input(t, true, svc("api", 15, 1000, 0, 0, 100, 100))
		in.NotEvaluated = []string{release.HealthAlertFiring}
		r := Evaluate(in)
		if !slices.Equal(r.NotEvaluated, []string{release.HealthAlertFiring, "service:api"}) {
			t.Errorf("not_evaluated = %v", r.NotEvaluated)
		}
		if !slices.Equal(in.NotEvaluated, []string{release.HealthAlertFiring}) {
			t.Errorf("Evaluate wrote through the input's slice: %v", in.NotEvaluated)
		}
	})

	t.Run("a frontend with a thin page-view baseline is inconclusive", func(t *testing.T) {
		site := ServiceHealth{Service: "site", Signal: SignalBrowser, Browser: &BrowserHealth{
			Before: BrowserStats{PageViews: 5},
			After:  BrowserStats{PageViews: 500, JSErrors: 400},
		}}
		r := Evaluate(input(t, true, site))
		if r.Verdict != Inconclusive || len(r.Findings) != 0 {
			t.Errorf("verdict = %s findings = %v, want INCONCLUSIVE and none", r.Verdict, codes(r))
		}
		if InconclusiveCode(r) != ReasonThinBaseline || !strings.Contains(InconclusiveWhy(r), "site had 5 page views before the promotion") {
			t.Errorf("reason %s: %s", InconclusiveCode(r), InconclusiveWhy(r))
		}
		if !slices.Contains(r.NotEvaluated, "browser:site") {
			t.Errorf("not_evaluated = %v, want browser:site", r.NotEvaluated)
		}
	})

	t.Run("rejudging keeps it inconclusive", func(t *testing.T) {
		r := Rejudge(Evaluate(input(t, true, svc("api", 15, 1000, 0, 0, 100, 100))), []string{release.HealthLatency})
		if r.Verdict != Inconclusive {
			t.Errorf("verdict = %s", r.Verdict)
		}
	})
}

// require_baseline is the env's own gate over the default.
func TestRequireBaseline(t *testing.T) {
	healthy := svc("api", 1000, 1000, 0, 0, 100, 100)
	thinBad := svc("new", 15, 500, 0, 0.5, 100, 900)
	with := func(policy string, services ...ServiceHealth) Input {
		in := input(t, true, services...)
		th, err := Resolve(Options{Overrides: map[string]string{ThresholdRequireBaseline: policy}})
		if err != nil {
			t.Fatal(err)
		}
		in.Thresholds = th
		return in
	}

	t.Run("any is the default", func(t *testing.T) {
		if th := defaults(t); th.RequireBaseline != release.BaselineAny {
			t.Errorf("default = %q", th.RequireBaseline)
		}
		if r := Evaluate(input(t, true, healthy, thinBad)); r.Verdict != Pass || !slices.Equal(r.NotEvaluated, []string{"service:new"}) {
			t.Errorf("verdict %s not_evaluated %v", r.Verdict, r.NotEvaluated)
		}
	})

	t.Run("every: one thin baseline holds the release", func(t *testing.T) {
		r := Evaluate(with(release.BaselineEvery, healthy, thinBad))
		if r.Verdict != Inconclusive || InconclusiveCode(r) != ReasonThinBaseline {
			t.Fatalf("verdict %s (%s), want INCONCLUSIVE thin_baseline", r.Verdict, InconclusiveCode(r))
		}
		if why := InconclusiveWhy(r); !strings.Contains(why, "require_baseline is every") || !strings.Contains(why, "new had 15 requests") {
			t.Errorf("reason: %s", why)
		}
		if r := Evaluate(with(release.BaselineEvery, healthy)); r.Verdict != Pass {
			t.Errorf("every service has a baseline: verdict %s", r.Verdict)
		}
	})

	t.Run("every still fails on an absolute finding", func(t *testing.T) {
		in := with(release.BaselineEvery, healthy, thinBad)
		in.NewIssues = []Issue{{ID: "ISS-1", Service: "new", Events: 12, FirstSeenAt: t0.Add(time.Minute), FirstVersion: "v2"}}
		if r := Evaluate(in); r.Verdict != Fail {
			t.Errorf("verdict %s", r.Verdict)
		}
	})

	t.Run("none compares against a thin baseline", func(t *testing.T) {
		r := Evaluate(with(release.BaselineNone, svc("api", 15, 1000, 0, 0, 100, 100)))
		if r.Verdict != Pass || len(r.NotEvaluated) != 0 {
			t.Errorf("verdict %s not_evaluated %v, want PASS as before the rule", r.Verdict, r.NotEvaluated)
		}
		r = Evaluate(with(release.BaselineNone, healthy, thinBad))
		if r.Verdict != Fail {
			t.Errorf("the thin service's error rate is compared under none: verdict %s findings %+v", r.Verdict, r.Findings)
		}
	})

	t.Run("the policy is listed and survives a rejudge", func(t *testing.T) {
		r := Evaluate(with(release.BaselineEvery, healthy, thinBad))
		if !slices.Contains(r.Thresholds, Threshold{ThresholdRequireBaseline, release.BaselineEvery, SourceRequest}) {
			t.Errorf("thresholds %+v", r.Thresholds)
		}
		if got := Rejudge(r, []string{release.HealthNewIssue}).Verdict; got != Inconclusive {
			t.Errorf("rejudged: %s", got)
		}
	})

	t.Run("the env's declaration sets it", func(t *testing.T) {
		shape := release.DefaultReleaseHealth()
		shape.RequireBaseline = release.BaselineEvery
		th, err := Resolve(Options{Shape: &shape})
		if err != nil || th.RequireBaseline != release.BaselineEvery || th.Sources[ThresholdRequireBaseline] != SourceReleaseShape {
			t.Errorf("Resolve = %+v, %v", th, err)
		}
	})

	t.Run("an unknown policy is refused", func(t *testing.T) {
		if _, err := ParseOverrides([]string{"require_baseline=some"}); err != nil {
			t.Fatalf("ParseOverrides takes the name; Resolve judges the value: %v", err)
		}
		if _, err := Resolve(Options{Overrides: map[string]string{ThresholdRequireBaseline: "some"}}); !errors.Is(err, ErrInvalid) {
			t.Errorf("err = %v, want ErrInvalid", err)
		}
	})
}

// A control plane that predates the rule answers PASS over a thin baseline;
// WithThinBaselines brings its answer to the rule a CLI applies.
func TestWithThinBaselines(t *testing.T) {
	old := Evaluate(input(t, true, svc("api", 15, 1000, 0, 0, 100, 100)))
	old.Verdict, old.NotEvaluated = Pass, nil

	r := WithThinBaselines(old)
	if r.Verdict != Inconclusive || InconclusiveCode(r) != ReasonThinBaseline {
		t.Errorf("verdict = %s (%s), want INCONCLUSIVE thin_baseline", r.Verdict, InconclusiveCode(r))
	}
	if !slices.Equal(r.NotEvaluated, []string{"service:api"}) {
		t.Errorf("not_evaluated = %v", r.NotEvaluated)
	}
	if again := WithThinBaselines(r); !slices.Equal(again.NotEvaluated, r.NotEvaluated) || again.Verdict != r.Verdict {
		t.Errorf("not idempotent: %v %s", again.NotEvaluated, again.Verdict)
	}
	if old.Verdict != Pass || old.NotEvaluated != nil {
		t.Errorf("WithThinBaselines changed its argument")
	}

	// A control plane's answer carries its policy in the thresholds.
	both := Evaluate(input(t, true, svc("api", 1000, 1000, 0, 0, 100, 100), svc("new", 3, 400, 0, 0, 0, 50)))
	if both.Verdict != Pass {
		t.Fatalf("verdict %s", both.Verdict)
	}
	both.Thresholds = append(slices.Clone(both.Thresholds[:len(both.Thresholds)-1]), Threshold{ThresholdRequireBaseline, release.BaselineEvery, SourceReleaseShape})
	if got := WithThinBaselines(both).Verdict; got != Inconclusive {
		t.Errorf("require_baseline every in the answer: verdict %s", got)
	}

	failed := old
	failed.Verdict = Fail
	if got := WithThinBaselines(failed).Verdict; got != Fail {
		t.Errorf("a FAIL is never softened: %s", got)
	}
}
