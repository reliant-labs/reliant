package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/ledgerfile"
	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/pkg/obsquery"
	"github.com/reliant-labs/forge/pkg/release"
)

const procHealth = "controlplane.v1.ObservabilityService/GetReleaseHealth"

func cloudFacts() obsbackend.Facts {
	return obsbackend.Facts{Env: "prod", Declared: true, Enabled: true, HasHosted: true, ControlPlane: "https://cp"}
}

// healthReply is a GetReleaseHealth answer (protojson) for promotion P=11:40.
func healthReply(verdict string, complete bool, findings, extra string) string {
	return fmt.Sprintf(`{
 "promotion":{"id":"promo-7","releaseVersion":"v2","gitCommit":"abc123","promotedAt":"2026-10-22T11:40:00Z","previousReleaseVersion":"v1"},
 "before":{"start":"2026-10-22T11:25:00Z","end":"2026-10-22T11:40:00Z"},
 "after":{"start":"2026-10-22T11:40:00Z","end":"2026-10-22T11:55:00Z"},
 "afterComplete":%t,
 "services":[{"service":"api","before":{"requests":"900","errorRate":0.001,"p95Ms":100},"after":{"requests":"1000","errorRate":0.05,"p95Ms":110},"versionShare":1}],
 %s
 "verdict":"%s",
 "findings":[%s],
 "thresholds":[{"name":"min_requests","value":"50","source":"default"},{"name":"fail_on","value":"error_rate,new_issue","source":"release_shape"}]
}`, complete, extra, verdict, findings)
}

const errorRateFinding = `{"code":"RELEASE_FINDING_CODE_ERROR_RATE","service":"api","value":0.049,"threshold":0.01,"policy":"fail","message":"api: error rate 0.1% -> 5.0%"}`
const latencyFinding = `{"code":"RELEASE_FINDING_CODE_LATENCY","service":"api","value":2,"threshold":1.5,"policy":"warn","message":"api: p95 doubled"}`

// seqCaller answers a procedure with its replies in turn (the last repeats)
// and records the requests.
type seqCaller struct {
	mu      sync.Mutex
	replies map[string][]string
	errs    map[string]error
	calls   []fakeCloudCall
	n       map[string]int
}

func (s *seqCaller) Call(ctx context.Context, procedure string, req, out any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.n == nil {
		s.n = map[string]int{}
	}
	fc := &fakeCloudCaller{replies: map[string]string{}, errs: s.errs}
	if rs := s.replies[procedure]; len(rs) > 0 {
		i := s.n[procedure]
		if i >= len(rs) {
			i = len(rs) - 1
		}
		fc.replies[procedure] = rs[i]
		s.n[procedure]++
	}
	err := fc.Call(ctx, procedure, req, out)
	s.calls = append(s.calls, fc.calls...)
	return err
}

// checkRun runs `forge obs <args>` against the cloud backend with caller, a
// clock the sleep advances, and the given local session start.
type obsCheckHarness struct {
	now    time.Time
	sleeps int
	ledger obsbackend.LedgerReader
}

func (e *obsCheckHarness) run(t *testing.T, facts obsbackend.Facts, caller obsbackend.Caller, local obsbackend.Runner, args ...string) (string, string, error) {
	t.Helper()
	var out, errb bytes.Buffer
	old, oldSleep := obsDepsOverride, obsCheckSleep
	t.Cleanup(func() { obsDepsOverride, obsCheckSleep = old, oldSleep })
	obsCheckSleep = func(_ context.Context, d time.Duration) error {
		e.now = e.now.Add(d)
		e.sleeps++
		return nil
	}
	obsDepsOverride = &obsDeps{
		Facts: func(context.Context, io.Writer, string) (obsbackend.Facts, error) { return facts, nil },
		Local: func(env string) obsbackend.Backend {
			return obsbackend.Local{Env: env, Runner: local, Ledger: func() (obsbackend.LedgerReader, error) { return e.ledger, nil }}
		},
		Cloud: func(_ context.Context, env string) (obsbackend.Cloud, error) {
			return obsbackend.Cloud{Env: env, EnvID: "env-prod", OrgID: "org-1", Caller: caller}, nil
		},
		Now: func() time.Time { return e.now },
	}
	cmd := newTestRootCmd(t)
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(append([]string{"obs"}, args...))
	err := cmd.ExecuteContext(context.Background())
	return out.String(), errb.String(), err
}

func newObsCheckHarness() *obsCheckHarness { return &obsCheckHarness{now: obsNow} }

func TestObsCheckExitMatrix(t *testing.T) {
	tests := []struct {
		name  string
		reply string
		err   error
		args  []string
		exit  int
		want  string // verdict in the document, "" when none
	}{
		{"PASS", healthReply("RELEASE_VERDICT_PASS", true, "", ""), nil, nil, 0, "PASS"},
		{"FAIL", healthReply("RELEASE_VERDICT_FAIL", true, errorRateFinding, ""), nil, nil, 1, "FAIL"},
		{"INCONCLUSIVE", healthReply("RELEASE_VERDICT_INCONCLUSIVE", true, "", ""), nil, nil, 2, "INCONCLUSIVE"},
		{"INCONCLUSIVE allowed", healthReply("RELEASE_VERDICT_INCONCLUSIVE", true, "", ""), nil, []string{"--allow-inconclusive"}, 0, "INCONCLUSIVE"},
		{"FAIL is not softened by --allow-inconclusive", healthReply("RELEASE_VERDICT_FAIL", true, errorRateFinding, ""), nil, []string{"--allow-inconclusive"}, 1, "FAIL"},
		{"unimplemented is could-not-look", "", &cloud.Error{Code: cloud.CodeUnimplemented, Message: "no such method"}, nil, 2, ""},
		{"unavailable is could-not-look", "", &cloud.Error{Code: cloud.CodeUnavailable}, nil, 2, ""},
		{"permission denied is could-not-look", "", &cloud.Error{Code: cloud.CodePermissionDenied}, nil, 2, ""},
		{"invalid argument is wrong", "", &cloud.Error{Code: cloud.CodeInvalidArgument}, nil, 1, ""},
		{"a transport failure is could-not-look", "", io.ErrUnexpectedEOF, nil, 2, ""},
		{"a bad --threshold is wrong before any call", "", nil, []string{"--threshold", "nope=1"}, 1, ""},
		{"a bad --fail-on is wrong before any call", "", nil, []string{"--fail-on", "vibes"}, 1, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			caller := &seqCaller{replies: map[string][]string{procHealth: {tc.reply}}, errs: map[string]error{}}
			if tc.err != nil {
				caller.errs[procHealth] = tc.err
			}
			out, _, err := newObsCheckHarness().run(t, cloudFacts(), caller, nil, append([]string{"check", "prod", "--json"}, tc.args...)...)
			if got := exitCodeForError(err); got != tc.exit {
				t.Fatalf("exit = %d, want %d (%v)", got, tc.exit, err)
			}
			doc := decode(t, out)
			if doc["schema"] != "forge.obs.check/v1" || doc["exit_code"] != float64(tc.exit) || doc["ok"] != (tc.exit == 0) {
				t.Errorf("head disagrees with the process: %v", doc)
			}
			if tc.want == "" {
				if _, has := doc["verdict"]; has {
					t.Errorf("a check that could not look states no verdict: %v", doc["verdict"])
				}
			} else if doc["verdict"] != tc.want {
				t.Errorf("verdict = %v, want %s", doc["verdict"], tc.want)
			}
			if tc.name == "unimplemented is could-not-look" && !strings.Contains(fmt.Sprint(doc["error"]), "does not serve release health") {
				t.Errorf("unimplemented must say so: %v", doc["error"])
			}
			if len(tc.args) > 0 && strings.Contains(tc.args[0], "threshold") && len(caller.calls) != 0 {
				t.Errorf("a bad flag reached the control plane: %+v", caller.calls)
			}
		})
	}
}

func TestObsCheckCloudRequestShapeAndDocument(t *testing.T) {
	reply := healthReply("RELEASE_VERDICT_FAIL", true, errorRateFinding+","+latencyFinding,
		`"newIssues":[{"id":"ISS-7f3a","service":"api","title":"boom","events":"12","firstSeenAt":"2026-10-22T11:41:00Z","firstVersion":"v2"}],
		 "regressedIssues":[{"id":"ISS-1","title":"old","events":"2","regressedAt":"2026-10-22T11:42:00Z"}],`)
	caller := &seqCaller{replies: map[string][]string{procHealth: {reply}}}
	out, _, err := newObsCheckHarness().run(t, cloudFacts(), caller, nil,
		"check", "prod", "--release", "v2", "--window", "30m", "--threshold", "min_requests=20", "--threshold", "max_p95_ratio=3", "--json")
	if exitCodeForError(err) != 1 {
		t.Fatalf("err = %v", err)
	}
	if len(caller.calls) != 1 || caller.calls[0].procedure != procHealth {
		t.Fatalf("calls = %+v", caller.calls)
	}
	body := caller.calls[0].body
	for _, want := range []string{`"envIds":["env-prod"]`, `"orgId":"org-1"`, `"releaseVersion":"v2"`, `"window":"1800s"`,
		`"thresholdOverrides":{"max_p95_ratio":"3","min_requests":"20"}`} {
		if !strings.Contains(body, want) {
			t.Errorf("request lacks %s:\n%s", want, body)
		}
	}
	if strings.Contains(body, "promotionId") {
		t.Errorf("--release names the subject alone:\n%s", body)
	}

	doc := decode(t, out)
	if sc := doc["scope"].(map[string]any); sc["backend"] != "cloud" || sc["env_id"] != "env-prod" {
		t.Errorf("scope = %v", sc)
	}
	if doc["query_mode"] != "structured" {
		t.Errorf("query_mode = %v", doc["query_mode"])
	}
	subj := doc["subject"].(map[string]any)
	if subj["release"] != "v2" || subj["promotion_id"] != "promo-7" || subj["previous_release"] != "v1" {
		t.Errorf("subject = %v", subj)
	}
	if w := doc["windows"].(map[string]any); w["after_complete"] != true {
		t.Errorf("windows = %v", w)
	}
	if fs := doc["findings"].([]any); len(fs) != 2 || fs[0].(map[string]any)["code"] != "error_rate" || fs[1].(map[string]any)["policy"] != "warn" {
		t.Errorf("findings = %v", fs)
	}
	ths := fmt.Sprint(doc["thresholds"])
	if !strings.Contains(ths, "release_shape") || !strings.Contains(ths, "default") {
		t.Errorf("thresholds carry their sources: %s", ths)
	}
	sug := fmt.Sprint(doc["suggested"])
	for _, want := range []string{"forge obs issue ISS-7f3a prod", "forge obs issue ISS-1 prod", "forge obs endpoints prod --sort errors"} {
		if !strings.Contains(sug, want) {
			t.Errorf("suggested lacks %q: %s", want, sug)
		}
	}
	obsGolden(t, "check.fail.json", out)
}

func TestObsCheckFailOnReJudgesTheControlPlanesAnswer(t *testing.T) {
	// The control plane judged under the env's policy (latency warns).
	reply := healthReply("RELEASE_VERDICT_PASS", true, latencyFinding, "")
	run := func(args ...string) (int, map[string]any) {
		caller := &seqCaller{replies: map[string][]string{procHealth: {reply}}}
		out, _, err := newObsCheckHarness().run(t, cloudFacts(), caller, nil, append([]string{"check", "prod", "--json"}, args...)...)
		return exitCodeForError(err), decode(t, out)
	}
	if code, _ := run(); code != 0 {
		t.Errorf("env policy: exit %d", code)
	}
	code, doc := run("--fail-on", "latency")
	if code != 1 || doc["verdict"] != "FAIL" {
		t.Errorf("--fail-on latency: exit %d verdict %v", code, doc["verdict"])
	}
	if !strings.Contains(fmt.Sprint(doc["thresholds"]), "request") {
		t.Errorf("the override is attributed to the request: %v", doc["thresholds"])
	}
}

func TestObsCheckWaitPollsUntilTheWindowFinishes(t *testing.T) {
	open := healthReply("RELEASE_VERDICT_INCONCLUSIVE", false, "", "")
	done := healthReply("RELEASE_VERDICT_PASS", true, "", "")
	caller := &seqCaller{replies: map[string][]string{procHealth: {open, open, done}}}
	env := newObsCheckHarness()
	out, errOut, err := env.run(t, cloudFacts(), caller, nil, "check", "prod", "--wait", "--json")
	if err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	if env.sleeps != 2 || len(caller.calls) != 3 {
		t.Errorf("sleeps = %d calls = %d, want 2 and 3", env.sleeps, len(caller.calls))
	}
	if got := env.now.Sub(obsNow); got != 2*obsCheckPollEvery {
		t.Errorf("clock advanced %s, want 2 polls of %s", got, obsCheckPollEvery)
	}
	if doc := decode(t, out); doc["verdict"] != "PASS" || doc["waited"] != "30s" {
		t.Errorf("doc = %v", doc)
	}
	if !strings.Contains(errOut, "waiting for the after window") {
		t.Errorf("progress goes to stderr: %q", errOut)
	}
}

func TestObsCheckWaitTimeoutIsExit8(t *testing.T) {
	open := healthReply("RELEASE_VERDICT_INCONCLUSIVE", false, "", "")
	caller := &seqCaller{replies: map[string][]string{procHealth: {open}}}
	env := newObsCheckHarness()
	out, _, err := env.run(t, cloudFacts(), caller, nil, "check", "prod", "--wait", "--timeout", "1m", "--json")
	if exitCodeForError(err) != exitTimedOut {
		t.Fatalf("exit = %d (%v), want 8", exitCodeForError(err), err)
	}
	if env.sleeps != 3 { // 15s, 30s, 45s; a 4th poll would reach the deadline
		t.Errorf("sleeps = %d", env.sleeps)
	}
	doc := decode(t, out)
	if doc["exit_code"] != float64(8) || doc["verdict"] != "INCONCLUSIVE" {
		t.Errorf("doc = %v", doc)
	}
}

func TestObsCheckWithoutWaitReportsAnOpenWindowAsInconclusive(t *testing.T) {
	caller := &seqCaller{replies: map[string][]string{procHealth: {healthReply("RELEASE_VERDICT_INCONCLUSIVE", false, "", "")}}}
	env := newObsCheckHarness()
	out, _, err := env.run(t, cloudFacts(), caller, nil, "check", "prod", "--json")
	if exitCodeForError(err) != 2 || env.sleeps != 0 {
		t.Fatalf("exit = %d sleeps = %d", exitCodeForError(err), env.sleeps)
	}
	if doc := decode(t, out); !strings.Contains(fmt.Sprint(doc["reason"]), "has not finished") {
		t.Errorf("reason = %v", doc["reason"])
	}
}

// --gate-json writes the document `forge gate record --from` reads: the round
// trip goes through the real reader.
func TestObsCheckGateJSONRoundTrip(t *testing.T) {
	tests := []struct {
		name   string
		reply  string
		err    error
		args   []string
		status release.GateStatus
	}{
		{"PASS", healthReply("RELEASE_VERDICT_PASS", true, "", ""), nil, nil, release.GateStatusPassed},
		{"FAIL", healthReply("RELEASE_VERDICT_FAIL", true, errorRateFinding, ""), nil, nil, release.GateStatusFailed},
		{"INCONCLUSIVE is error, not a pass", healthReply("RELEASE_VERDICT_INCONCLUSIVE", true, "", ""), nil, []string{"--allow-inconclusive"}, release.GateStatusError},
		{"could not look is error", "", &cloud.Error{Code: cloud.CodeUnavailable}, nil, release.GateStatusError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "check.json")
			caller := &seqCaller{replies: map[string][]string{procHealth: {tc.reply}}, errs: map[string]error{}}
			if tc.err != nil {
				caller.errs[procHealth] = tc.err
			}
			_, _, _ = newObsCheckHarness().run(t, cloudFacts(), caller, nil, append([]string{"check", "prod", "--gate-json", path}, tc.args...)...)
			gate, err := gateFromDocumentFile(path, "")
			if err != nil {
				t.Fatalf("`gate record --from` cannot read it: %v", err)
			}
			if gate.Name != "release-health" || gate.Status != tc.status {
				t.Errorf("gate = %s/%s, want release-health/%s", gate.Name, gate.Status, tc.status)
			}
			if gate.Summary == "" || gate.StartedAt == nil || gate.FinishedAt == nil {
				t.Errorf("gate lacks its summary or window: %+v", gate)
			}
			if tc.status != release.GateStatusError || tc.err == nil {
				if gate.Details["verdict"] == nil {
					t.Errorf("details carry the verdict: %v", gate.Details)
				}
			}
		})
	}
}

// ── local ───────────────────────────────────────────────────────────────

// healthRunner answers a local check: the per-service RED statement is run
// once per window (before, then after), and the issues statement last.
type healthRunner struct {
	before, after, issues obsbackend.Result
	ran                   []obsquery.Stmt
	reds                  int
}

func (h *healthRunner) Run(_ context.Context, st obsquery.Stmt, _ obsbackend.Limits) (obsbackend.Result, error) {
	h.ran = append(h.ran, st)
	switch {
	case strings.Contains(st.SQL, "first_seen"):
		return h.issues, nil
	case strings.Contains(st.SQL, "quantilesExactWeighted"):
		h.reds++
		if h.reds == 1 {
			return h.before, nil
		}
		return h.after, nil
	}
	return obsbackend.Result{}, nil
}

func redRow(svc string, requests, errors int, p95 float64) []any {
	return []any{svc, jn(fmt.Sprint(requests)), jn(fmt.Sprint(errors)), []any{jn("10"), jn(fmt.Sprint(p95)), jn("300")}, ""}
}

func TestObsCheckLocalOverScriptedRunner(t *testing.T) {
	hr := &healthRunner{
		before: obsbackend.Result{Rows: [][]any{redRow("api", 1000, 1, 100)}},
		after:  obsbackend.Result{Rows: [][]any{redRow("api", 1000, 80, 110)}},
		issues: obsbackend.Result{Rows: [][]any{{"deadbeefdeadbeef", "boom: <n>", "x.go in Handle", "api", jn("7"), "2026-10-22 11:41:00.000000000"}}},
	}
	out, _, err := newObsCheckHarness().run(t, devFacts(), nil, hr, "check", "dev", "--since", "20m", "--json")
	if exitCodeForError(err) != 1 {
		t.Fatalf("exit = %d (%v)", exitCodeForError(err), err)
	}
	if hr.reds != 2 {
		t.Errorf("RED ran %d times, want once per window", hr.reds)
	}
	// P = now-20m = 11:40; before = [11:25, 11:40), after = [11:40, 11:55).
	doc := decode(t, out)
	w := doc["windows"].(map[string]any)
	if w["before"].(map[string]any)["from"] != "2026-10-22T11:25:00Z" || w["after"].(map[string]any)["to"] != "2026-10-22T11:55:00Z" || w["after_complete"] != true {
		t.Errorf("windows = %v", w)
	}
	var codes []string
	for _, f := range doc["findings"].([]any) {
		codes = append(codes, f.(map[string]any)["code"].(string))
	}
	if strings.Join(codes, ",") != "error_rate,new_issue" {
		t.Errorf("findings = %v", codes)
	}
	if ne := fmt.Sprint(doc["not_evaluated"]); !strings.Contains(ne, "alert_firing") || !strings.Contains(ne, "workload_restarts") || !strings.Contains(ne, "regressed_issue") {
		t.Errorf("a local PASS must not claim what it did not look at: %s", ne)
	}
	if !strings.Contains(fmt.Sprint(doc["suggested"]), "forge obs issue sfp1-deadbeefdeadbeef dev") {
		t.Errorf("suggested = %v", doc["suggested"])
	}
	// The sfp1 statement is parameterized: no literal from the request in SQL.
	last := hr.ran[len(hr.ran)-1]
	if !strings.Contains(last.SQL, obsquery.ServerFingerprintSQL) || strings.Contains(last.SQL, "'dev'") || last.Params["env"] != "dev" {
		t.Errorf("issues statement:\n%s\n%v", last.SQL, last.Params)
	}
	obsGolden(t, "check.local.fail.json", out)
}

func TestObsCheckLocalEqualTrafficPasses(t *testing.T) {
	hr := &healthRunner{
		before: obsbackend.Result{Rows: [][]any{redRow("api", 1000, 10, 100)}},
		after:  obsbackend.Result{Rows: [][]any{redRow("api", 1000, 10, 100)}},
	}
	out, _, err := newObsCheckHarness().run(t, devFacts(), nil, hr, "check", "dev", "--since", "20m")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "PASS") {
		t.Errorf("human output:\n%s", out)
	}
}

func TestObsCheckLocalThinTrafficIsInconclusive(t *testing.T) {
	hr := &healthRunner{
		before: obsbackend.Result{Rows: [][]any{redRow("api", 1000, 0, 100)}},
		after:  obsbackend.Result{Rows: [][]any{redRow("api", 10, 9, 900)}},
	}
	_, _, err := newObsCheckHarness().run(t, devFacts(), nil, hr, "check", "dev", "--since", "20m")
	if exitCodeForError(err) != 2 {
		t.Errorf("exit = %d, want 2 (%v)", exitCodeForError(err), err)
	}
	_, _, err = newObsCheckHarness().run(t, devFacts(), nil, hr, "check", "dev", "--since", "20m", "--allow-inconclusive")
	if err != nil {
		t.Errorf("--allow-inconclusive: %v", err)
	}
}

func TestObsCheckLocalOpenWindowAtFiveMinutes(t *testing.T) {
	hr := &healthRunner{
		before: obsbackend.Result{Rows: [][]any{redRow("api", 1000, 0, 100)}},
		after:  obsbackend.Result{Rows: [][]any{redRow("api", 1000, 0, 100)}},
	}
	out, _, err := newObsCheckHarness().run(t, devFacts(), nil, hr, "check", "dev", "--since", "5m", "--json")
	if exitCodeForError(err) != 2 {
		t.Fatalf("exit = %d (%v)", exitCodeForError(err), err)
	}
	w := decode(t, out)["windows"].(map[string]any)
	if w["after_complete"] != false || w["after"].(map[string]any)["to"] != "2026-10-22T12:00:00Z" {
		t.Errorf("T+5m with w=15m: windows = %v", w)
	}
}

func TestObsCheckLocalDefaultsToTheLastEnvUp(t *testing.T) {
	old := obsLocalSessionStart
	t.Cleanup(func() { obsLocalSessionStart = old })
	obsLocalSessionStart = func(env string) (time.Time, bool, error) {
		return obsNow.Add(-20 * time.Minute), env == "dev", nil
	}
	hr := &healthRunner{
		before: obsbackend.Result{Rows: [][]any{redRow("api", 1000, 0, 100)}},
		after:  obsbackend.Result{Rows: [][]any{redRow("api", 1000, 0, 100)}},
	}
	out, _, err := newObsCheckHarness().run(t, devFacts(), nil, hr, "check", "dev", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if got := decode(t, out)["subject"].(map[string]any)["promoted_at"]; got != "2026-10-22T11:40:00Z" {
		t.Errorf("promoted_at = %v", got)
	}

	obsLocalSessionStart = func(string) (time.Time, bool, error) { return time.Time{}, false, nil }
	out, _, err = newObsCheckHarness().run(t, devFacts(), nil, hr, "check", "dev", "--json")
	if exitCodeForError(err) != 2 || !strings.Contains(fmt.Sprint(decode(t, out)["suggested"]), "forge obs check dev --since 20m") {
		t.Errorf("no recorded start: exit %d\n%s", exitCodeForError(err), out)
	}
}

func TestObsCheckLocalRefusesPromotionFlags(t *testing.T) {
	_, _, err := newObsCheckHarness().run(t, devFacts(), nil, &healthRunner{}, "check", "dev", "--release", "v2", "--since", "20m")
	if exitCodeForError(err) != 1 {
		t.Errorf("exit = %d (%v)", exitCodeForError(err), err)
	}
}

func TestObsCheckLocalExplainRunsNothing(t *testing.T) {
	hr := &healthRunner{}
	out, _, err := newObsCheckHarness().run(t, devFacts(), nil, hr, "check", "dev", "--since", "20m", "--explain")
	if err != nil {
		t.Fatal(err)
	}
	if len(hr.ran) != 0 {
		t.Errorf("--explain ran %d statements", len(hr.ran))
	}
	if !strings.Contains(out, "FROM default.otel_traces") || !strings.Contains(out, "otel_logs") {
		t.Errorf("explain output:\n%s", out)
	}
}

func TestObsCheckTelemetryOffIsCouldNotLook(t *testing.T) {
	_, _, err := newObsCheckHarness().run(t, obsbackend.Facts{Env: "dev"}, nil, &healthRunner{}, "check", "dev", "--since", "20m")
	if exitCodeForError(err) != 2 {
		t.Errorf("exit = %d (%v)", exitCodeForError(err), err)
	}
}

func TestObsCheckFieldsProjectionAndRegistration(t *testing.T) {
	caller := &seqCaller{replies: map[string][]string{procHealth: {healthReply("RELEASE_VERDICT_PASS", true, "", "")}}}
	out, _, err := newObsCheckHarness().run(t, cloudFacts(), caller, nil, "check", "prod", "--json", "--fields", "verdict")
	if err != nil {
		t.Fatal(err)
	}
	doc := decode(t, out)
	if doc["verdict"] != "PASS" || doc["services"] != nil || doc["schema"] != "forge.obs.check/v1" {
		t.Errorf("projection: %v", doc)
	}
	if _, _, err := newObsCheckHarness().run(t, cloudFacts(), caller, nil, "check", "prod", "--json", "--fields", "verdct"); err == nil || !strings.Contains(err.Error(), "verdict") {
		t.Errorf("a typo lists the real fields: %v", err)
	}
}

// ── changes ─────────────────────────────────────────────────────────────

const (
	procEnvs     = "controlplane.v1.DeployService/ListEnvironments"
	procPromos   = "controlplane.v1.DeployService/ListPromotions"
	procReleases = "controlplane.v1.DeployService/ListReleases"
	procConv     = "controlplane.v1.DeployService/ListConvergences"
	procActivity = "controlplane.v1.ActivityService/ListActivity"
	procOrg      = cloud.ProcGetOrganization
)

func orgReplyJSON() string {
	b, _ := json.Marshal(cloud.OrganizationReply("org-1"))
	return string(b)
}

func changesCaller() *seqCaller {
	return &seqCaller{replies: map[string][]string{
		procEnvs: {`{"environments":[{"id":"env-prod","name":"prod"}]}`},
		procPromos: {`{"promotions":[
			{"id":"p2","releaseVersion":"v2","createdAt":"2026-10-22T11:30:00Z","promotedByActor":"ci"},
			{"id":"p1","releaseVersion":"v1","createdAt":"2026-10-22T10:30:00Z"}]}`},
		procReleases: {`{"releases":[]}`},
		procConv:     {`{"convergences":[{"id":"c1","promotionId":"p2","state":"failed","reason":"ImagePullBackOff","observedAt":"2026-10-22T11:35:00Z","firstAttemptAt":"2026-10-22T11:30:30Z"}]}`},
		procActivity: {`{"events":[
			{"id":"a1","at":"2026-10-22T11:50:00Z","actor":{"name":"alice"},"verb":"rotate","target":{"kind":"secret","id":"db-pass"},"via":"cli"},
			{"id":"a2","at":"2026-10-22T11:30:00Z","actor":{"name":"ci"},"verb":"promote","target":{"kind":"release","id":"v2"},"via":"ci"}]}`},
		procOrg: {orgReplyJSON()},
	}}
}

func TestObsChangesCloudMergesTheLedgerAndActivity(t *testing.T) {
	caller := changesCaller()
	out, _, err := newObsCheckHarness().run(t, cloudFacts(), caller, nil, "changes", "prod", "--since", "2h", "--json")
	if err != nil {
		t.Fatal(err)
	}
	doc := decode(t, out)
	if doc["schema"] != "forge.obs.changes/v1" || doc["query_mode"] != "structured" {
		t.Errorf("head: %v", doc)
	}
	var kinds []string
	for _, c := range doc["changes"].([]any) {
		m := c.(map[string]any)
		kinds = append(kinds, m["kind"].(string)+":"+fmt.Sprint(m["version"]))
	}
	// Newest first: the rotation (11:50), the failed deploy (11:35), v2 (11:30), v1 (10:30).
	// The promote activity event is the ledger row's own; it is not repeated.
	if got := strings.Join(kinds, " "); got != "activity:<nil> failed_deploy:v2 promotion:v2 promotion:v1" {
		t.Errorf("changes = %s", got)
	}
	var act *fakeCloudCall
	for i := range caller.calls {
		if caller.calls[i].procedure == procActivity {
			act = &caller.calls[i]
		}
	}
	if act == nil || !strings.Contains(act.body, `"envIds":["env-prod"]`) || !strings.Contains(act.body, `"orgId":"org-1"`) {
		t.Errorf("activity request: %+v", act)
	}
	if sug := fmt.Sprint(doc["suggested"]); !strings.Contains(sug, "forge obs check prod --promotion") {
		t.Errorf("suggested = %s", sug)
	}
	if _, warned := doc["warnings"]; warned {
		t.Errorf("every source answered, so there is nothing to warn about: %v", doc["warnings"])
	}
	if !strings.Contains(out, `"via": "ci"`) {
		t.Errorf("the promotion row takes its channel from the matching activity event:\n%s", out)
	}
	obsGolden(t, "changes.json", out)
}

func TestObsChangesActivityUnimplementedDegradesToTheLedger(t *testing.T) {
	caller := changesCaller()
	caller.errs = map[string]error{procActivity: &cloud.Error{Code: cloud.CodeUnimplemented}}
	out, _, err := newObsCheckHarness().run(t, cloudFacts(), caller, nil, "changes", "prod", "--json")
	if err != nil {
		t.Fatal(err)
	}
	doc := decode(t, out)
	if len(doc["changes"].([]any)) != 3 || !strings.Contains(fmt.Sprint(doc["warnings"]), "does not serve activity") {
		t.Errorf("doc = %v", doc)
	}
}

func TestObsChangesLimitReportsOmitted(t *testing.T) {
	out, _, err := newObsCheckHarness().run(t, cloudFacts(), changesCaller(), nil, "changes", "prod", "--limit", "2", "--json")
	if err != nil {
		t.Fatal(err)
	}
	doc := decode(t, out)
	if len(doc["changes"].([]any)) != 2 || doc["truncated"] != true || doc["omitted"] != float64(2) {
		t.Errorf("doc = %v", doc)
	}
}

func TestObsChangesCloudErrorsMapToTheExitTable(t *testing.T) {
	caller := changesCaller()
	caller.errs = map[string]error{procPromos: &cloud.Error{Code: cloud.CodeUnavailable}}
	_, _, err := newObsCheckHarness().run(t, cloudFacts(), caller, nil, "changes", "prod", "--json")
	if exitCodeForError(err) != 2 {
		t.Errorf("exit = %d (%v)", exitCodeForError(err), err)
	}
}

func TestObsChangesLocalReadsTheMachineLedger(t *testing.T) {
	store, err := ledgerfile.Open(t.TempDir(), "proj")
	if err != nil {
		t.Fatal(err)
	}
	sessions := []release.LocalSession{
		{ID: "s1", Env: "dev", Worktree: release.Worktree{Label: "main", Host: "h1"}, StartedAt: obsNow.Add(-30 * time.Minute), LastSeenAt: obsNow},
		{ID: "s0", Env: "dev", Worktree: release.Worktree{Key: "old", Host: "h1"}, StartedAt: obsNow.Add(-5 * time.Hour), LastSeenAt: obsNow},
	}
	for _, s := range sessions {
		if err := store.ReportSession(s); err != nil {
			t.Fatal(err)
		}
	}
	env := newObsCheckHarness()
	env.ledger = store
	out, _, err := env.run(t, devFacts(), nil, &healthRunner{}, "changes", "dev", "--since", "2h", "--json")
	if err != nil {
		t.Fatal(err)
	}
	doc := decode(t, out)
	cs := doc["changes"].([]any)
	if len(cs) != 1 || cs[0].(map[string]any)["kind"] != "session" || !strings.Contains(fmt.Sprint(cs[0]), "forge env up dev started (main)") {
		t.Errorf("the 5h-old stack is outside --since 2h: %v", cs)
	}
	if doc["scope"].(map[string]any)["backend"] != "local" {
		t.Errorf("scope = %v", doc["scope"])
	}
}
