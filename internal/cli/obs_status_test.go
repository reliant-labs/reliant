package cli

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/ledgerfile"
	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/pkg/release"
)

const (
	procOverview   = "controlplane.v1.ObservabilityService/GetServiceOverview"
	procAlerts     = "controlplane.v1.AlertingService/ListAlerts"
	procDeliveries = "controlplane.v1.TelemetryStatusService/ListTelemetryDeliveries"
	procSearchLogs = "controlplane.v1.ObservabilityService/SearchLogs"
	procTraces     = "controlplane.v1.ObservabilityService/ListTraces"
)

// statusCaller answers every read of `forge obs status prod`.
func statusCaller() *seqCaller {
	c := changesCaller()
	for proc, reply := range map[string]string{
		procOverview: `{"services":[
			{"service":"worker","requestCount":"0","errorLogCount":"2"},
			{"service":"api","requestCount":"3600","ratePerSecond":1,"errorCount":"36","errorRate":0.01,"p50Ms":12,"p95Ms":80,"p99Ms":200,"errorLogCount":"40"}],
			"trendStepSeconds":30}`,
		cloudList: `{"issues":[` + cloudIssueJSON + `]}`,
		procAlerts: `{"alerts":[
			{"envId":"env-prod","rule":"SlowP95","state":"pending","activeAt":"2026-10-22T11:58:00Z","lastValue":900},
			{"envId":"env-prod","rule":"HighErrorRate","fingerprint":"f1","labels":{"service":"api"},"annotations":{"summary":"api error rate above 1%"},
			 "state":"firing","activeAt":"2026-10-22T11:40:00Z","firingAt":"2026-10-22T11:45:00Z","lastValue":0.02}]}`,
		procDeliveries: `{"enabled":true,"deliveries":[{"environmentId":"env-prod","cluster":"gke-prod","namespace":"shop","name":"reliant-telemetry",
			"state":"DELIVERY_STATE_FAILING","expiresAt":"2026-10-23T12:00:00Z","lastError":"forbidden","consecutiveFailures":3}]}`,
		procSearchLogs: `{"logs":[{"timestamp":"2026-10-22T11:59:00Z","severityText":"INFO","service":"api","body":"ok"}]}`,
		procTraces:     `{"traces":[{"traceId":"t1","startTime":"2026-10-22T11:59:30Z","durationMs":12}]}`,
	} {
		c.replies[proc] = []string{reply}
	}
	return c
}

func callTo(calls []fakeCloudCall, proc string) *fakeCloudCall {
	for i := range calls {
		if calls[i].procedure == proc {
			return &calls[i]
		}
	}
	return nil
}

func TestObsStatusCloudReadsEverySection(t *testing.T) {
	caller := statusCaller()
	out, errOut, err := newObsCheckHarness().run(t, cloudFacts(), caller, nil, "status", "prod", "--json")
	if err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	obsGolden(t, "status-cloud.json", out)
	doc := decode(t, out)
	if doc["schema"] != "forge.obs.status/v1" || doc["ok"] != true || doc["unread"] != nil || doc["warnings"] != nil {
		t.Errorf("head: schema=%v ok=%v unread=%v warnings=%v", doc["schema"], doc["ok"], doc["unread"], doc["warnings"])
	}
	svcs := doc["services"].([]any)
	if len(svcs) != 2 || svcs[1].(map[string]any)["service"] != "api" || svcs[1].(map[string]any)["error_logs"] != float64(40) {
		t.Errorf("services keep the control plane's order and figures: %v", svcs)
	}
	if is := doc["issues"].([]any); len(is) != 1 || is[0].(map[string]any)["id"] != "ISS-7f3a" {
		t.Errorf("issues = %v", is)
	}
	alerts := doc["alerts"].([]any)
	if len(alerts) != 2 || alerts[0].(map[string]any)["rule"] != "HighErrorRate" || alerts[0].(map[string]any)["summary"] != "api error rate above 1%" {
		t.Errorf("firing alerts lead: %v", alerts)
	}
	tel := doc["telemetry"].(map[string]any)
	if tel["last_log_at"] != "2026-10-22T11:59:00Z" || tel["last_span_at"] != "2026-10-22T11:59:30Z" || tel["deliveries_enabled"] != true {
		t.Errorf("telemetry = %v", tel)
	}
	if dl := tel["deliveries"].([]any); dl[0].(map[string]any)["state"] != "failing" {
		t.Errorf("deliveries = %v", dl)
	}
	// p2 (v2) is in force; its convergence failed, so the rollout is unsettled.
	cur := doc["current_release"].(map[string]any)
	if cur["version"] != "v2" || cur["promotion_id"] != "p2" || cur["unsettled"] != true {
		t.Errorf("current_release = %v", cur)
	}
	if ch := doc["changes"].([]any); len(ch) != 4 {
		t.Errorf("changes = %v", ch)
	}
	sug := fmt.Sprint(doc["suggested"])
	for _, want := range []string{"forge obs issue ISS-7f3a prod", "forge obs endpoints prod --service api --sort errors", "forge obs check prod --promotion p2"} {
		if !strings.Contains(sug, want) {
			t.Errorf("suggested lacks %q: %s", want, sug)
		}
	}

	if c := callTo(caller.calls, cloudList); c == nil || !strings.Contains(c.body, `"statuses":["ISSUE_STATUS_NEW","ISSUE_STATUS_REGRESSED"]`) {
		t.Errorf("issues request: %+v", c)
	}
	if c := callTo(caller.calls, procAlerts); c == nil || c.body != `{"envId":"env-prod","orgId":"org-1"}` {
		t.Errorf("alerts request: %+v", c)
	}
	if c := callTo(caller.calls, procDeliveries); c == nil || c.body != `{"environmentId":"env-prod","orgId":"org-1"}` {
		t.Errorf("deliveries request: %+v", c)
	}
	if c := callTo(caller.calls, procOverview); c == nil || !strings.Contains(c.body, `"timeRange":{"start":"2026-10-22T11:00:00Z","end":"2026-10-22T12:00:00Z"}`) {
		t.Errorf("the overview reads --since (1h): %+v", c)
	}
	if c := callTo(caller.calls, procSearchLogs); c == nil || !strings.Contains(c.body, `"start":"2026-10-22T11:45:00Z"`) || !strings.Contains(c.body, `"pageSize":1`) {
		t.Errorf("freshness probes the last 15m for one record first: %+v", c)
	}
}

func TestObsStatusText(t *testing.T) {
	out, _, err := newObsCheckHarness().run(t, cloudFacts(), statusCaller(), nil, "status", "prod")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"prod (cloud backend)",
		"release v2, promoted 30m ago by ci (rollout not reported finished)",
		"telemetry: last log 1m ago, last span 30s ago",
		"telemetry token shop/reliant-telemetry on gke-prod: failing (forbidden)",
		"SERVICE", "api", "ISS-7f3a", "FIRING   HighErrorRate: api error rate above 1%", "PENDING  SlowP95",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text lacks %q:\n%s", want, out)
		}
	}
}

// A section the control plane cannot answer is named and explained; the rest
// is still shown, but the partial read exits 2 instead of reporting success.
func TestObsStatusDegradesSectionBySection(t *testing.T) {
	caller := statusCaller()
	caller.errs = map[string]error{
		procAlerts:     &cloud.Error{Code: cloud.CodeUnimplemented},
		procDeliveries: &cloud.Error{Code: cloud.CodePermissionDenied, Message: "machine credentials are refused"},
		procTraces:     &cloud.Error{Code: cloud.CodeUnavailable},
	}
	out, errOut, err := newObsCheckHarness().run(t, cloudFacts(), caller, nil, "status", "prod", "--json")
	if exitCodeForError(err) != 2 {
		t.Fatalf("a partial read must exit 2: %v", err)
	}
	doc := decode(t, out)
	if doc["ok"] != false || doc["exit_code"] != float64(2) || !strings.Contains(fmt.Sprint(doc["error"]), "partial") {
		t.Errorf("partial status must not look healthy: %v", doc)
	}
	if got := fmt.Sprint(doc["unread"]); got != "[alerts telemetry.deliveries]" {
		t.Errorf("unread = %s", got)
	}
	if _, ok := doc["alerts"]; ok {
		t.Error("an unread section is absent, not empty")
	}
	w := fmt.Sprint(doc["warnings"])
	for _, want := range []string{"alerts: the control plane does not serve alerts yet", "telemetry.deliveries:", "machine credentials are refused", "the newest span is unknown"} {
		if !strings.Contains(w, want) {
			t.Errorf("warnings lack %q: %s", want, w)
		}
	}
	tel := doc["telemetry"].(map[string]any)
	if tel["last_log_at"] == nil || tel["last_span_at"] != nil || tel["deliveries"] != nil {
		t.Errorf("logs alone answer freshness: %v", tel)
	}
	if doc["services"] == nil || doc["issues"] == nil || doc["current_release"] == nil {
		t.Errorf("the readable sections are still there: %v", doc)
	}
	if errOut != "" {
		t.Errorf("--json writes no warnings to stderr: %q", errOut)
	}
}

func TestObsStatusNestedUnreadEvidenceIsPartial(t *testing.T) {
	for _, proc := range []string{procTraces, procActivity} {
		t.Run(proc, func(t *testing.T) {
			caller := statusCaller()
			caller.errs = map[string]error{proc: &cloud.Error{Code: cloud.CodeUnavailable}}
			out, _, err := newObsCheckHarness().run(t, cloudFacts(), caller, nil, "status", "prod", "--json")
			if exitCodeForError(err) != 2 {
				t.Fatalf("nested partial read must exit 2: %v\n%s", err, out)
			}
			doc := decode(t, out)
			if doc["ok"] != false || doc["exit_code"] != float64(2) || doc["warnings"] == nil || doc["current_release"] == nil || doc["services"] == nil {
				t.Errorf("partial status must preserve the readable evidence: %v", doc)
			}
		})
	}
}

func TestObsStatusNothingReadableIsExit2(t *testing.T) {
	caller := statusCaller()
	caller.errs = map[string]error{}
	for proc := range caller.replies {
		caller.errs[proc] = &cloud.Error{Code: cloud.CodeUnavailable}
	}
	out, _, err := newObsCheckHarness().run(t, cloudFacts(), caller, nil, "status", "prod", "--json")
	if exitCodeForError(err) != 2 {
		t.Fatalf("exit = %d (%v)", exitCodeForError(err), err)
	}
	doc := decode(t, out)
	if doc["ok"] != false || doc["exit_code"] != float64(2) || !strings.Contains(fmt.Sprint(doc["error"]), "nothing about prod could be read") {
		t.Errorf("failure document: %v", doc)
	}
}

// An empty section that was read is an empty list: "none", not "unknown".
func TestObsStatusEmptySectionsArePresent(t *testing.T) {
	caller := statusCaller()
	caller.replies[procOverview] = []string{`{"services":[]}`}
	caller.replies[cloudList] = []string{`{}`}
	caller.replies[procAlerts] = []string{`{}`}
	out, _, err := newObsCheckHarness().run(t, cloudFacts(), caller, nil, "status", "prod", "--json")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{`"services": []`, `"issues": []`, `"alerts": []`} {
		if !strings.Contains(out, k) {
			t.Errorf("document lacks %s:\n%s", k, out)
		}
	}
	text, _, err := newObsCheckHarness().run(t, cloudFacts(), caller, nil, "status", "prod")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"no service traffic in the range", "no new or regressed issues", "no alerts firing"} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
}

func TestObsStatusLocal(t *testing.T) {
	store, err := ledgerfile.Open(t.TempDir(), "proj")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReportSession(release.LocalSession{ID: "s1", Env: "dev", Worktree: release.Worktree{Label: "main", Host: "h1"},
		StartedAt: obsNow.Add(-20 * time.Minute), LastSeenAt: obsNow}); err != nil {
		t.Fatal(err)
	}
	sr := &scriptedRunner{answers: []scriptedAnswer{
		{"AS error_logs", obsbackend.Result{Rows: [][]any{{"api", jn("50"), jn("4"), ""}, {"cron", jn("3"), jn("1"), ""}}}},
		issuesAnswer(),
		{"AS requests", obsbackend.Result{Rows: [][]any{redRow("api", 720, 18, 95)}}},
		{"ORDER BY start DESC", obsbackend.Result{Rows: [][]any{{"t9", "2026-10-22 11:58:00.000000000", 5.0, "api", "GET /", 5.0, jn("1"), jn("0"), 1.0, []any{"api"}, ""}}}},
		logsAnswer(),
	}}
	h := newObsCheckHarness()
	h.ledger = store
	out, errOut, err := h.run(t, devFacts(), nil, sr, "status", "dev", "--json")
	if err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	doc := decode(t, out)
	if doc["scope"].(map[string]any)["backend"] != "local" {
		t.Errorf("scope = %v", doc["scope"])
	}
	svcs := doc["services"].([]any)
	api := svcs[0].(map[string]any)
	if len(svcs) != 2 || api["service"] != "api" || api["requests"] != float64(720) || api["error_rate"] != 0.025 ||
		api["rate_per_second"] != 0.2 || api["error_logs"] != float64(4) {
		t.Errorf("services merge the RED and log figures, busiest first: %v", svcs)
	}
	if cron := svcs[1].(map[string]any); cron["service"] != "cron" || cron["requests"] != float64(0) || cron["error_logs"] != float64(1) {
		t.Errorf("a service that only logged is listed: %v", cron)
	}
	if is := doc["issues"].([]any); len(is) != 2 {
		t.Errorf("locally every error group in the range is listed: %v", is)
	}
	if got := fmt.Sprint(doc["not_evaluated"]); got != "[alerts]" {
		t.Errorf("not_evaluated = %s", got)
	}
	if doc["unread"] != nil {
		t.Errorf("unread = %v, warnings = %v", doc["unread"], doc["warnings"])
	}
	tel := doc["telemetry"].(map[string]any)
	if tel["last_log_at"] != "2026-10-22T11:59:50Z" || tel["last_span_at"] != "2026-10-22T11:58:00Z" || tel["deliveries_enabled"] != nil {
		t.Errorf("telemetry = %v", tel)
	}
	if _, ok := doc["current_release"]; ok {
		t.Error("a local env with no promotion has no release in force")
	}
	if ch := doc["changes"].([]any); len(ch) != 1 || ch[0].(map[string]any)["kind"] != "session" {
		t.Errorf("changes = %v", ch)
	}
}

func TestObsStatusFieldsAndFlags(t *testing.T) {
	out, _, err := newObsCheckHarness().run(t, cloudFacts(), statusCaller(), nil, "status", "prod", "--json", "--fields", "services.service,current_release.version")
	if err != nil {
		t.Fatal(err)
	}
	doc := decode(t, out)
	if _, ok := doc["issues"]; ok || doc["current_release"].(map[string]any)["version"] != "v2" {
		t.Errorf("projection: %v", doc)
	}
	if _, _, err := newObsCheckHarness().run(t, cloudFacts(), statusCaller(), nil, "status", "prod", "--json", "--fields", "servics"); err == nil || !strings.Contains(err.Error(), "services") {
		t.Errorf("a typo lists the real fields: %v", err)
	}
	if _, _, err := newObsCheckHarness().run(t, cloudFacts(), statusCaller(), nil, "status", "prod", "--since", "-1h"); exitCodeForError(err) != 1 {
		t.Errorf("a bad range is a wrong request: %v", err)
	}
}

// The newest record is looked for in the cheap 15m probe first, and over the
// whole 24h lookback only when the probe found nothing or failed: prod's
// ListTraces timed out sorting a day of spans, so every status was partial.
func TestStatusFreshnessProbesBeforeTheWholeLookback(t *testing.T) {
	fn := obsbackend.Narrow{Range: obsbackend.Range{From: obsNow.Add(-24 * time.Hour), To: obsNow}}
	for name, tc := range map[string]struct {
		probeHas  bool
		probeErr  error
		wantReads int
	}{
		"fresh":        {probeHas: true, wantReads: 1},
		"quiet 15m":    {wantReads: 2},
		"probe failed": {probeErr: errors.New("504"), wantReads: 2},
	} {
		t.Run(name, func(t *testing.T) {
			var windows []time.Duration
			at, err := newestWithin(fn, func(n obsbackend.Narrow) (time.Time, bool, error) {
				windows = append(windows, n.Range.To.Sub(n.Range.From))
				if len(windows) == 1 {
					return obsNow.Add(-time.Minute), tc.probeHas, tc.probeErr
				}
				return obsNow.Add(-time.Hour), true, nil
			})
			if err != nil || len(windows) != tc.wantReads || windows[0] != statusFreshnessProbe {
				t.Fatalf("reads %v err %v, want %d reads starting with the %s probe", windows, err, tc.wantReads, statusFreshnessProbe)
			}
			if tc.wantReads == 2 && (windows[1] != 24*time.Hour || !at.Equal(obsNow.Add(-time.Hour))) {
				t.Fatalf("widened read %v at %v, want 24h", windows, at)
			}
		})
	}
}

// The external delivery reconciler being off is reported only for an env that
// declared a Secret for it. A hosted env's own token is renewed by the
// platform's workload operator, so "nothing keeps the token valid" there was
// false (control-plane prod, 2026-10-29).
func TestStatusTelemetryDeliveryOffNeedsADeclaredSecret(t *testing.T) {
	off := false
	now := time.Date(2026, 10, 29, 16, 0, 0, 0, time.UTC)
	var hosted strings.Builder
	writeStatusTelemetry(&hosted, now, &obsStatusTelemetry{DeliveriesEnabled: &off})
	if strings.Contains(hosted.String(), "telemetry token") {
		t.Errorf("an env with no declared delivery got a token warning:\n%s", hosted.String())
	}

	var declared strings.Builder
	writeStatusTelemetry(&declared, now, &obsStatusTelemetry{DeliveriesEnabled: &off,
		Deliveries: []obsbackend.TelemetryDelivery{{Cluster: "prod", Namespace: "obs", Name: "reliant-otel-auth", State: "disabled"}}})
	if got := declared.String(); !strings.Contains(got, "1 declared Secret(s) on connected clusters are not delivered") ||
		!strings.Contains(got, "reconciler is off") || !strings.Contains(got, "obs/reliant-otel-auth on prod: disabled") {
		t.Errorf("a declared delivery with the reconciler off must say so:\n%s", got)
	}
}
