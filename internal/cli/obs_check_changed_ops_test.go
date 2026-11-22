package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/releasehealth"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// checkP is the promotion every changed-op test judges: 11:40, so the before
// window is [11:25, 11:40) and the after window [11:40, 11:55).
var checkP = time.Date(2026, 10, 22, 11, 40, 0, 0, time.UTC)

const (
	notifySend   = "notify.Send"
	createTodo   = "todo.v1.TodoService/CreateTodo"
	commitNotify = "3f2c1ab9e0d4c5b6a7f8091a2b3c4d5e6f708192"
	procPanel    = "controlplane.v1.ObservabilityService/QueryPanel"
)

// fakeOpsReader answers each window's ops read with its figures, and records
// the requests.
type fakeOpsReader struct {
	before, after []obsbackend.OpStats
	err           error
	reqs          []obsbackend.OpsRequest
}

func (f *fakeOpsReader) Ops(_ context.Context, req obsbackend.OpsRequest) (obsbackend.OpsResult, error) {
	f.reqs = append(f.reqs, req)
	if f.err != nil {
		return obsbackend.OpsResult{}, f.err
	}
	if req.Range.From.Before(checkP) {
		return obsbackend.OpsResult{Ops: f.before}, nil
	}
	return obsbackend.OpsResult{Ops: f.after}, nil
}

// opsLocal is the local backend with its OpsReader replaced.
type opsLocal struct {
	obsbackend.Local
	ops obsbackend.OpsReader
}

func (o opsLocal) Ops(ctx context.Context, req obsbackend.OpsRequest) (obsbackend.OpsResult, error) {
	return o.ops.Ops(ctx, req)
}

// fixedOpChanges is a changed-op source that says ops changed, and records
// the subject it was asked about.
func fixedOpChanges(asked *[]releasehealth.Subject, ops ...changedOp) opChangeFunc {
	return func(_ context.Context, _ string, s releasehealth.Subject) opChanges {
		*asked = append(*asked, s)
		return opChanges{Read: true, Base: "base0123", Ops: ops}
	}
}

func notifyOp() changedOp {
	return changedOp{Name: notifySend, Kind: releasehealth.OpKindComponent,
		Commits: []releasehealth.OpCommit{{SHA: commitNotify, Subject: "fix(notify): retry (#42)", PR: 42}}}
}

func stats(svc, name string, calls, errRatio, p95 float64) obsbackend.OpStats {
	return obsbackend.OpStats{Service: svc, Name: name, Calls: calls, Errors: calls * errRatio, ErrorRatio: errRatio, P50Ms: p95 / 2, P95Ms: p95, P99Ms: p95 * 2}
}

func equalTraffic() *healthRunner {
	return &healthRunner{
		before: obsbackend.Result{Rows: [][]any{redRow("api", 1000, 10, 100)}},
		after:  obsbackend.Result{Rows: [][]any{redRow("api", 1000, 10, 100)}},
	}
}

func findingOf(doc map[string]any, code string) map[string]any {
	for _, f := range doc["findings"].([]any) {
		if m := f.(map[string]any); m["code"] == code {
			return m
		}
	}
	return nil
}

func TestObsCheckLocalChangedOpErrorsWarn(t *testing.T) {
	var asked []releasehealth.Subject
	ops := &fakeOpsReader{
		before: []obsbackend.OpStats{stats("api", notifySend, 200, 0.01, 10)},
		after:  []obsbackend.OpStats{stats("api", notifySend, 200, 0.2, 12)},
	}
	h := newObsCheckHarness()
	h.opChanges = fixedOpChanges(&asked, notifyOp())
	h.wrapLocal = func(l obsbackend.Local) obsbackend.Backend { return opsLocal{l, ops} }
	out, _, err := h.run(t, devFacts(), nil, equalTraffic(), "check", "dev", "--since", "20m", "--json")
	if err != nil {
		t.Fatalf("a changed op that regressed warns by default: %v\n%s", err, out)
	}
	if len(asked) != 1 || !asked[0].PromotedAt.Equal(checkP) || promotionSubject(asked[0]) {
		t.Errorf("the source is asked once, about the local instant: %+v", asked)
	}
	// One read per window, of the changed ops' names alone.
	if len(ops.reqs) != 2 || !slices.Equal(ops.reqs[0].Names, []string{notifySend}) || ops.reqs[0].ByVersion || ops.reqs[1].ByVersion ||
		!ops.reqs[0].Range.From.Equal(checkP.Add(-15*time.Minute)) || !ops.reqs[0].Range.To.Equal(checkP) ||
		!ops.reqs[1].Range.From.Equal(checkP) || !ops.reqs[1].Range.To.Equal(checkP.Add(15*time.Minute)) {
		t.Errorf("ops reads: %+v", ops.reqs)
	}
	doc := decode(t, out)
	if doc["verdict"] != "PASS" {
		t.Errorf("verdict = %v", doc["verdict"])
	}
	f := findingOf(doc, release.HealthChangedOpErrors)
	if f == nil || f["policy"] != releasehealth.PolicyWarn || f["op"] != notifySend || f["commit"] != commitNotify || f["pr"] != float64(42) || f["service"] != "api" {
		t.Fatalf("changed_op_errors finding = %v", f)
	}
	if msg := fmt.Sprint(f["message"]); !strings.HasSuffix(msg, "changed in 3f2c1ab (#42)") {
		t.Errorf("message = %s", msg)
	}
	co := doc["changed_ops"].([]any)
	if len(co) != 1 {
		t.Fatalf("changed_ops = %v", co)
	}
	c := co[0].(map[string]any)
	if c["op"] != notifySend || c["service"] != "api" || c["kind"] != "component" ||
		c["before"].(map[string]any)["requests"] != float64(200) || c["after"].(map[string]any)["error_rate"] != 0.2 ||
		c["commits"].([]any)[0].(map[string]any)["sha"] != commitNotify {
		t.Errorf("changed op = %v", c)
	}
	sug := fmt.Sprint(doc["suggested"])
	for _, want := range []string{
		"forge obs traces dev --service api --filter span.attr.forge.op=notify.Send --errors",
		"forge obs blast-radius dev --base base0123 --head " + commitNotify,
	} {
		if !strings.Contains(sug, want) {
			t.Errorf("suggested lacks %q: %s", want, sug)
		}
	}
	if ths := fmt.Sprint(doc["thresholds"]); !strings.Contains(ths, releasehealth.ThresholdChangedOpMinCalls) {
		t.Errorf("the changed-op thresholds are listed: %s", ths)
	}

	// The text output names the op and its commit, the codes aligned.
	h2 := newObsCheckHarness()
	h2.opChanges, h2.wrapLocal = h.opChanges, h.wrapLocal
	text, _, err := h2.run(t, devFacts(), nil, equalTraffic(), "check", "dev", "--since", "20m")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"CHANGED OP", "notify.Send", "3f2c1ab (#42)", "warn changed_op_errors notify.Send (api): error rate"} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
}

func TestObsCheckLocalChangedOpFailsWhenFailOnNamesIt(t *testing.T) {
	var asked []releasehealth.Subject
	ops := &fakeOpsReader{
		before: []obsbackend.OpStats{stats("api", notifySend, 200, 0.01, 10)},
		after:  []obsbackend.OpStats{stats("api", notifySend, 200, 0.2, 12)},
	}
	h := newObsCheckHarness()
	h.opChanges = fixedOpChanges(&asked, notifyOp())
	h.wrapLocal = func(l obsbackend.Local) obsbackend.Backend { return opsLocal{l, ops} }
	out, _, err := h.run(t, devFacts(), nil, equalTraffic(), "check", "dev", "--since", "20m", "--json", "--fail-on", "error_rate,changed_op_errors")
	if exitCodeForError(err) != exitWrong {
		t.Fatalf("exit = %d (%v)", exitCodeForError(err), err)
	}
	doc := decode(t, out)
	if f := findingOf(doc, release.HealthChangedOpErrors); doc["verdict"] != "FAIL" || f == nil || f["policy"] != releasehealth.PolicyFail {
		t.Errorf("verdict %v, finding %v", doc["verdict"], f)
	}
	if sug := fmt.Sprint(doc["suggested"]); !strings.Contains(sug, "--filter span.attr.forge.op=notify.Send --errors") || !strings.Contains(sug, "forge obs endpoints dev --sort errors") {
		t.Errorf("a FAIL suggests the op's traces with the rest: %s", sug)
	}
}

// panelReply is a QueryPanel answer of one ops-server row.
func panelReply(svc, name string, calls, errs, p95 float64) string {
	return `{"rows":[` + panelRow(svc, name, "", calls, errs, p95) + `]}`
}

// afterReply is a hosted after window's QueryPanel answer, read per version:
// one row of the judged release, v2 (cloudFacts).
func afterReply(svc, name string, calls, errs, p95 float64) string {
	return `{"rows":[` + panelRow(svc, name, "v2", calls, errs, p95) + `]}`
}

// beforeReply is a hosted before window's QueryPanel answer, read per
// version: one row of the previous release, v1 (cloudFacts).
func beforeReply(svc, name string, calls, errs, p95 float64) string {
	return `{"rows":[` + panelRow(svc, name, "v1", calls, errs, p95) + `]}`
}

// rowsReply is a QueryPanel answer of the given panelRow rows.
func rowsReply(rows ...string) string {
	return `{"rows":[` + strings.Join(rows, ",") + `]}`
}

// panelRow is one ops-server row; version "" is a row of spans with no
// service.version (or a panel not grouped by it).
func panelRow(svc, name, version string, calls, errs, p95 float64) string {
	ver := ""
	if version != "" {
		ver = fmt.Sprintf(`,"resource.attr.service.version":%q`, version)
	}
	return fmt.Sprintf(`{"group":{"service.name":%q,"span.name":%q%s},"values":[%g,%g,%g,%g,%g]}`, svc, name, ver, calls, errs, p95/2, p95, p95*2)
}

// A control plane that predates the changed-op rule sends no ops: the CLI
// reads them itself, and --fail-on still re-judges the rest of the answer.
func TestObsCheckHostedAnswerGetsItsChangedOps(t *testing.T) {
	reply := healthReply("RELEASE_VERDICT_PASS", true, latencyFinding, "")
	rpc := changedOp{Name: createTodo, Kind: releasehealth.OpKindRPC, Commits: []releasehealth.OpCommit{{SHA: "c0ffee1234567", Subject: "perf: batch writes"}}}
	run := func(t *testing.T, args ...string) (int, map[string]any, *seqCaller, []releasehealth.Subject) {
		var asked []releasehealth.Subject
		caller := &seqCaller{replies: map[string][]string{procHealth: {reply},
			procPanel: {beforeReply("api", createTodo, 400, 0, 20), afterReply("api", createTodo, 400, 0, 90)}}}
		h := newObsCheckHarness()
		h.opChanges = fixedOpChanges(&asked, rpc)
		out, _, err := h.run(t, cloudFacts(), caller, nil, append([]string{"check", "prod", "--json"}, args...)...)
		return exitCodeForError(err), decode(t, out), caller, asked
	}

	code, doc, caller, asked := run(t)
	if code != 0 || doc["verdict"] != "PASS" {
		t.Fatalf("env policy: exit %d, %v", code, doc["verdict"])
	}
	if len(asked) != 1 || asked[0].Release != "v2" || asked[0].GitCommit != "abc123" || asked[0].PreviousRelease != "v1" {
		t.Errorf("the source is asked about the promotion: %+v", asked)
	}
	f := findingOf(doc, release.HealthChangedOpLatency)
	if f == nil || f["policy"] != releasehealth.PolicyWarn || f["op"] != createTodo || f["value"] != 4.5 {
		t.Fatalf("changed_op_latency = %v", f)
	}
	var panels int
	for _, c := range caller.calls {
		if c.procedure == procPanel {
			panels++
			if !strings.Contains(c.body, createTodo) || !strings.Contains(c.body, "ops-server") {
				t.Errorf("panel request: %s", c.body)
			}
		}
	}
	if panels != 2 {
		t.Errorf("QueryPanel ran %d times, want once per window", panels)
	}
	if sug := fmt.Sprint(doc["suggested"]); !strings.Contains(sug, "forge obs traces prod --service api --route "+createTodo+" --slow") {
		t.Errorf("suggested = %s", sug)
	}

	// --fail-on re-judges the control plane's latency finding (Rejudge),
	// and the changed-op finding follows the same set.
	code, doc, _, _ = run(t, "--fail-on", "latency")
	if code != exitWrong || doc["verdict"] != "FAIL" {
		t.Fatalf("--fail-on latency: exit %d, %v", code, doc["verdict"])
	}
	if f := findingOf(doc, release.HealthLatency); f == nil || f["policy"] != releasehealth.PolicyFail {
		t.Errorf("latency = %v", f)
	}
	if f := findingOf(doc, release.HealthChangedOpLatency); f == nil || f["policy"] != releasehealth.PolicyWarn {
		t.Errorf("changed_op_latency under --fail-on latency = %v", f)
	}
	code, doc, _, _ = run(t, "--fail-on", "changed_op_latency")
	if f := findingOf(doc, release.HealthChangedOpLatency); code != exitWrong || f == nil || f["policy"] != releasehealth.PolicyFail {
		t.Errorf("--fail-on changed_op_latency: exit %d, %v", code, f)
	}
	if f := findingOf(doc, release.HealthLatency); f == nil || f["policy"] != releasehealth.PolicyWarn {
		t.Errorf("latency under --fail-on changed_op_latency = %v", f)
	}
}

// A changed op that regressed keeps --early-pass waiting for the whole
// window, as any finding does; the diff is read once however many polls.
func TestObsCheckChangedOpBlocksAnEarlyPass(t *testing.T) {
	var asked []releasehealth.Subject
	before, after := beforeReply("api", createTodo, 400, 0, 20), afterReply("api", createTodo, 400, 0, 90)
	caller := &seqCaller{replies: map[string][]string{
		procHealth: {healthReply("RELEASE_VERDICT_INCONCLUSIVE", false, "", ""), healthReply("RELEASE_VERDICT_PASS", true, "", "")},
		procPanel:  {before, after, before, after},
	}}
	h := newObsCheckHarness()
	h.opChanges = fixedOpChanges(&asked, changedOp{Name: createTodo, Kind: releasehealth.OpKindRPC})
	out, errOut, err := h.run(t, cloudFacts(), caller, nil, "check", "prod", "--wait", "--early-pass", "5m", "--json")
	if err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	doc := decode(t, out)
	if h.sleeps != 1 || doc["verdict"] != "PASS" || doc["early_pass"] != nil || findingOf(doc, release.HealthChangedOpLatency) == nil {
		t.Errorf("sleeps %d, verdict %v, early_pass %v, findings %v", h.sleeps, doc["verdict"], doc["early_pass"], doc["findings"])
	}
	if len(asked) != 1 {
		t.Errorf("the diff was read %d times, want once", len(asked))
	}
}

// --explain lists the changed ops' statements with the rest, and runs none.
func TestObsCheckExplainListsTheChangedOpStatements(t *testing.T) {
	var asked []releasehealth.Subject
	hr := &healthRunner{}
	h := newObsCheckHarness()
	h.opChanges = fixedOpChanges(&asked, notifyOp())
	out, _, err := h.run(t, devFacts(), nil, hr, "check", "dev", "--since", "20m", "--explain", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if len(hr.ran) != 0 {
		t.Errorf("--explain ran %d statements", len(hr.ran))
	}
	var purposes []string
	for _, s := range decode(t, out)["statements"].([]any) {
		purposes = append(purposes, fmt.Sprint(s.(map[string]any)["purpose"]))
	}
	if n := strings.Count(strings.Join(purposes, "|"), "ops component"); n != 2 {
		t.Errorf("want the component-ops statement once per window, got %d: %v", n, purposes)
	}
}

// The changed-op thresholds are applied by the CLI, never sent: an older
// control plane refuses names it does not know.
func TestObsCheckHoldsTheChangedOpThresholdsBackFromTheControlPlane(t *testing.T) {
	var asked []releasehealth.Subject
	caller := &seqCaller{replies: map[string][]string{procHealth: {healthReply("RELEASE_VERDICT_PASS", true, "", "")},
		procPanel: {beforeReply("api", createTodo, 30, 0, 20), afterReply("api", createTodo, 30, 0, 90)}}}
	h := newObsCheckHarness()
	h.opChanges = fixedOpChanges(&asked, changedOp{Name: createTodo, Kind: releasehealth.OpKindRPC})
	out, _, err := h.run(t, cloudFacts(), caller, nil, "check", "prod", "--json",
		"--threshold", "min_requests=20", "--threshold", "changed_op_min_calls=25")
	if err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	_ = json.Unmarshal([]byte(caller.calls[0].body), &sent)
	if ov, _ := sent["thresholdOverrides"].(map[string]any); ov["min_requests"] != "20" || ov[releasehealth.ThresholdChangedOpMinCalls] != nil {
		t.Errorf("thresholdOverrides sent = %v", sent["thresholdOverrides"])
	}
	doc := decode(t, out)
	// 30 calls a window clears 25: the op is compared, and its latency found.
	if findingOf(doc, release.HealthChangedOpLatency) == nil {
		t.Errorf("the override was not applied: %v", doc["findings"])
	}
	if ths := fmt.Sprint(doc["thresholds"]); !strings.Contains(ths, "name:changed_op_min_calls source:request value:25") {
		t.Errorf("thresholds = %s", ths)
	}
}

func TestObsCheckChangedOpsNotEvaluated(t *testing.T) {
	var asked []releasehealth.Subject
	thin := &fakeOpsReader{
		before: []obsbackend.OpStats{stats("api", notifySend, 10, 0, 10)},
		after:  []obsbackend.OpStats{stats("api", notifySend, 10, 0.5, 90)},
	}
	for name, tc := range map[string]struct {
		changes opChangeFunc
		wrap    func(obsbackend.Local) obsbackend.Backend
		want    string
	}{
		"thin windows": {fixedOpChanges(&asked, notifyOp()), func(l obsbackend.Local) obsbackend.Backend { return opsLocal{l, thin} },
			"changed_op: 1 op(s) below changed_op_min_calls (50 calls in each window): notify.Send"},
		"no OpsReader": {fixedOpChanges(&asked, notifyOp()), func(l obsbackend.Local) obsbackend.Backend {
			return struct {
				obsbackend.Backend
				obsbackend.HealthReader
			}{l, l}
		}, "changed_op: the local backend cannot read per-op figures"},
		"the read fails": {fixedOpChanges(&asked, notifyOp()), func(l obsbackend.Local) obsbackend.Backend {
			return opsLocal{l, &fakeOpsReader{err: errors.New("clickhouse is down")}}
		}, "changed_op: the changed ops' figures could not be read: clickhouse is down"},
		"no checkout": {noCheckoutOpChanges, nil, "changed_op: there is no checkout here"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newObsCheckHarness()
			h.opChanges, h.wrapLocal = tc.changes, tc.wrap
			out, _, err := h.run(t, devFacts(), nil, equalTraffic(), "check", "dev", "--since", "20m", "--json")
			if err != nil {
				t.Fatalf("a changed op the check cannot judge never fails it: %v", err)
			}
			doc := decode(t, out)
			if ne := fmt.Sprint(doc["not_evaluated"]); !strings.Contains(ne, tc.want) {
				t.Errorf("not_evaluated = %s, want %q", ne, tc.want)
			}
			if doc["verdict"] != "PASS" || findingOf(doc, release.HealthChangedOpErrors) != nil {
				t.Errorf("verdict %v, findings %v", doc["verdict"], doc["findings"])
			}
		})
	}
}

// Nothing changed since P: no changed ops, and no not_evaluated noise.
func TestObsCheckNothingChangedSaysNothing(t *testing.T) {
	h := newObsCheckHarness()
	h.opChanges = func(context.Context, string, releasehealth.Subject) opChanges {
		return opChanges{Read: true, Base: "b"}
	}
	out, _, err := h.run(t, devFacts(), nil, equalTraffic(), "check", "dev", "--since", "20m", "--json")
	if err != nil {
		t.Fatal(err)
	}
	doc := decode(t, out)
	if _, has := doc["changed_ops"]; has || strings.Contains(fmt.Sprint(doc["not_evaluated"]), releasehealth.NotEvaluatedChangedOps) {
		t.Errorf("changed_ops %v, not_evaluated %v", doc["changed_ops"], doc["not_evaluated"])
	}
}

func TestObsCheckChangedOpFields(t *testing.T) {
	var asked []releasehealth.Subject
	ops := &fakeOpsReader{
		before: []obsbackend.OpStats{stats("api", notifySend, 200, 0.01, 10), stats("worker", notifySend, 100, 0, 10)},
		after:  []obsbackend.OpStats{stats("api", notifySend, 200, 0.2, 12), stats("worker", notifySend, 100, 0, 10)},
	}
	h := newObsCheckHarness()
	h.opChanges = fixedOpChanges(&asked, notifyOp())
	h.wrapLocal = func(l obsbackend.Local) obsbackend.Backend { return opsLocal{l, ops} }
	out, _, err := h.run(t, devFacts(), nil, equalTraffic(), "check", "dev", "--since", "20m", "--json",
		"--fields", "changed_ops.service,changed_ops.after.requests,findings.op,findings.pr")
	if err != nil {
		t.Fatal(err)
	}
	doc := decode(t, out)
	// One OpHealth per service the op ran in.
	if got := fmt.Sprint(doc["changed_ops"]); got != "[map[after:map[requests:200] service:api] map[after:map[requests:100] service:worker]]" {
		t.Errorf("changed_ops = %s", got)
	}
	if got := fmt.Sprint(doc["findings"]); got != "[map[op:notify.Send pr:42]]" {
		t.Errorf("findings = %s", got)
	}
}

// On a promotion both windows are read per service.version and the changed
// op judged on the release's own after-window calls (against v1's before
// window here): errors of the old version
// still in rotation are not the release's, and an op whose spans carry no
// version is not judged at all.
func TestObsCheckHostedChangedOpsAreTheReleasesVersion(t *testing.T) {
	run := func(t *testing.T, after string) (map[string]any, *seqCaller) {
		t.Helper()
		return runHostedChangedOp(t, beforeReply("api", createTodo, 400, 0, 20), after)
	}
	panelGroupBy := func(caller *seqCaller) []string {
		var out []string
		for _, c := range caller.calls {
			if c.procedure == procPanel {
				var m map[string]any
				_ = json.Unmarshal([]byte(c.body), &m)
				out = append(out, fmt.Sprint(m["panel"].(map[string]any)["groupBy"]))
			}
		}
		return out
	}

	// v1 still serves most calls, and fails a third of them; v2 is healthy.
	doc, caller := run(t, `{"rows":[`+panelRow("api", createTodo, "v1", 600, 200, 20)+`,`+panelRow("api", createTodo, "v2", 300, 0, 20)+`]}`)
	if got := panelGroupBy(caller); fmt.Sprint(got) != "[[service.name span.name resource.attr.service.version] [service.name span.name resource.attr.service.version]]" {
		t.Errorf("panels groupBy %v: both windows per version", got)
	}
	if doc["verdict"] != "PASS" || findingOf(doc, release.HealthChangedOpErrors) != nil {
		t.Errorf("old-version errors judged against the release: %v %v", doc["verdict"], doc["findings"])
	}
	co := doc["changed_ops"].([]any)[0].(map[string]any)
	if co["after_version"] != "v2" || co["after"].(map[string]any)["requests"] != float64(300) || co["after"].(map[string]any)["error_rate"] != float64(0) {
		t.Errorf("changed op = %v: After is v2's calls", co)
	}

	// v2's own calls failing is the release's finding.
	doc, _ = run(t, `{"rows":[`+panelRow("api", createTodo, "v1", 600, 0, 20)+`,`+panelRow("api", createTodo, "v2", 300, 100, 20)+`]}`)
	if f := findingOf(doc, release.HealthChangedOpErrors); doc["verdict"] != "FAIL" || f == nil || f["unattributed"] != nil {
		t.Errorf("v2's errors: %v %v", doc["verdict"], doc["findings"])
	}

	// Spans with no service.version: not judged, and said so.
	doc, _ = run(t, panelReply("api", createTodo, 900, 200, 20))
	if doc["verdict"] != "PASS" || findingOf(doc, release.HealthChangedOpErrors) != nil {
		t.Errorf("unversioned figures were judged: %v %v", doc["verdict"], doc["findings"])
	}
	if ne := fmt.Sprint(doc["not_evaluated"]); !strings.Contains(ne, "changed_op: 1 op(s) have no after-window figures of v2 (their spans carry no service.version, or none was read per version): "+createTodo) {
		t.Errorf("not_evaluated = %s", ne)
	}
	if co := doc["changed_ops"].([]any)[0].(map[string]any); co["after_version"] != nil {
		t.Errorf("changed op = %v: no after_version", co)
	}
}

// runHostedChangedOp runs `forge obs check prod` (release v2, previous
// release v1; cloudFacts) over one changed rpc whose before and after
// windows' QueryPanel answers are before and after, failing on
// changed_op_errors.
func runHostedChangedOp(t *testing.T, before, after string) (map[string]any, *seqCaller) {
	t.Helper()
	var asked []releasehealth.Subject
	caller := &seqCaller{replies: map[string][]string{procHealth: {healthReply("RELEASE_VERDICT_PASS", true, "", "")},
		procPanel: {before, after}}}
	h := newObsCheckHarness()
	h.opChanges = fixedOpChanges(&asked, changedOp{Name: createTodo, Kind: releasehealth.OpKindRPC})
	out, _, err := h.run(t, cloudFacts(), caller, nil, "check", "prod", "--json", "--fail-on", "error_rate,changed_op_errors")
	doc := decode(t, out)
	if code, fail := exitCodeForError(err), doc["verdict"] == "FAIL"; (code == exitWrong) != fail || (code != 0 && code != exitWrong) {
		t.Fatalf("exit %d (%v) for verdict %v", code, err, doc["verdict"])
	}
	return doc, caller
}

func TestJoinOpHealthPicksTheReleasesRows(t *testing.T) {
	v := func(s obsbackend.OpStats, version string) obsbackend.OpStats { s.Version = version; return s }
	before := map[opKey][]obsbackend.OpStats{
		{"api", notifySend}:    {stats("api", notifySend, 200, 0, 10)},
		{"worker", notifySend}: {stats("worker", notifySend, 200, 0, 10)},
		{"cron", notifySend}:   {stats("cron", notifySend, 200, 0, 10)},
		{"jobs", notifySend}:   {stats("jobs", notifySend, 200, 0, 10)},
	}
	after := map[opKey][]obsbackend.OpStats{
		// v1 and v2: v2's row.
		{"api", notifySend}: {v(stats("api", notifySend, 500, 0.5, 10), "v1"), v(stats("api", notifySend, 100, 0, 10), "v2")},
		// Only another version: the release made no calls.
		{"worker", notifySend}: {v(stats("worker", notifySend, 300, 0.5, 10), "v1")},
		// No version on any span: the unscoped figures, AfterVersion "".
		{"cron", notifySend}: {stats("cron", notifySend, 300, 0.5, 10)},
		// jobs: no after row at all.
	}
	got := map[string]releasehealth.OpHealth{}
	for _, o := range joinOpHealth([]changedOp{notifyOp(), {Name: "never.Seen"}}, before, after, releasehealth.Subject{Release: "v2"}) {
		got[o.Op+"/"+o.Service] = o
	}
	for key, want := range map[string]struct {
		version string
		calls   int64
	}{
		"notify.Send/api": {"v2", 100}, "notify.Send/worker": {"v2", 0}, "notify.Send/cron": {"", 300},
		"notify.Send/jobs": {"v2", 0}, "never.Seen/": {"v2", 0},
	} {
		o, ok := got[key]
		if !ok || o.AfterVersion != want.version || o.After.Requests != want.calls || (o.Service != "" && o.Before.Requests != 200) {
			t.Errorf("%s = %+v, want after_version %q with %d calls", key, o, want.version, want.calls)
		}
	}

	// A local env names no release: the whole figures, no version.
	for _, o := range joinOpHealth([]changedOp{notifyOp()}, before, map[opKey][]obsbackend.OpStats{{"api", notifySend}: {stats("api", notifySend, 300, 0.5, 10)}}, releasehealth.Subject{}) {
		if o.AfterVersion != "" || (o.Service == "api" && o.After.Requests != 300) {
			t.Errorf("local: %+v", o)
		}
	}
}

// On a promotion the before window is version-scoped too: the op is
// compared with the version it replaced, not with every version that
// served the before window.
func TestObsCheckHostedChangedOpBaselineIsOneVersion(t *testing.T) {
	// A FALSE finding avoided: v0 (an older version still in rotation)
	// served most of the before window cleanly, v1 (the previous release)
	// failed 10% of its calls, and v2 fails 12%, about v1's level. Against
	// the aggregated before window (10 errors in 1000 calls, 1%) v2's 12%
	// is +11 points and would fire changed_op_errors; against v1 it is +2.
	doc, caller := runHostedChangedOp(t,
		rowsReply(panelRow("api", createTodo, "v0", 900, 0, 20), panelRow("api", createTodo, "v1", 100, 10, 20)),
		afterReply("api", createTodo, 300, 36, 20))
	if doc["verdict"] != "PASS" || findingOf(doc, release.HealthChangedOpErrors) != nil {
		t.Errorf("v2 at v1's error level was judged against v0's: %v %v", doc["verdict"], doc["findings"])
	}
	co := doc["changed_ops"].([]any)[0].(map[string]any)
	if co["before_version"] != "v1" || co["before"].(map[string]any)["requests"] != float64(100) || co["before"].(map[string]any)["error_rate"] != 0.1 {
		t.Errorf("changed op = %v: Before is v1's calls", co)
	}
	var panels int
	for _, c := range caller.calls {
		if c.procedure == procPanel {
			panels++
			if !strings.Contains(c.body, "resource.attr.service.version") {
				t.Errorf("a window read whole: %s", c.body)
			}
		}
	}
	if panels != 2 {
		t.Errorf("QueryPanel ran %d times, want once per window", panels)
	}

	// A TRUE finding no longer masked: v0 failed half its calls in the
	// before window, v1 none, and v2 fails 10%. The aggregated before window
	// (45%) would hide it; against v1 it is +10 points.
	doc, _ = runHostedChangedOp(t,
		rowsReply(panelRow("api", createTodo, "v0", 900, 450, 20), panelRow("api", createTodo, "v1", 100, 0, 20)),
		afterReply("api", createTodo, 300, 30, 20))
	if f := findingOf(doc, release.HealthChangedOpErrors); doc["verdict"] != "FAIL" || f == nil {
		t.Errorf("v2's regression over v1: %v %v", doc["verdict"], doc["findings"])
	}

	// The judged release's own calls before the promotion (a canary) are no
	// baseline either: only v2 and unversioned spans served the before
	// window, so the op is not judged.
	for name, before := range map[string]string{
		"no version on any before span": panelReply("api", createTodo, 400, 0, 20),
		"only the canary":               rowsReply(panelRow("api", createTodo, "v2", 100, 0, 20), panelRow("api", createTodo, "", 50, 0, 20)),
	} {
		t.Run(name, func(t *testing.T) {
			doc, _ := runHostedChangedOp(t, before, afterReply("api", createTodo, 300, 100, 20))
			if doc["verdict"] != "PASS" || findingOf(doc, release.HealthChangedOpErrors) != nil {
				t.Errorf("judged with no baseline: %v %v", doc["verdict"], doc["findings"])
			}
			want := "changed_op: 1 op(s) have no before-window figures of the previous release v1 to compare with (none of their before-window spans carries a service.version other than v2, or none was read per version): " + createTodo
			if ne := fmt.Sprint(doc["not_evaluated"]); !strings.Contains(ne, want) {
				t.Errorf("not_evaluated = %s, want %q", ne, want)
			}
			if co := doc["changed_ops"].([]any)[0].(map[string]any); co["before_version"] != nil || co["after_version"] != "v2" {
				t.Errorf("changed op = %v", co)
			}
		})
	}
}

func TestJoinOpHealthPicksTheBaselineVersion(t *testing.T) {
	v := func(s obsbackend.OpStats, version string) obsbackend.OpStats { s.Version = version; return s }
	row := func(svc, version string, calls float64) obsbackend.OpStats {
		return v(stats(svc, notifySend, calls, 0, 10), version)
	}
	before := map[opKey][]obsbackend.OpStats{
		// v0 the busiest, v1 the previous release, v2 the canary (busiest
		// of all, never the baseline).
		{"api", notifySend}: {row("api", "v0", 300), row("api", "v1", 100), row("api", "v2", 900)},
		// A tie: the lower version wins, so the choice is deterministic.
		{"tie", notifySend}: {row("tie", "v1", 100), row("tie", "v0", 100)},
		// Versions, none the previous release.
		{"old", notifySend}: {row("old", "v0", 100)},
		// No version at all.
		{"bare", notifySend}: {row("bare", "", 100)},
		// The canary alone.
		{"canary", notifySend}: {row("canary", "v2", 100)},
	}
	after := map[opKey][]obsbackend.OpStats{}
	for k := range before {
		after[k] = []obsbackend.OpStats{row(k.service, "v2", 100)}
	}
	after[opKey{"new", notifySend}] = []obsbackend.OpStats{row("new", "v2", 100)} // no before row
	ops := []changedOp{notifyOp(), {Name: "never.Seen"}}
	type pick struct {
		version string
		calls   int64
	}
	join := func(s releasehealth.Subject) map[string]pick {
		out := map[string]pick{}
		for _, o := range joinOpHealth(ops, before, after, s) {
			out[o.Op+"/"+o.Service] = pick{o.BeforeVersion, o.Before.Requests}
		}
		return out
	}

	// The previous release recorded: its row, or zero calls (too thin) when
	// it made none; no baseline when no span names another version.
	got := join(releasehealth.Subject{Release: "v2", PreviousRelease: "v1"})
	for key, want := range map[string]pick{
		"notify.Send/api": {"v1", 100}, "notify.Send/tie": {"v1", 100}, "notify.Send/old": {"v1", 0},
		"notify.Send/bare": {"", 100}, "notify.Send/canary": {"", 0}, "notify.Send/new": {"v1", 0}, "never.Seen/": {"v1", 0},
	} {
		if got[key] != want {
			t.Errorf("previous release v1: %s = %+v, want %+v", key, got[key], want)
		}
	}

	// None recorded (or the release itself, a re-promotion): the version
	// that served most of the op's before window, the canary excluded.
	for _, prev := range []string{"", "v2"} {
		got = join(releasehealth.Subject{Release: "v2", PreviousRelease: prev})
		for key, want := range map[string]pick{
			"notify.Send/api": {"v0", 300}, "notify.Send/tie": {"v0", 100}, "notify.Send/old": {"v0", 100},
			"notify.Send/bare": {"", 100}, "notify.Send/canary": {"", 0}, "notify.Send/new": {"", 0}, "never.Seen/": {"", 0},
		} {
			if got[key] != want {
				t.Errorf("previous release %q: %s = %+v, want %+v", prev, key, got[key], want)
			}
		}
	}

	// A local env scopes nothing.
	for key, p := range join(releasehealth.Subject{}) {
		if p.version != "" {
			t.Errorf("local: %s = %+v", key, p)
		}
	}
}

func TestPromotionSubject(t *testing.T) {
	if promotionSubject(releasehealth.Subject{PromotedAt: checkP}) || !promotionSubject(releasehealth.Subject{Release: "v2"}) {
		t.Error("a local subject is only its instant; a promotion names a release")
	}
}

// localOpBase and mapOpChanges over a real repository: the base is the
// newest commit before P, and a range this checkout does not hold is
// not_evaluated, never an error.
func TestLocalOpBaseOverGit(t *testing.T) {
	testkit.Slow(t, "runs git in a temporary repository")
	dir := t.TempDir()
	git := func(date string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("", "init", "-q", "-b", "main")
	commit := func(date, msg string) string {
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(msg), 0o644); err != nil {
			t.Fatal(err)
		}
		git(date, "add", ".")
		git(date, "commit", "-q", "-m", msg)
		return git(date, "rev-parse", "HEAD")
	}
	first := commit("2026-10-22T11:00:00Z", "first")
	commit("2026-10-22T11:50:00Z", "second")

	ctx := context.Background()
	if base, err := localOpBase(ctx, dir, checkP); err != nil || base != first {
		t.Errorf("base = %q, %v; want the commit before P %s", base, err, first)
	}
	if base, err := localOpBase(ctx, dir, checkP.Add(-2*time.Hour)); err != nil || base != "" {
		t.Errorf("no commit predates P: base = %q, %v", base, err)
	}
	ch := mapOpChanges(ctx, dir, "0123456789abcdef0123456789abcdef01234567", first)
	if ch.Read || len(ch.NotEvaluated) != 1 || !strings.HasPrefix(ch.NotEvaluated[0], "changed_op: the release's commits are not in this checkout (0123456..") {
		t.Errorf("a range outside the checkout: %+v", ch)
	}
}
