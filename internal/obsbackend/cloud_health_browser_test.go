package obsbackend

import (
	"encoding/json"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/releasehealth"
)

// siteAnswer is a control plane's GetReleaseHealth answer for a frontend-only
// env (a hosted static site): no server spans, only its pages' browser
// figures (control-plane#2752), protojson-encoded as the wire carries it: the
// field names are control-plane's ServiceHealthDelta / BrowserHealthDelta /
// BrowserWindowStats as merged in control-plane#2955 (08eb56fa), int64s as
// JSON strings.
func siteAnswer(t *testing.T, verdict string, before, after string, findings string) wireReleaseHealth {
	t.Helper()
	var w wireReleaseHealth
	body := `{"promotion":{"id":"p2","releaseVersion":"v2","promotedAt":"2026-11-18T12:30:00Z","previousReleaseVersion":"v1"},
		"before":{"start":"2026-11-18T12:15:00Z","end":"2026-11-18T12:30:00Z"},"after":{"start":"2026-11-18T12:30:00Z","end":"2026-11-18T12:45:00Z"},
		"afterComplete":true,"verdict":"` + verdict + `",
		"services":[{"service":"landing-web","before":{},"after":{},"signal":"browser","browser":{"before":` + before + `,"after":` + after + `}}],
		"findings":[` + findings + `],
		"thresholds":[{"name":"min_requests","value":"50","source":"default"},{"name":"fail_on","value":"alert_firing,error_rate,new_issue,probe_failed,regressed_issue","source":"default"}]}`
	if err := json.Unmarshal([]byte(body), &w); err != nil {
		t.Fatal(err)
	}
	return w
}

const healthyPages = `{"pageViews":"120","pageLoads":"100","jsErrors":"1","requests":"300","failedRequests":"1","pageLoadP95Ms":900}`

// A frontend's browser figures reach the report, so the folds `forge obs
// check` applies to a control plane's answer (the baseline rule) judge the
// site on its page views rather than as a service that sent nothing.
func TestReportFromWire_CarriesBrowserFigures(t *testing.T) {
	r, err := reportFromWire(siteAnswer(t, "RELEASE_VERDICT_PASS", healthyPages, healthyPages, ""))
	if err != nil {
		t.Fatal(err)
	}
	s := r.Services[0]
	want := releasehealth.BrowserStats{PageViews: 120, PageLoads: 100, JSErrors: 1, Requests: 300, FailedRequests: 1, PageLoadP95Ms: 900}
	if s.Signal != releasehealth.SignalBrowser || s.Browser == nil || s.Browser.Before != want || s.Browser.After != want {
		t.Fatalf("service = %+v (browser %+v), want landing-web judged on %+v in both windows", s, s.Browser, want)
	}
	if got := releasehealth.WithThinBaselines(r); got.Verdict != releasehealth.Pass {
		t.Fatalf("after the baseline rule: %s (%s), want the control plane's PASS to stand", got.Verdict, releasehealth.InconclusiveWhy(got))
	}
}

// Below min_requests page views the site is low traffic, not "no request
// telemetry": its pages did report.
func TestReportFromWire_QuietSiteIsLowTraffic(t *testing.T) {
	quiet := `{"pageViews":"20","pageLoads":"20","requests":"40"}`
	r, err := reportFromWire(siteAnswer(t, "RELEASE_VERDICT_INCONCLUSIVE", quiet, quiet, ""))
	if err != nil {
		t.Fatal(err)
	}
	r = releasehealth.WithThinBaselines(r)
	if r.Verdict != releasehealth.Inconclusive || releasehealth.InconclusiveCode(r) != releasehealth.ReasonLowTraffic {
		t.Fatalf("verdict=%s code=%s, want INCONCLUSIVE low_traffic", r.Verdict, releasehealth.InconclusiveCode(r))
	}
}

// --fail-on reaches a browser finding the env's policy only warned about.
func TestReportFromWire_BrowserFindingRejudged(t *testing.T) {
	bad := `{"pageViews":"120","pageLoads":"100","jsErrors":"40","requests":"300","failedRequests":"30","pageLoadP95Ms":900}`
	finding := `{"code":"RELEASE_FINDING_CODE_BROWSER_ERRORS","service":"landing-web","value":0.325,"threshold":0.01,"policy":"warn","message":"landing-web: JavaScript errors per page view"}`
	r, err := reportFromWire(siteAnswer(t, "RELEASE_VERDICT_PASS", healthyPages, bad, finding))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Findings) != 1 || r.Findings[0].Code != release.HealthBrowserErrors {
		t.Fatalf("findings = %+v, want browser_errors", r.Findings)
	}
	if got := releasehealth.Rejudge(r, []string{release.HealthBrowserErrors}); got.Verdict != releasehealth.Fail {
		t.Fatalf("--fail-on browser_errors: %s, want FAIL", got.Verdict)
	}
}

// An older control plane sends no browser figures; its answer reads as before.
func TestReportFromWire_OlderControlPlaneSendsNoBrowser(t *testing.T) {
	var w wireReleaseHealth
	if err := json.Unmarshal([]byte(`{"verdict":"RELEASE_VERDICT_PASS","services":[{"service":"api","before":{"requests":"100"},"after":{"requests":"100"}}]}`), &w); err != nil {
		t.Fatal(err)
	}
	r, err := reportFromWire(w)
	if err != nil {
		t.Fatal(err)
	}
	if s := r.Services[0]; s.Browser != nil || s.Signal != "" || s.After.Requests != 100 {
		t.Fatalf("service = %+v", s)
	}
}
