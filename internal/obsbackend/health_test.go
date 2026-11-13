package obsbackend

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/obsquery"
	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/releasehealth"
)

const healthWire = `{
 "promotion":{"id":"p1","releaseVersion":"v2","promotedAt":"2026-10-22T11:40:00Z"},
 "before":{"start":"2026-10-22T11:25:00Z","end":"2026-10-22T11:40:00Z"},
 "after":{"start":"2026-10-22T11:40:00Z","end":"2026-10-22T11:55:00Z"},
 "afterComplete":true,
 "services":[{"service":"api","before":{"requests":"900"},"after":{"requests":"1000","errorRate":0.05},"versionShare":0.5}],
 "firingAlerts":[{"rule":"HighErrors","firingAt":"2026-10-22T11:45:00Z"}],
 "workloadFindings":[{"workload":"api","restarts":"3","lastTerminatedReason":"OOMKilled"}],
 "verdict":"RELEASE_VERDICT_FAIL",
 "findings":[{"code":"RELEASE_FINDING_CODE_ALERT_FIRING","policy":"fail","message":"m"},{"code":"RELEASE_FINDING_CODE_UNSPECIFIED"}],
 "thresholds":[{"name":"min_requests","value":"50","source":"release_shape"}]
}`

func TestCloudReleaseHealthMapsTheWireAndOmitsAnUnsetWindow(t *testing.T) {
	f := &fakeCaller{replies: map[string]string{procGetReleaseHealth: healthWire}}
	res, err := cloudWith(f).ReleaseHealth(context.Background(), HealthRequest{Release: "v2", Now: t0c})
	if err != nil {
		t.Fatal(err)
	}
	req := f.reqs[procGetReleaseHealth]
	if req["releaseVersion"] != "v2" || req["promotionId"] != nil {
		t.Errorf("subject: %v", req)
	}
	if _, set := req["window"]; set {
		t.Errorf("an unset --window must not override the env's declared window: %v", req["window"])
	}
	if _, set := req["thresholdOverrides"]; set {
		t.Errorf("no overrides were given: %v", req)
	}
	if scope := req["scope"].(map[string]any); scope["orgId"] != "org-1" || scope["envIds"].([]any)[0] != "env-1" {
		t.Errorf("scope: %v", scope)
	}

	r := res.Report
	if r.Verdict != releasehealth.Fail || !r.AfterComplete || r.Subject.Release != "v2" {
		t.Errorf("report: %+v", r)
	}
	if len(r.Findings) != 1 || r.Findings[0].Code != release.HealthAlertFiring {
		t.Errorf("an unspecified code is dropped: %+v", r.Findings)
	}
	if r.Services[0].After.Requests != 1000 || r.Services[0].VersionShare == nil || *r.Services[0].VersionShare != 0.5 {
		t.Errorf("services: %+v", r.Services)
	}
	if len(r.Workloads) != 1 || r.Workloads[0].Restarts != 3 || len(r.FiringAlerts) != 1 {
		t.Errorf("workloads %+v alerts %+v", r.Workloads, r.FiringAlerts)
	}
	if r.Thresholds[0].Source != releasehealth.SourceReleaseShape {
		t.Errorf("thresholds: %+v", r.Thresholds)
	}
}

func TestCloudReleaseHealthSendsWindowPromotionAndOverrides(t *testing.T) {
	f := &fakeCaller{replies: map[string]string{procGetReleaseHealth: healthWire}}
	_, err := cloudWith(f).ReleaseHealth(context.Background(), HealthRequest{
		PromotionID: "p9", Now: t0c,
		Options: releasehealth.Options{Window: 30 * time.Minute, Overrides: map[string]string{"min_requests": "10"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := f.reqs[procGetReleaseHealth]
	if req["window"] != "1800s" || req["promotionId"] != "p9" || req["thresholdOverrides"].(map[string]any)["min_requests"] != "10" {
		t.Errorf("request: %v", req)
	}
}

func TestCloudReleaseHealthErrorMapping(t *testing.T) {
	for name, tc := range map[string]struct {
		code string
		exit int
		msg  string
	}{
		"unimplemented":    {cloud.CodeUnimplemented, ExitUndetermined, "does not serve release health"},
		"unavailable":      {cloud.CodeUnavailable, ExitUndetermined, ""},
		"unauthenticated":  {cloud.CodeUnauthenticated, ExitUndetermined, ""},
		"exhausted":        {cloud.CodeResourceExhausted, ExitUndetermined, ""},
		"invalid argument": {cloud.CodeInvalidArgument, ExitWrong, ""},
		"not found":        {cloud.CodeNotFound, ExitWrong, ""},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeCaller{errs: map[string]error{procGetReleaseHealth: &cloud.Error{Code: tc.code}}}
			_, err := cloudWith(f).ReleaseHealth(context.Background(), HealthRequest{Now: t0c})
			be, ok := err.(*Error)
			if !ok || be.Code != tc.exit || !strings.Contains(err.Error(), tc.msg) {
				t.Errorf("err = %v (%T), want exit %d containing %q", err, err, tc.exit, tc.msg)
			}
		})
	}
}

func TestCloudReleaseHealthRefusesAnUnreadableVerdict(t *testing.T) {
	var m map[string]any
	_ = json.Unmarshal([]byte(healthWire), &m)
	m["verdict"] = "RELEASE_VERDICT_UNSPECIFIED"
	b, _ := json.Marshal(m)
	f := &fakeCaller{replies: map[string]string{procGetReleaseHealth: string(b)}}
	_, err := cloudWith(f).ReleaseHealth(context.Background(), HealthRequest{Now: t0c})
	if be, ok := err.(*Error); !ok || be.Code != ExitUndetermined {
		t.Errorf("a verdict-less answer is never a pass: %v", err)
	}
}

func TestLocalReleaseHealthNeedsAnInstantAndLabelsWhatItSkipped(t *testing.T) {
	l := Local{Env: "dev", Runner: &fakeRunner{}}
	if _, err := l.ReleaseHealth(context.Background(), HealthRequest{Now: t0c}); err == nil || err.(*Error).Code != ExitWrong {
		t.Errorf("no instant: %v", err)
	}
	if _, err := l.ReleaseHealth(context.Background(), HealthRequest{PromotedAt: t0c.Add(time.Hour), Now: t0c}); err == nil || err.(*Error).Code != ExitWrong {
		t.Errorf("future instant: %v", err)
	}
	res, err := l.ReleaseHealth(context.Background(), HealthRequest{PromotedAt: t0c.Add(-20 * time.Minute), Now: t0c})
	if err != nil {
		t.Fatal(err)
	}
	if res.Report.Verdict != releasehealth.Inconclusive {
		t.Errorf("no traffic is not a pass: %s", res.Report.Verdict)
	}
	for _, want := range []string{release.HealthRegressedIssue, release.HealthAlertFiring, release.HealthWorkloadRestarts} {
		found := false
		for _, n := range res.Report.NotEvaluated {
			found = found || n == want
		}
		if !found {
			t.Errorf("not_evaluated lacks %s: %v", want, res.Report.NotEvaluated)
		}
	}
	if len(res.Statements) != 7 {
		t.Errorf("statements = %d, want RED, RED by endpoint and browser RED per window, plus issues", len(res.Statements))
	}
	if bad := (releasehealth.Options{Overrides: map[string]string{"min_requests": "x"}}); true {
		if _, err := l.ReleaseHealth(context.Background(), HealthRequest{PromotedAt: t0c.Add(-20 * time.Minute), Now: t0c, Options: bad}); err == nil || err.(*Error).Code != ExitWrong {
			t.Errorf("bad threshold: %v", err)
		}
	}
}

// A frontend with no server spans is judged on its browser telemetry: enough
// page views prove it, and a JavaScript error spike is a (warning) finding.
func TestLocalReleaseHealthJudgesAFrontendOnBrowserTelemetry(t *testing.T) {
	browser := func(errs string) Result {
		return Result{Rows: [][]any{{"web", num64("500"), num64("200"), num64(errs), num64("1000"), num64("1"), []any{num64("300"), num64("900"), num64("1200")}}}}
	}
	// RED, RED by endpoint (both windows), issues, then browser RED before and after.
	l := Local{Env: "dev", Runner: &seqRunner{results: []Result{{}, {}, {}, {}, {}, browser("5"), browser("200")}}}
	res, err := l.ReleaseHealth(context.Background(), HealthRequest{PromotedAt: t0c.Add(-20 * time.Minute), Now: t0c})
	if err != nil {
		t.Fatal(err)
	}
	r := res.Report
	if r.Verdict != releasehealth.Pass || r.NoRequestTelemetry() || len(r.Services) != 1 || r.Services[0].Signal != releasehealth.SignalBrowser {
		t.Fatalf("verdict %s services %+v", r.Verdict, r.Services)
	}
	if b := r.Services[0].Browser; b == nil || b.After.JSErrors != 200 || b.Before.PageLoadP95Ms != 900 {
		t.Fatalf("browser figures = %+v", b)
	}
	if len(r.Findings) != 1 || r.Findings[0].Code != release.HealthBrowserErrors || r.Findings[0].Policy != releasehealth.PolicyWarn {
		t.Fatalf("findings = %+v, want one browser_errors warning", r.Findings)
	}
}

// seqRunner answers the n-th statement with results[n].
type seqRunner struct{ results []Result }

func (s *seqRunner) Run(_ context.Context, _ obsquery.Stmt, _ Limits) (Result, error) {
	if len(s.results) == 0 {
		return Result{}, nil
	}
	r := s.results[0]
	s.results = s.results[1:]
	return r, nil
}

// An endpoint set cut at obsquery.MaxREDEndpoints is not judged as whole: the
// surviving endpoints say nothing about the omitted ones, so latency falls
// back to the service-level p95, which here regressed.
func TestLocalReleaseHealthJudgesTheServiceWhenTheEndpointSetIsTruncated(t *testing.T) {
	red := func(p95 string) Result {
		return Result{Rows: [][]any{{"api", num64("1000"), num64("0"), []any{num64("1"), num64(p95), num64(p95)}, ""}}}
	}
	var eps [][]any
	for i := 0; i <= obsquery.MaxREDEndpoints; i++ {
		eps = append(eps, []any{"api", fmt.Sprintf("GET /e%d", i), num64("1"), num64("0"), []any{num64("1"), num64("10"), num64("10")}})
	}
	// RED before, RED after, by endpoint before, by endpoint after, issues.
	l := Local{Env: "dev", Runner: &seqRunner{results: []Result{red("10"), red("50"), {Rows: eps}, {Rows: eps}, {}}}}
	res, err := l.ReleaseHealth(context.Background(), HealthRequest{PromotedAt: t0c.Add(-20 * time.Minute), Now: t0c})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Report.Services) != 1 || len(res.Report.Services[0].Endpoints) != 0 {
		t.Fatalf("a truncated endpoint set must not be attached: %+v", res.Report.Services)
	}
	found := false
	for _, f := range res.Report.Findings {
		found = found || f.Code == release.HealthLatency
	}
	if !found {
		t.Errorf("the service-level p95 regression is judged: %+v", res.Report.Findings)
	}
}

func TestChangeFeedOrderAndLimit(t *testing.T) {
	res := ChangesResult{Changes: []Change{
		{Kind: ChangeSession, At: t0c},
		{Kind: ChangeActivity, At: t0c.Add(2 * time.Hour)},
		{Kind: ChangePromotion, At: t0c.Add(time.Hour)},
	}}
	sortChanges(&res, 2)
	if res.Changes[0].Kind != ChangeActivity || res.Changes[1].Kind != ChangePromotion || res.Omitted != 1 {
		t.Errorf("feed: %+v omitted %d", res.Changes, res.Omitted)
	}
}

func TestLocalChangesWithoutALedgerSaysSo(t *testing.T) {
	_, err := Local{Env: "dev"}.Changes(context.Background(), ChangesRequest{Env: "dev"})
	if be, ok := err.(*Error); !ok || be.Code != ExitUndetermined {
		t.Errorf("err = %v", err)
	}
}
