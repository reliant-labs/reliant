package releasehealth

import (
	"slices"
	"testing"

	"github.com/reliant-labs/forge/pkg/obsquery"
	"github.com/reliant-labs/forge/pkg/release"
)

func names(ss []ServiceHealth) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.Service + ":" + s.Signal
	}
	return out
}

// Precedence: a service's own spans; ingress for a service with no span
// request in either window; browser figures beside either, or alone.
func TestCombinePrecedence(t *testing.T) {
	spans := []ServiceHealth{
		svc("api", 1000, 1000, 0, 0, 100, 100), // has spans: ingress ignored
		svc("idle", 0, 0, 0, 0, 0, 0),          // no span requests: ingress wins
		svc("quiet", 0, 0, 0, 0, 0, 0),         // no span requests, no ingress: kept
	}
	ingress := []ServiceHealth{
		svc("api", 5000, 5000, 0.5, 0.5, 1, 1),
		svc("idle", 200, 200, 0, 0.1, 10, 10),
		svc("site", 300, 300, 0, 0, 5, 5),
	}
	browser := map[string]BrowserHealth{
		"site":  {After: BrowserStats{PageViews: 10}},
		"web":   {Before: BrowserStats{PageViews: 100}, After: BrowserStats{PageViews: 100}},
		"empty": {},
	}
	got := Combine(spans, ingress, browser)
	if want := []string{"api:spans", "quiet:spans", "idle:ingress", "site:ingress", "web:browser"}; !slices.Equal(names(got), want) {
		t.Fatalf("Combine = %v, want %v", names(got), want)
	}
	if got[0].After.ErrorRate != 0 || got[0].Browser != nil {
		t.Errorf("api took ingress figures: %+v", got[0])
	}
	if got[2].After.ErrorRate != 0.1 || got[2].VersionShare != nil || got[2].Endpoints != nil {
		t.Errorf("idle = %+v, want its ingress figures and nothing else", got[2])
	}
	if got[3].Browser == nil || got[3].Browser.After.PageViews != 10 {
		t.Errorf("site's browser figures were not attached: %+v", got[3])
	}
	if got[4].Browser == nil || got[4].After.Requests != 0 {
		t.Errorf("web = %+v, want browser figures alone", got[4])
	}
}

// A static site with no spans is judged on ingress: its 5xx share and its
// p95 reach the same findings, and enough ingress traffic proves it good.
func TestIngressFiguresAreJudgedLikeSpans(t *testing.T) {
	site := Combine(nil, []ServiceHealth{svc("site", 1000, 1000, 0.001, 0.08, 20, 20)}, nil)
	r := Evaluate(input(t, true, site...))
	if r.Verdict != Fail || len(r.Findings) != 1 || r.Findings[0].Code != release.HealthErrorRate || r.Findings[0].Service != "site" {
		t.Fatalf("verdict %s findings %+v, want an error_rate failure on site", r.Verdict, r.Findings)
	}
	ok := Evaluate(input(t, true, Combine(nil, []ServiceHealth{svc("site", 1000, 1000, 0, 0, 20, 20)}, nil)...))
	if ok.Verdict != Pass || ok.NoRequestTelemetry() {
		t.Fatalf("a healthy site judged on ingress = %s (no telemetry %v)", ok.Verdict, ok.NoRequestTelemetry())
	}
}

func browserOnly(before, after BrowserStats) []ServiceHealth {
	return Combine(nil, nil, map[string]BrowserHealth{"web": {Before: before, After: after}})
}

func TestBrowserFindings(t *testing.T) {
	steady := BrowserStats{PageViews: 1000, PageLoads: 400, JSErrors: 10, Requests: 2000, FailedRequests: 2, PageLoadP95Ms: 800}
	for _, tc := range []struct {
		name     string
		after    BrowserStats
		failOn   []string
		verdict  Verdict
		findings []string
	}{
		{"steady passes on page views alone", steady, nil, Pass, nil},
		{"js errors per view rose: warns by default",
			BrowserStats{PageViews: 1000, PageLoads: 400, JSErrors: 200, Requests: 2000, FailedRequests: 2, PageLoadP95Ms: 800},
			nil, Pass, []string{release.HealthBrowserErrors}},
		{"failed requests rose", BrowserStats{PageViews: 1000, PageLoads: 400, JSErrors: 10, Requests: 2000, FailedRequests: 200, PageLoadP95Ms: 800},
			nil, Pass, []string{release.HealthBrowserFailedRequests}},
		{"page load slowed", BrowserStats{PageViews: 1000, PageLoads: 400, JSErrors: 10, Requests: 2000, FailedRequests: 2, PageLoadP95Ms: 2400},
			nil, Pass, []string{release.HealthBrowserPageLoad}},
		{"fail_on makes a browser finding fail", BrowserStats{PageViews: 1000, PageLoads: 400, JSErrors: 200, Requests: 2000, FailedRequests: 2, PageLoadP95Ms: 800},
			[]string{release.HealthBrowserErrors}, Fail, []string{release.HealthBrowserErrors}},
		{"under min_requests page views is not judged", BrowserStats{PageViews: 10, JSErrors: 10}, nil, Inconclusive, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := input(t, true, browserOnly(steady, tc.after)...)
			if tc.failOn != nil {
				in.Thresholds.FailOn = tc.failOn
			}
			r := Evaluate(in)
			var codes []string
			for _, f := range r.Findings {
				codes = append(codes, f.Code)
			}
			if r.Verdict != tc.verdict || !slices.Equal(codes, tc.findings) {
				t.Errorf("verdict %s findings %v, want %s %v (%+v)", r.Verdict, codes, tc.verdict, tc.findings, r.Findings)
			}
			if r.NoRequestTelemetry() {
				t.Error("a frontend with browser telemetry reads as having no request telemetry")
			}
		})
	}
}

// The new codes are findings fail_on may name, and the browser findings warn
// unless it does.
func TestBrowserCodesAreFindingsThatWarnByDefault(t *testing.T) {
	for _, c := range []string{release.HealthBrowserErrors, release.HealthBrowserFailedRequests, release.HealthBrowserPageLoad} {
		if !slices.Contains(release.HealthFindings, c) {
			t.Errorf("%s is not in release.HealthFindings", c)
		}
		if slices.Contains(defaults(t).FailOn, c) {
			t.Errorf("%s fails by default: a new signal must warn until an env opts in", c)
		}
	}
	if _, err := ParseFailOn("browser_errors,error_rate"); err != nil {
		t.Errorf("--fail-on browser_errors: %v", err)
	}
}

// obsquery.BrowserFigures and BrowserStats are one shape: a caller converts.
func TestBrowserStatsConvertsFromObsquery(t *testing.T) {
	f := obsquery.BrowserFigures{PageViews: 1, PageLoads: 2, JSErrors: 3, Requests: 4, FailedRequests: 5, PageLoadP95Ms: 6}
	if got := BrowserStats(f); got != (BrowserStats{1, 2, 3, 4, 5, 6}) {
		t.Fatalf("BrowserStats(%+v) = %+v", f, got)
	}
}

// Page views prove a frontend-only app good, but never stand in for a backend
// that saw requests too few to judge: a busy frontend beside a quiet API is
// low traffic, not a pass.
func TestBrowserViewsDoNotMaskAnUnderThresholdBackend(t *testing.T) {
	views := map[string]BrowserHealth{"web": {Before: BrowserStats{PageViews: 500}, After: BrowserStats{PageViews: 500}}}

	alone := Evaluate(input(t, true, Combine(nil, nil, views)...))
	if alone.Verdict != Pass {
		t.Fatalf("a frontend alone with 500 views = %s, want PASS", alone.Verdict)
	}

	quietAPI := []ServiceHealth{svc("api", 5, 5, 0, 0, 20, 20)}
	mixed := Evaluate(input(t, true, Combine(quietAPI, nil, views)...))
	if mixed.Verdict != Inconclusive {
		t.Fatalf("500 views beside an api with 5 requests = %s, want INCONCLUSIVE", mixed.Verdict)
	}
	quietSite := []ServiceHealth{svc("site", 5, 5, 0, 0, 20, 20)}
	if r := Evaluate(input(t, true, Combine(nil, quietSite, views)...)); r.Verdict != Inconclusive {
		t.Fatalf("500 views beside a site with 5 ingress requests = %s, want INCONCLUSIVE", r.Verdict)
	}

	busyAPI := []ServiceHealth{svc("api", 500, 500, 0, 0, 20, 20)}
	if r := Evaluate(input(t, true, Combine(busyAPI, nil, views)...)); r.Verdict != Pass {
		t.Fatalf("a judged api beside the frontend = %s, want PASS", r.Verdict)
	}
}
