package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
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
	"github.com/reliant-labs/forge/pkg/releasehealth"
)

const procHealth = "controlplane.v1.ObservabilityService/GetReleaseHealth"

const (
	procListAlertsCheck       = "controlplane.v1.AlertingService/ListAlerts"
	procListAlertHistoryCheck = "controlplane.v1.AlertingService/ListAlertHistory"
)

const (
	procListGatesCheck = "controlplane.v1.DeployService/ListGates"
	procGetIssueCheck  = "controlplane.v1.IssuesService/GetIssue"
)

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
	// No alerts firing and no alert history, unless the test says
	// otherwise: obs check reads the alert state on every cloud check, and
	// the history when an alert_firing finding could fail.
	fc := &fakeCloudCaller{replies: map[string]string{procListAlertsCheck: `{"alerts":[]}`, procListAlertHistoryCheck: `{"events":[]}`}, errs: s.errs}
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
	slept  []time.Duration
	ledger obsbackend.LedgerReader
	// opChanges is the changed-op source; nil reads none (no git in a
	// test), so a check judges no changed ops unless the test says.
	opChanges opChangeFunc
	// wrapLocal replaces the local backend, to give it another OpsReader
	// or none (obs_check_changed_ops_test.go).
	wrapLocal func(obsbackend.Local) obsbackend.Backend
}

func (e *obsCheckHarness) run(t *testing.T, facts obsbackend.Facts, caller obsbackend.Caller, local obsbackend.Runner, args ...string) (string, string, error) {
	t.Helper()
	var out, errb bytes.Buffer
	old, oldSleep, oldJitter := obsDepsOverride, obsCheckSleep, obsCheckJitter
	t.Cleanup(func() { obsDepsOverride, obsCheckSleep, obsCheckJitter = old, oldSleep, oldJitter })
	obsCheckSleep = func(_ context.Context, d time.Duration) error {
		e.now = e.now.Add(d)
		e.sleeps++
		e.slept = append(e.slept, d)
		return nil
	}
	obsCheckJitter = func(time.Duration) time.Duration { return 0 }
	obsDepsOverride = &obsDeps{
		Facts: func(context.Context, io.Writer, string) (obsbackend.Facts, error) { return facts, nil },
		Local: func(env string) obsbackend.Backend {
			l := obsbackend.Local{Env: env, Runner: local, Ledger: func() (obsbackend.LedgerReader, error) { return e.ledger, nil }}
			if e.wrapLocal != nil {
				return e.wrapLocal(l)
			}
			return l
		},
		Cloud: func(_ context.Context, env string) (obsbackend.Cloud, error) {
			return obsbackend.Cloud{Env: env, EnvID: "env-prod", OrgID: "org-1", Caller: caller}, nil
		},
		Now:       func() time.Time { return e.now },
		OpChanges: e.opChanges,
	}
	if e.opChanges == nil {
		obsDepsOverride.OpChanges = func(context.Context, string, releasehealth.Subject) opChanges { return opChanges{} }
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
		 "regressedIssues":[{"id":"ISS-1","title":"old","events":"4","regressedAt":"2026-10-22T11:42:00Z"}],`)
	caller := &seqCaller{replies: map[string][]string{procHealth: {reply}}}
	out, _, err := newObsCheckHarness().run(t, cloudFacts(), caller, nil,
		"check", "prod", "--release", "v2", "--window", "30m", "--threshold", "min_requests=20", "--threshold", "max_p95_ratio=3", "--json")
	if exitCodeForError(err) != 1 {
		t.Fatalf("err = %v", err)
	}
	// The judgement is one GetReleaseHealth; the rest are its attribution
	// reads (the promotion's gates, each issue's events by version).
	if len(caller.calls) == 0 || caller.calls[0].procedure != procHealth {
		t.Fatalf("calls = %+v", caller.calls)
	}
	for _, c := range caller.calls[1:] {
		if c.procedure != procListGatesCheck && c.procedure != procGetIssueCheck {
			t.Errorf("unexpected call %s", c.procedure)
		}
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
	if env.sleeps != 2 || caller.n[procHealth] != 3 {
		t.Errorf("sleeps = %d health calls = %d, want 2 and 3", env.sleeps, caller.n[procHealth])
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

// The control plane stops an open after window at now, so its end moves with
// every poll. The progress line names the window's real end, P + window, the
// same on every poll (forge_cli_report 9fe4605f).
func TestObsCheckWaitNamesTheWindowsEndNotTheReadTime(t *testing.T) {
	open := func(readAt string) string {
		r := healthReply("RELEASE_VERDICT_INCONCLUSIVE", false, "", "")
		r = strings.Replace(r, `"end":"2026-10-22T11:55:00Z"`, `"end":"`+readAt+`"`, 1)
		return strings.Replace(r, `"thresholds":[`, `"thresholds":[{"name":"window","value":"15m","source":"default"},`, 1)
	}
	done := healthReply("RELEASE_VERDICT_PASS", true, "", "")
	caller := &seqCaller{replies: map[string][]string{procHealth: {open("2026-10-22T11:47:40Z"), open("2026-10-22T11:47:55Z"), done}}}
	_, errOut, err := newObsCheckHarness().run(t, cloudFacts(), caller, nil, "check", "prod", "--wait", "--json")
	if err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	if n := strings.Count(errOut, "(ends 2026-10-22T11:55:00Z)"); n != 2 {
		t.Errorf("both polls name the window's end 11:55: %q", errOut)
	}
	if strings.Contains(errOut, "11:47") {
		t.Errorf("progress names the read time as the end: %q", errOut)
	}
}

// --early-pass: an open window that is clean, rolled out and busy enough passes
// at once; anything found, or too little of the window, keeps the wait going.
func TestObsCheckEarlyPass(t *testing.T) {
	open := healthReply("RELEASE_VERDICT_INCONCLUSIVE", false, "", "")
	done := healthReply("RELEASE_VERDICT_PASS", true, "", "")
	for _, tc := range []struct {
		name       string
		replies    []string
		earlyPass  string
		wantSleeps int
		wantEarly  bool
	}{
		{"clean, rolled out, 15m observed", []string{open, open, done}, "5m", 0, true},
		{"less observed than asked", []string{open, open, done}, "20m", 2, false},
		{"a warn finding waits for the window", []string{healthReply("RELEASE_VERDICT_INCONCLUSIVE", false, latencyFinding, ""), done}, "5m", 1, false},
		{"rollout not finished", []string{strings.Replace(open, `"versionShare":1`, `"versionShare":0.6`, 1), done}, "5m", 1, false},
		{"a second service too quiet waits for the window", []string{strings.Replace(open, `"versionShare":1}]`, `"versionShare":1},{"service":"worker","before":{"requests":"5"},"after":{"requests":"3"},"versionShare":1}]`, 1), done}, "5m", 1, false},
		{"a second service with no version share waits for the window", []string{strings.Replace(open, `"versionShare":1}]`, `"versionShare":1},{"service":"worker","before":{"requests":"900"},"after":{"requests":"900"}}]`, 1), done}, "5m", 1, false},
		{"a new issue waits for the window", []string{healthReply("RELEASE_VERDICT_INCONCLUSIVE", false, "", `"newIssues":[{"id":"i1","service":"api","title":"boom","events":"9"}],`), done}, "5m", 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caller := &seqCaller{replies: map[string][]string{procHealth: tc.replies}}
			env := newObsCheckHarness()
			out, errOut, err := env.run(t, cloudFacts(), caller, nil, "check", "prod", "--wait", "--early-pass", tc.earlyPass, "--json")
			if err != nil {
				t.Fatalf("%v\n%s", err, errOut)
			}
			doc := decode(t, out)
			if env.sleeps != tc.wantSleeps || doc["verdict"] != "PASS" {
				t.Errorf("sleeps = %d verdict = %v, want %d PASS", env.sleeps, doc["verdict"], tc.wantSleeps)
			}
			if early := doc["early_pass"] != nil; early != tc.wantEarly {
				t.Errorf("early_pass = %v, want set: %v", doc["early_pass"], tc.wantEarly)
			}
		})
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
// once per window (before, then after), then RED by endpoint per window
// (answered with no rows, so latency is judged per service), the issues
// statement, and browser RED per window (no rows: no frontend).
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
	case strings.Contains(st.SQL, "GROUP BY ServiceName, endpoint"):
		return obsbackend.Result{}, nil
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
	var last obsquery.Stmt
	for _, st := range hr.ran {
		if strings.Contains(st.SQL, "first_seen") {
			last = st
		}
	}
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

// A release promoted into a critical alert that is still firing is not proven
// good: the PASS the control plane computed becomes INCONCLUSIVE (exit 2).
func TestObsCheckStandingCriticalAlertIsNotAPass(t *testing.T) {
	alerts := `{"alerts":[{"rule":"SecretStoreNotReady","state":"firing","fingerprint":"f1","firingAt":"2020-01-01T00:00:00Z","labels":{"severity":"critical","namespace":"ns","name":"store"}},` +
		`{"rule":"Backup","state":"firing","firingAt":"2020-01-01T00:00:00Z","labels":{"severity":"warning"}}]}`
	caller := &seqCaller{replies: map[string][]string{
		procHealth: {healthReply("RELEASE_VERDICT_PASS", true, "", "")},
		"controlplane.v1.AlertingService/ListAlerts": {alerts},
	}}
	env := newObsCheckHarness()
	out, _, err := env.run(t, cloudFacts(), caller, nil, "check", "prod", "--json")
	if exitCodeForError(err) != exitUndetermined {
		t.Fatalf("exit = %d (%v), want 2", exitCodeForError(err), err)
	}
	doc := decode(t, out)
	if doc["verdict"] != "INCONCLUSIVE" || !strings.Contains(fmt.Sprint(doc["reason"]), "SecretStoreNotReady") {
		t.Errorf("doc = %v", doc)
	}
	if sc, _ := doc["standing_critical_alerts"].([]any); len(sc) != 1 {
		t.Errorf("standing_critical_alerts = %v", doc["standing_critical_alerts"])
	}
}

// A critical alert that opened after the promotion fails the check over a
// control plane that judged it PASS, whatever --fail-on says.
func TestObsCheckPostPromotionCriticalAlertFails(t *testing.T) {
	alerts := `{"alerts":[{"rule":"Down","state":"firing","fingerprint":"f2","firingAt":"2026-10-22T11:45:00Z","labels":{"severity":"critical"}}]}`
	for _, extra := range [][]string{nil, {"--fail-on", "latency"}} {
		caller := &seqCaller{replies: map[string][]string{
			procHealth: {healthReply("RELEASE_VERDICT_PASS", true, "", "")},
			"controlplane.v1.AlertingService/ListAlerts": {alerts},
		}}
		env := newObsCheckHarness()
		out, _, err := env.run(t, cloudFacts(), caller, nil, append([]string{"check", "prod", "--json"}, extra...)...)
		if exitCodeForError(err) != exitWrong {
			t.Fatalf("%v: exit = %d (%v), want 1", extra, exitCodeForError(err), err)
		}
		if doc := decode(t, out); doc["verdict"] != "FAIL" || !strings.Contains(fmt.Sprint(doc["findings"]), "Down") {
			t.Errorf("%v: doc = %v", extra, doc)
		}
	}
}

// A check that cannot read the alert state cannot rule out a firing critical
// alert: INCONCLUSIVE, exit 2, and an error gate — never a PASS.
func TestObsCheckUnreadAlertStateIsNotAPass(t *testing.T) {
	caller := &seqCaller{
		replies: map[string][]string{procHealth: {healthReply("RELEASE_VERDICT_PASS", true, "", "")}},
		errs:    map[string]error{"controlplane.v1.AlertingService/ListAlerts": errors.New("permission denied")},
	}
	env := newObsCheckHarness()
	gate := filepath.Join(t.TempDir(), "gate.json")
	out, _, err := env.run(t, cloudFacts(), caller, nil, "check", "prod", "--json", "--gate-json", gate)
	if exitCodeForError(err) != exitUndetermined {
		t.Fatalf("exit = %d (%v), want 2", exitCodeForError(err), err)
	}
	if doc := decode(t, out); doc["verdict"] != "INCONCLUSIVE" || !strings.Contains(fmt.Sprint(doc["reason"]), "alert state") {
		t.Errorf("doc = %v", doc)
	}
	raw, rerr := os.ReadFile(gate)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if !strings.Contains(string(raw), `"status": "error"`) && !strings.Contains(string(raw), `"status":"error"`) {
		t.Errorf("gate = %s, want status error", raw)
	}
}

// A critical alert the control plane's own answer proves opened after the
// promotion fails the check even when the second alert read fails, and
// --allow-inconclusive does not soften it.
func TestObsCheckCriticalAlertInTheAnswerFailsWhenListAlertsFails(t *testing.T) {
	reply := strings.Replace(healthReply("RELEASE_VERDICT_PASS", true, "", ""), `"verdict"`,
		`"firingAlerts":[{"rule":"Down","firingAt":"2026-10-22T11:45:00Z","labels":{"severity":"critical"}}],"verdict"`, 1)
	caller := &seqCaller{
		replies: map[string][]string{procHealth: {reply}},
		errs:    map[string]error{procListAlertsCheck: errors.New("unavailable")},
	}
	out, _, err := newObsCheckHarness().run(t, cloudFacts(), caller, nil, "check", "prod", "--json", "--allow-inconclusive")
	if exitCodeForError(err) != exitWrong {
		t.Fatalf("exit = %d (%v), want 1", exitCodeForError(err), err)
	}
	if doc := decode(t, out); doc["verdict"] != "FAIL" {
		t.Errorf("doc = %v", doc)
	}
}

// An alert whose rule says it is not about the env's workloads (v1.12.0,
// 2026-11-09: MachineLoadHigh on the build host failed prod's check) judges
// nothing, whichever side judged it: the control plane's FAIL on its
// alert_firing finding becomes the PASS the rest of the answer earns, and the
// alert is listed as excluded. A counted alert of another rule still fails.
func TestObsCheckExcludedAlertJudgesNoRelease(t *testing.T) {
	const machine = `{"rule":"MachineLoadHigh","fingerprint":"m1","firingAt":"2026-10-22T11:45:00Z","labels":{"severity":"critical","machine":"ws-ws-e164d47d","release_health":"exclude"}}`
	finding := `{"code":"RELEASE_FINDING_CODE_ALERT_FIRING","ref":"MachineLoadHigh","value":1,"threshold":1,"policy":"fail","message":"alert MachineLoadHigh opened after the promotion and is still firing"}`
	reply := strings.Replace(healthReply("RELEASE_VERDICT_FAIL", true, finding, ""), `"verdict"`, `"firingAlerts":[`+machine+`],"verdict"`, 1)
	caller := &seqCaller{replies: map[string][]string{
		procHealth:          {reply},
		procListAlertsCheck: {`{"alerts":[` + strings.Replace(machine, `"rule"`, `"state":"firing","rule"`, 1) + `]}`},
	}}
	out, _, err := newObsCheckHarness().run(t, cloudFacts(), caller, nil, "check", "prod", "--json")
	if err != nil {
		t.Fatalf("exit = %d (%v), want 0; out = %s", exitCodeForError(err), err, out)
	}
	doc := decode(t, out)
	if doc["verdict"] != "PASS" || strings.Contains(fmt.Sprint(doc["findings"]), "MachineLoadHigh") || doc["firing_alerts"] != nil {
		t.Errorf("doc = %v", doc)
	}
	if ex, _ := doc["excluded_alerts"].([]any); len(ex) != 1 || !strings.Contains(fmt.Sprint(ex), "ws-ws-e164d47d") {
		t.Errorf("excluded_alerts = %v", doc["excluded_alerts"])
	}

	// An answer with no alert arrays (an older control plane): the alert
	// state read here names the rule excluded.
	reply = healthReply("RELEASE_VERDICT_FAIL", true, finding, "")
	caller = &seqCaller{replies: map[string][]string{
		procHealth:          {reply},
		procListAlertsCheck: {`{"alerts":[` + strings.Replace(machine, `"rule"`, `"state":"firing","rule"`, 1) + `]}`},
	}}
	out, _, err = newObsCheckHarness().run(t, cloudFacts(), caller, nil, "check", "prod", "--json")
	if err != nil {
		t.Fatalf("no alert arrays: exit = %d (%v), want 0; out = %s", exitCodeForError(err), err, out)
	}
	if doc := decode(t, out); doc["verdict"] != "PASS" || strings.Contains(fmt.Sprint(doc["findings"]), "MachineLoadHigh") {
		t.Errorf("no alert arrays: doc = %v", doc)
	}

	down := `{"rule":"Down","fingerprint":"d1","firingAt":"2026-10-22T11:45:00Z","labels":{"severity":"critical"}}`
	reply = strings.Replace(healthReply("RELEASE_VERDICT_PASS", true, "", ""), `"verdict"`, `"firingAlerts":[`+machine+`,`+down+`],"verdict"`, 1)
	caller = &seqCaller{replies: map[string][]string{procHealth: {reply}}}
	out, _, err = newObsCheckHarness().run(t, cloudFacts(), caller, nil, "check", "prod", "--json")
	if exitCodeForError(err) != exitWrong {
		t.Fatalf("a counted critical alert beside it: exit = %d (%v), want 1", exitCodeForError(err), err)
	}
	if doc := decode(t, out); doc["verdict"] != "FAIL" || !strings.Contains(fmt.Sprint(doc["findings"]), "Down") || strings.Contains(fmt.Sprint(doc["findings"]), "MachineLoadHigh") {
		t.Errorf("doc = %v", doc)
	}
}

// control-plane v1.21.0-rc.6 (2026-11-17): prod's check failed on
// IdentityLatency, a warning that fired 2-27 times a day and twice that day
// before the release rolled. The check reads the alert history and holds the
// control plane's answer to the baseline-alert rule: the alert fired in the
// baseline window, so its finding warns, it is listed in baseline_alerts, and
// the check passes. An alert the history never saw, --threshold
// alert_baseline_min_firings=0, or a history it cannot read still fails.
func TestObsCheckAlertThatFlappedBeforeThePromotionDoesNotFail(t *testing.T) {
	const latency = `{"rule":"IdentityLatency","fingerprint":"fp4","firingAt":"2026-10-22T11:45:00Z","labels":{"severity":"warning","n":"15","slow":"4"}}`
	const finding = `{"code":"RELEASE_FINDING_CODE_ALERT_FIRING","value":1,"threshold":1,"policy":"fail","message":"alert IdentityLatency opened after the promotion and is still firing"}`
	reply := strings.Replace(healthReply("RELEASE_VERDICT_FAIL", true, finding, ""), `"verdict"`, `"firingAlerts":[`+latency+`],"verdict"`, 1)
	// Newest first: the after-window firing, then two before the promotion
	// (11:40), one of them with another sample count.
	const flapped = `{"events":[` +
		`{"rule":"IdentityLatency","fingerprint":"fp4","labels":{"severity":"warning","n":"15","slow":"4"},"transition":"firing","at":"2026-10-22T11:45:00Z"},` +
		`{"rule":"IdentityLatency","fingerprint":"fp3","labels":{"severity":"warning","n":"14","slow":"3"},"transition":"resolved","at":"2026-10-22T11:30:00Z"},` +
		`{"rule":"IdentityLatency","fingerprint":"fp3","labels":{"severity":"warning","n":"14","slow":"3"},"transition":"firing","at":"2026-10-22T11:20:00Z"},` +
		`{"rule":"IdentityLatency","fingerprint":"fp5","labels":{"severity":"warning","n":"15","slow":"5"},"transition":"firing","at":"2026-10-22T02:00:00Z"}]}`
	run := func(t *testing.T, history string, errs map[string]error, args ...string) (map[string]any, string, *seqCaller, error) {
		t.Helper()
		caller := &seqCaller{replies: map[string][]string{procHealth: {reply}, procListAlertHistoryCheck: {history}}, errs: errs}
		out, stderr, err := newObsCheckHarness().run(t, cloudFacts(), caller, nil, append([]string{"check", "prod", "--json"}, args...)...)
		return decode(t, out), stderr, caller, err
	}

	doc, _, caller, err := run(t, flapped, nil)
	if err != nil {
		t.Fatalf("exit = %d (%v), want 0; doc = %v", exitCodeForError(err), err, doc)
	}
	fs, _ := doc["findings"].([]any)
	if doc["verdict"] != "PASS" || len(fs) != 1 || doc["firing_alerts"] != nil {
		t.Fatalf("doc = %v", doc)
	}
	if f := fs[0].(map[string]any); f["policy"] != "warn" || !strings.Contains(fmt.Sprint(f["unattributed"]), "fired 2 time(s) in the 24h before the promotion") {
		t.Errorf("finding = %v, want a warn naming the baseline firings", f)
	}
	if ba, _ := doc["baseline_alerts"].([]any); len(ba) != 1 || ba[0].(map[string]any)["baseline_firings"] != float64(2) {
		t.Errorf("baseline_alerts = %v", doc["baseline_alerts"])
	}
	var body string
	for _, c := range caller.calls {
		if c.procedure == procListAlertHistoryCheck {
			body = c.body
		}
	}
	// The history since the baseline window's start (24h before 11:40), for
	// this env, as much of it as one call returns.
	if !strings.Contains(body, `"since":"2026-10-21T11:40:00Z"`) || !strings.Contains(body, `"envId":"env-prod"`) || !strings.Contains(body, `"limit":1000`) {
		t.Errorf("ListAlertHistory request = %s", body)
	}

	for name, tc := range map[string]struct {
		history string
		errs    map[string]error
		args    []string
		stderr  string
	}{
		"never fired before":            {history: `{"events":[]}`},
		"alert_baseline_min_firings=0":  {history: flapped, args: []string{"--threshold", "alert_baseline_min_firings=0"}},
		"history unreadable":            {errs: map[string]error{procListAlertHistoryCheck: errors.New("permission denied")}, stderr: "could not read the alert history"},
		"a flap rate it does not reach": {history: flapped, args: []string{"--threshold", "alert_baseline_min_firings=3"}},
	} {
		t.Run(name, func(t *testing.T) {
			doc, stderr, _, err := run(t, tc.history, tc.errs, tc.args...)
			if exitCodeForError(err) != exitWrong || doc["verdict"] != "FAIL" || doc["baseline_alerts"] != nil {
				t.Errorf("exit = %d (%v), doc = %v; want FAIL", exitCodeForError(err), err, doc)
			}
			if !strings.Contains(stderr, tc.stderr) {
				t.Errorf("stderr = %q, want %q", stderr, tc.stderr)
			}
		})
	}
}

// control-plane v1.23.0-rc.9 (2026-11-19): prod's check exited 1 on one
// alert_firing finding, IssueSpike for an issue first seen 3.5h before the
// promotion in the previous build and firing at the same rate under the
// release; the IssueSpike instance first fired after the promotion, so the
// history had nothing to discount. The check reads the issue the alert names,
// its events by version from the baseline window's start to the after
// window's end, and holds the answer to the pre-existing-alert rule: the
// finding warns, the alert is listed in pre_existing_alerts, and the check
// passes. The same issue firing 3x as often, or an issue it cannot read,
// still fails.
func TestObsCheckAlertOnAPreExistingIssueDoesNotFail(t *testing.T) {
	const spike = `{"rule":"IssueSpike","fingerprint":"s1","firingAt":"2026-10-22T11:45:00Z","labels":{"severity":"warning","issue_id":"7f55","first_build":"v1","service":"workspace-controller"}}`
	const finding = `{"code":"RELEASE_FINDING_CODE_ALERT_FIRING","value":1,"threshold":1,"policy":"fail","message":"alert IssueSpike opened after the promotion and is still firing"}`
	reply := strings.Replace(healthReply("RELEASE_VERDICT_FAIL", true, finding, ""), `"verdict"`, `"firingAlerts":[`+spike+`],"verdict"`, 1)
	// ~30 an hour on v1 since 08:00, and ~23 on v2 after the 11:40
	// promotion. The issue is read twice: the after window first, then the
	// baseline's whole hours before the promotion.
	const header = `{"issue":{"id":"7f55","service":"workspace-controller","firstSeenAt":"2026-10-22T08:00:00Z","firstVersion":"v1"},"byVersion":[`
	before := header + `{"version":"v1","events":"90","firstSeenAt":"2026-10-22T08:00:00Z","lastSeenAt":"2026-10-22T10:59:00Z"}]}`
	issue := func(v2Events int, v2Last string) []string {
		return []string{fmt.Sprintf(header+
			`{"version":"v1","events":"12","firstSeenAt":"2026-10-22T11:00:00Z","lastSeenAt":"2026-10-22T11:42:00Z"},`+
			`{"version":"v2","events":"%d","firstSeenAt":"2026-10-22T11:41:00Z","lastSeenAt":"%s"}]}`, v2Events, v2Last), before}
	}
	run := func(t *testing.T, issueReplies []string, errs map[string]error) (map[string]any, string, *seqCaller, error) {
		t.Helper()
		caller := &seqCaller{replies: map[string][]string{procHealth: {reply}, procGetIssueCheck: issueReplies}, errs: errs}
		out, stderr, err := newObsCheckHarness().run(t, cloudFacts(), caller, nil, "check", "prod", "--json")
		return decode(t, out), stderr, caller, err
	}

	doc, _, caller, err := run(t, issue(6, "2026-10-22T11:54:00Z"), nil)
	if err != nil {
		t.Fatalf("exit = %d (%v), want 0; doc = %v", exitCodeForError(err), err, doc)
	}
	fs, _ := doc["findings"].([]any)
	if doc["verdict"] != "PASS" || len(fs) != 1 || doc["firing_alerts"] != nil {
		t.Fatalf("doc = %v", doc)
	}
	if f := fs[0].(map[string]any); f["policy"] != "warn" || f["pre_existing"] != true ||
		!strings.Contains(fmt.Sprint(f["unattributed"]), "pre-existing alert (first seen v1, rate unchanged)") {
		t.Errorf("finding = %v, want a pre-existing warn", f)
	}
	if pa, _ := doc["pre_existing_alerts"].([]any); len(pa) != 1 || pa[0].(map[string]any)["issue"].(map[string]any)["id"] != "7f55" {
		t.Errorf("pre_existing_alerts = %v", doc["pre_existing_alerts"])
	}
	var bodies []string
	for _, c := range caller.calls {
		if c.procedure == procGetIssueCheck {
			bodies = append(bodies, c.body)
		}
	}
	// The after window from the promotion to its end, then the baseline's
	// whole hours: from the first hour inside the 24h before 11:40 to the
	// hour holding the promotion, which the rollout shares.
	if len(bodies) != 2 || !strings.Contains(bodies[0], `"issueId":"7f55"`) ||
		!strings.Contains(bodies[0], `"start":"2026-10-22T11:40:00Z"`) || !strings.Contains(bodies[0], `"end":"2026-10-22T11:55:00Z"`) ||
		!strings.Contains(bodies[1], `"start":"2026-10-21T12:00:00Z"`) || !strings.Contains(bodies[1], `"end":"2026-10-22T11:00:00Z"`) {
		t.Errorf("GetIssue requests = %v", bodies)
	}

	for name, tc := range map[string]struct {
		issue  []string
		errs   map[string]error
		stderr string
	}{
		"the issue fires 3x as often": {issue: issue(18, "2026-10-22T11:54:00Z")},
		// The hour the after window ends in, counted past its end: the
		// window's own events cannot be told from the later ones.
		"the release's events reach past the after window": {issue: issue(9, "2026-10-22T12:30:00Z")},
		"the issue unreadable":                             {issue: issue(6, "2026-10-22T11:54:00Z"), errs: map[string]error{procGetIssueCheck: errors.New("permission denied")}, stderr: "could not read issue 7f55, so the alert that names it counts"},
	} {
		t.Run(name, func(t *testing.T) {
			doc, stderr, _, err := run(t, tc.issue, tc.errs)
			if exitCodeForError(err) != exitWrong || doc["verdict"] != "FAIL" || doc["pre_existing_alerts"] != nil {
				t.Errorf("exit = %d (%v), doc = %v; want FAIL", exitCodeForError(err), err, doc)
			}
			if !strings.Contains(stderr, tc.stderr) {
				t.Errorf("stderr = %q, want %q", stderr, tc.stderr)
			}
		})
	}
}

// control-plane v1.20.0-rc.4 (2026-11-14): a control plane that predates
// regressed_issue_min_events failed prod on one event of an old transient
// signature, and on the IssueRegressed and NewIssue alerts it and a 1-event
// issue of an older client build raised. The check holds the answer to the
// issue thresholds and judges each issue alert as its issue; a regression at
// the bar, or --threshold regressed_issue_min_events=1, still fails.
func TestObsCheckIssueBelowItsThresholdAndItsAlertsDoNotFail(t *testing.T) {
	const alerts = `"firingAlerts":[` +
		`{"rule":"IssueRegressed","fingerprint":"r1","firingAt":"2026-10-22T11:45:00Z","labels":{"severity":"warning","issue_id":"ISS-R"}},` +
		`{"rule":"NewIssue","fingerprint":"n1","firingAt":"2026-10-22T11:45:00Z","labels":{"severity":"warning","issue_id":"web-1","first_build":"1.18.0"}}],`
	findings := `{"code":"RELEASE_FINDING_CODE_REGRESSED_ISSUE","value":%d,"threshold":1,"policy":"fail","message":"issue ISS-R regressed: unavailable"},` +
		`{"code":"RELEASE_FINDING_CODE_ALERT_FIRING","value":1,"threshold":1,"policy":"fail","message":"alert IssueRegressed opened after the promotion and is still firing"},` +
		`{"code":"RELEASE_FINDING_CODE_ALERT_FIRING","value":1,"threshold":1,"policy":"fail","message":"alert NewIssue opened after the promotion and is still firing"}`
	reply := func(events int) string {
		return healthReply("RELEASE_VERDICT_FAIL", true, fmt.Sprintf(findings, events),
			fmt.Sprintf(`"regressedIssues":[{"id":"ISS-R","service":"daemon","title":"unavailable","events":"%d","regressedAt":"2026-10-22T11:44:00Z"}],`, events)+alerts)
	}
	run := func(t *testing.T, events int, args ...string) (map[string]any, error) {
		t.Helper()
		caller := &seqCaller{replies: map[string][]string{procHealth: {reply(events)}}}
		out, _, err := newObsCheckHarness().run(t, cloudFacts(), caller, nil, append([]string{"check", "prod", "--json"}, args...)...)
		return decode(t, out), err
	}

	doc, err := run(t, 1)
	if err != nil {
		t.Fatalf("exit = %d (%v), want 0; doc = %v", exitCodeForError(err), err, doc)
	}
	if doc["verdict"] != "PASS" || len(doc["findings"].([]any)) != 0 || doc["regressed_issues"] != nil || doc["firing_alerts"] != nil {
		t.Errorf("doc = %v", doc)
	}
	if ia, _ := doc["issue_alerts"].([]any); len(ia) != 2 {
		t.Errorf("issue_alerts = %v", doc["issue_alerts"])
	}
	if ths := fmt.Sprint(doc["thresholds"]); !strings.Contains(ths, "map[name:regressed_issue_min_events source:default value:3]") {
		t.Errorf("thresholds = %s", ths)
	}

	if doc, err = run(t, 3); exitCodeForError(err) != exitWrong || doc["verdict"] != "FAIL" || !strings.Contains(fmt.Sprint(doc["findings"]), "regressed_issue") {
		t.Errorf("a 3-event regression: exit %d doc %v", exitCodeForError(err), doc)
	}
	if strings.Contains(fmt.Sprint(doc["findings"]), "alert_firing") {
		t.Errorf("the regression's alert is counted beside it: %v", doc["findings"])
	}
	if doc, err = run(t, 1, "--threshold", "regressed_issue_min_events=1"); exitCodeForError(err) != exitWrong || doc["verdict"] != "FAIL" {
		t.Errorf("regressed_issue_min_events=1 is the old rule: exit %d doc %v", exitCodeForError(err), doc)
	}
}

// control-plane v1.20.1-rc.1 (2026-11-15, control-plane #2820): a control
// plane that predates error_rate_min_errors failed prod on reliant-api-server
// going 0.0% -> 3.3%, 8 errors of 246 requests, all one daemon's terminal
// websockets the quiet before window held none of. The check holds the answer
// to the floor and says PASS; a real rise (40 errors), or --threshold
// error_rate_min_errors=1, still fails.
func TestObsCheckErrorRateBelowItsMinErrorsDoesNotFail(t *testing.T) {
	const finding = `{"code":"RELEASE_FINDING_CODE_ERROR_RATE","service":"reliant-api-server","value":%g,"threshold":0.01,"policy":"fail","message":"reliant-api-server: error rate 0.0%% -> %.1f%%"}`
	reply := func(errs int) string {
		rate := float64(errs) / 246
		r := healthReply("RELEASE_VERDICT_FAIL", true, fmt.Sprintf(finding, rate, rate*100), "")
		return strings.Replace(r,
			`"services":[{"service":"api","before":{"requests":"900","errorRate":0.001,"p95Ms":100},"after":{"requests":"1000","errorRate":0.05,"p95Ms":110},"versionShare":1}]`,
			fmt.Sprintf(`"services":[{"service":"reliant-api-server","before":{"requests":"210","errorRate":0,"p95Ms":100},"after":{"requests":"246","errorRate":%g,"p95Ms":110},"versionShare":1}]`, rate), 1)
	}
	run := func(t *testing.T, errs int, args ...string) (map[string]any, error) {
		t.Helper()
		caller := &seqCaller{replies: map[string][]string{procHealth: {reply(errs)}}}
		out, _, err := newObsCheckHarness().run(t, cloudFacts(), caller, nil, append([]string{"check", "prod", "--json"}, args...)...)
		return decode(t, out), err
	}

	doc, err := run(t, 8)
	if err != nil {
		t.Fatalf("exit = %d (%v), want 0; doc = %v", exitCodeForError(err), err, doc)
	}
	if doc["verdict"] != "PASS" || len(doc["findings"].([]any)) != 0 {
		t.Errorf("doc = %v", doc)
	}
	if ths := fmt.Sprint(doc["thresholds"]); !strings.Contains(ths, "map[name:error_rate_min_errors source:default value:10]") {
		t.Errorf("thresholds = %s", ths)
	}
	if doc, err = run(t, 40); exitCodeForError(err) != exitWrong || doc["verdict"] != "FAIL" || !strings.Contains(fmt.Sprint(doc["findings"]), "error_rate") {
		t.Errorf("40 errors: exit %d doc %v", exitCodeForError(err), doc)
	}
	if doc, err = run(t, 8, "--threshold", "error_rate_min_errors=1"); exitCodeForError(err) != exitWrong || doc["verdict"] != "FAIL" {
		t.Errorf("error_rate_min_errors=1 is the old rule: exit %d doc %v", exitCodeForError(err), doc)
	}
}

// An app adopted by its Dockerfile exports no OpenTelemetry unless it was
// instrumented, so the control plane answers with no service at all however
// much traffic it served. Measured: 120 real requests to an adopted Express
// app were reported as "no service had at least 50 requests". The verdict
// stays INCONCLUSIVE (exit 2: not proven good), and the document now says
// why in a code a gate can branch on and a reason that names the app.
func TestObsCheckNoRequestTelemetryNamesTheSelfInstrumentedService(t *testing.T) {
	reply := strings.Replace(healthReply("RELEASE_VERDICT_INCONCLUSIVE", true, "", ""),
		`"services":[{"service":"api","before":{"requests":"900","errorRate":0.001,"p95Ms":100},"after":{"requests":"1000","errorRate":0.05,"p95Ms":110},"versionShare":1}],`,
		`"services":[],`, 1)
	facts := cloudFacts()
	facts.SelfInstrumented = []string{"app"}
	caller := &seqCaller{replies: map[string][]string{procHealth: {reply}}}
	out, _, err := newObsCheckHarness().run(t, facts, caller, nil, "check", "prod", "--json")
	if got := exitCodeForError(err); got != 2 {
		t.Fatalf("exit = %d, want 2 (not proven good): %v", got, err)
	}
	doc := decode(t, out)
	if doc["verdict"] != "INCONCLUSIVE" || doc["reason_code"] != "no_request_telemetry" {
		t.Errorf("verdict %v, reason_code %v; want INCONCLUSIVE / no_request_telemetry", doc["verdict"], doc["reason_code"])
	}
	reason := fmt.Sprint(doc["reason"])
	for _, want := range []string{"no request telemetry", "app exports none unless it is instrumented", "OTEL_EXPORTER_OTLP_ENDPOINT"} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason does not say %q: %s", want, reason)
		}
	}
	if !strings.Contains(fmt.Sprint(doc["suggested"]), "obs workloads prod") {
		t.Errorf("suggested = %v, want what forge sees without telemetry (obs workloads)", doc["suggested"])
	}
	if !strings.Contains(err.Error(), "no request telemetry") {
		t.Errorf("the exit message does not carry the reason: %v", err)
	}
}
