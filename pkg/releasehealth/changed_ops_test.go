package releasehealth

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

const opSHA = "3f2c1ab9e0d4c7b6a5f4e3d2c1b0a9f8e7d6c5b4"

// op is a changed op whose after figures are the judged release's own (v2,
// the release input() names) and whose before figures are v1's.
func op(name string, beforeCalls, afterCalls int64, beforeErr, afterErr, beforeP95, afterP95 float64) OpHealth {
	return OpHealth{Op: name, Service: "api", Kind: OpKindComponent, AfterVersion: "v2", BeforeVersion: "v1",
		Before:  WindowStats{Requests: beforeCalls, ErrorRate: beforeErr, P95Ms: beforeP95},
		After:   WindowStats{Requests: afterCalls, ErrorRate: afterErr, P95Ms: afterP95},
		Commits: []OpCommit{{SHA: opSHA, Subject: "fix(notify): batch sends (#42)"}, {SHA: "aaaaaaaa", Subject: "older (#7)"}}}
}

// healthy is a comparable service the ops are judged beside, so the verdict
// with no failing finding is PASS.
func healthy() ServiceHealth { return svc("api", 1000, 1000, 0, 0, 100, 100) }

func withOps(t *testing.T, ops ...OpHealth) Input {
	in := input(t, true, healthy())
	in.Ops = ops
	return in
}

func codes(fs []Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Code)
	}
	return out
}

func TestChangedOpRule(t *testing.T) {
	const thinEntry = NotEvaluatedChangedOps + " 1 op(s) below changed_op_min_calls (50 calls in each window): notify.Send"
	tests := []struct {
		name     string
		op       OpHealth
		findings []string
		thin     bool
	}{
		{"error rate rise past the limit", op("notify.Send", 100, 100, 0, 0.10, 10, 10), []string{release.HealthChangedOpErrors}, false},
		{"a rise exactly at the limit does not fire", op("notify.Send", 100, 100, 0, 0.05, 10, 10), nil, false},
		{"below changed_op_min_errors does not fire", op("notify.Send", 50, 50, 0, 0.08, 10, 10), nil, false},
		{"exactly changed_op_min_errors fires", op("notify.Send", 50, 50, 0, 0.10, 10, 10), []string{release.HealthChangedOpErrors}, false},
		{"a thin before window is not evaluated", op("notify.Send", 49, 1000, 0, 0.5, 10, 100), nil, true},
		{"a thin after window is not evaluated", op("notify.Send", 1000, 49, 0, 0.5, 10, 100), nil, true},
		{"a new op (no before calls) is not evaluated", op("notify.Send", 0, 1000, 0, 0.5, 0, 100), nil, true},
		{"exactly changed_op_min_calls is compared", op("notify.Send", 50, 50, 0, 0.5, 10, 10), []string{release.HealthChangedOpErrors}, false},
		{"p95 past the ratio", op("notify.Send", 100, 100, 0, 0, 10, 25), []string{release.HealthChangedOpLatency}, false},
		{"p95 exactly at the ratio does not fire", op("notify.Send", 100, 100, 0, 0, 10, 20), nil, false},
		{"no before p95 is no latency comparison", op("notify.Send", 100, 100, 0, 0, 0, 500), nil, false},
		{"both", op("notify.Send", 100, 100, 0.01, 0.2, 10, 50), []string{release.HealthChangedOpErrors, release.HealthChangedOpLatency}, false},
		{"an improvement is nothing", op("notify.Send", 100, 100, 0.2, 0, 50, 10), nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := Evaluate(withOps(t, tc.op))
			if got := codes(r.Findings); !slices.Equal(got, tc.findings) {
				t.Fatalf("findings = %v, want %v (%+v)", got, tc.findings, r.Findings)
			}
			for _, f := range r.Findings {
				if f.Policy != PolicyWarn {
					t.Errorf("%s policy = %s: the changed-op findings warn by default", f.Code, f.Policy)
				}
			}
			if r.Verdict != Pass {
				t.Errorf("verdict = %s: a warning, or an op too thin to compare, never moves a PASS", r.Verdict)
			}
			if got := slices.Contains(r.NotEvaluated, thinEntry); got != tc.thin {
				t.Errorf("not_evaluated = %q, want the thin entry: %v", r.NotEvaluated, tc.thin)
			}
			if !reflect.DeepEqual(r.ChangedOps, []OpHealth{tc.op}) {
				t.Errorf("changed_ops = %+v", r.ChangedOps)
			}
		})
	}
}

func TestChangedOpFindingNamesTheOpAndCommit(t *testing.T) {
	r := Evaluate(withOps(t, op("notify.Send", 100, 140, 0.01, 0.10, 12, 40)))
	if len(r.Findings) != 2 {
		t.Fatalf("findings = %+v", r.Findings)
	}
	e, l := r.Findings[0], r.Findings[1]
	for _, f := range r.Findings {
		if f.Op != "notify.Send" || f.Ref != "notify.Send" || f.Service != "api" || f.Commit != opSHA || f.PR != 42 {
			t.Errorf("finding = %+v: want op, ref, service, the newest commit and its PR", f)
		}
		if !strings.HasPrefix(f.Message, "notify.Send (api): ") || !strings.HasSuffix(f.Message, "; changed in 3f2c1ab (#42)") {
			t.Errorf("message = %q", f.Message)
		}
	}
	if !strings.Contains(e.Message, "error rate 1.0% -> 10.0% (+9.0%, limit +5.0%; 14 errors in 140 calls)") || e.Value <= 0.089 || e.Threshold != DefaultChangedOpMaxErrorRateIncrease {
		t.Errorf("errors finding = %+v", e)
	}
	if !strings.Contains(l.Message, "p95 12ms -> 40ms (3.33x, limit 2x)") || l.Threshold != DefaultChangedOpMaxP95Ratio {
		t.Errorf("latency finding = %+v", l)
	}

	// A commit whose PR the caller set wins over its subject; one with no PR
	// and no commit at all say less.
	o := op("notify.Send", 100, 100, 0, 0.2, 10, 10)
	o.Commits[0].PR = 99
	if f := Evaluate(withOps(t, o)).Findings[0]; f.PR != 99 || !strings.HasSuffix(f.Message, "(#99)") {
		t.Errorf("explicit PR: %+v", f)
	}
	o.Commits = []OpCommit{{SHA: "abc"}}
	if f := Evaluate(withOps(t, o)).Findings[0]; f.PR != 0 || !strings.HasSuffix(f.Message, "; changed in abc") {
		t.Errorf("no PR: %+v", f)
	}
	o.Commits, o.Service = nil, ""
	if f := Evaluate(withOps(t, o)).Findings[0]; f.Commit != "" || strings.Contains(f.Message, "changed in") || !strings.HasPrefix(f.Message, "notify.Send: ") {
		t.Errorf("no commit, no service: %+v", f)
	}
}

func TestChangedOpFailsWhenFailOnNamesIt(t *testing.T) {
	in := withOps(t, op("notify.Send", 100, 100, 0, 0.2, 10, 10), op("billing.Charge", 100, 100, 0, 0, 10, 50))
	in.Thresholds.FailOn = normalizeCodes(append(slices.Clone(in.Thresholds.FailOn), release.HealthChangedOpErrors))
	r := Evaluate(in)
	if r.Verdict != Fail {
		t.Fatalf("verdict = %s (%+v)", r.Verdict, r.Findings)
	}
	for _, f := range r.Findings {
		want := map[string]string{release.HealthChangedOpErrors: PolicyFail, release.HealthChangedOpLatency: PolicyWarn}[f.Code]
		if f.Policy != want {
			t.Errorf("%s policy = %s, want %s", f.Code, f.Policy, want)
		}
	}
}

func TestThinChangedOpsAreOneEntry(t *testing.T) {
	var ops []OpHealth
	for _, n := range []string{"e.E", "b.B", "d.D", "a.A", "c.C"} {
		ops = append(ops, op(n, 1, 1, 0, 0, 1, 1))
	}
	ops = append(ops, op("z.Z", 100, 100, 0, 0, 1, 1))
	r := Evaluate(withOps(t, ops...))
	want := NotEvaluatedChangedOps + " 5 op(s) below changed_op_min_calls (50 calls in each window): a.A, b.B, c.C, 2 more"
	var entries []string
	for _, e := range r.NotEvaluated {
		if strings.HasPrefix(e, NotEvaluatedChangedOps) {
			entries = append(entries, e)
		}
	}
	if !slices.Equal(entries, []string{want}) {
		t.Errorf("not_evaluated = %q, want one entry %q", r.NotEvaluated, want)
	}
}

func TestParsePR(t *testing.T) {
	for subject, want := range map[string]int{
		"fix(api): retry on reconnect (#1234)": 1234,
		"squashed(#12)":                        12,
		"  padded (#7)  ":                      7,
		"no number":                            0,
		"":                                     0,
		"not a number (#abc)":                  0,
		"zero (#0)":                            0,
		"negative (#-3)":                       0,
		"not at the end (#12) and more":        0,
		"plain parens (12)":                    0,
	} {
		if got := ParsePR(subject); got != want {
			t.Errorf("ParsePR(%q) = %d, want %d", subject, got, want)
		}
	}
}

func TestChangedOpThresholds(t *testing.T) {
	def := defaults(t)
	l := fromList(def.List())
	for name, value := range map[string]string{
		ThresholdChangedOpMinCalls: "50", ThresholdChangedOpMaxErrorRateIncrease: "0.05",
		ThresholdChangedOpMinErrors: "5", ThresholdChangedOpMaxP95Ratio: "2",
	} {
		if l[name] != (Threshold{name, value, SourceDefault}) {
			t.Errorf("%s = %+v, want %s from default", name, l[name], value)
		}
	}
	// Listed before fail_on, so require_baseline stays last.
	names := func(ths []Threshold) []string {
		var out []string
		for _, th := range ths {
			out = append(out, th.Name)
		}
		return out
	}
	got := names(def.List())
	if i, j := slices.Index(got, ThresholdChangedOpMaxP95Ratio), slices.Index(got, ThresholdFailOn); i < 0 || i+1 != j || got[len(got)-1] != ThresholdRequireBaseline {
		t.Errorf("thresholds order = %v", got)
	}
	// A hand-built Thresholds lists the defaults it is judged by.
	if l := fromList(Thresholds{MinRequests: 50}.List()); l[ThresholdChangedOpMinCalls].Value != "50" || l[ThresholdChangedOpMaxP95Ratio].Value != "2" {
		t.Errorf("zero thresholds list %+v", l)
	}

	// No release shape carries them: a non-default policy leaves them default.
	shape := release.DefaultReleaseHealth()
	shape.MinRequests = 200
	th, err := Resolve(Options{Shape: &shape})
	if err != nil {
		t.Fatal(err)
	}
	if l := fromList(th.List()); l[ThresholdChangedOpMinCalls].Source != SourceDefault || l[ThresholdMinRequests].Source != SourceReleaseShape {
		t.Errorf("shape sources: %+v", l)
	}

	overrides, err := ParseOverrides([]string{"changed_op_min_calls=200", "changed_op_max_error_rate_increase=0.1", "changed_op_min_errors=2", "changed_op_max_p95_ratio=3"})
	if err != nil {
		t.Fatal(err)
	}
	th, err = Resolve(Options{Shape: &shape, Overrides: overrides})
	if err != nil {
		t.Fatal(err)
	}
	if th.ChangedOpMinCalls != 200 || th.ChangedOpMaxErrorRateIncrease != 0.1 || th.ChangedOpMinErrors != 2 || th.ChangedOpMaxP95Ratio != 3 {
		t.Errorf("overridden: %+v", th)
	}
	for _, name := range ChangedOpThresholds {
		if s := fromList(th.List())[name].Source; s != SourceRequest {
			t.Errorf("%s source = %s, want request", name, s)
		}
	}

	for name, v := range map[string]string{
		ThresholdChangedOpMinCalls: "0", ThresholdChangedOpMinErrors: "0",
		ThresholdChangedOpMaxErrorRateIncrease: "0", ThresholdChangedOpMaxP95Ratio: "1",
	} {
		if _, err := Resolve(Options{Overrides: map[string]string{name: v}}); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s=%s: err = %v, want ErrInvalid", name, v, err)
		}
	}
	for _, bad := range []map[string]string{
		{ThresholdChangedOpMaxErrorRateIncrease: "1.5"},
		{ThresholdChangedOpMinCalls: "many"},
		{ThresholdChangedOpMaxP95Ratio: "fast"},
	} {
		if _, err := Resolve(Options{Overrides: bad}); !errors.Is(err, ErrInvalid) {
			t.Errorf("%v: err = %v, want ErrInvalid", bad, err)
		}
	}

	failOn, err := ParseFailOn("changed_op_latency, changed_op_errors,error_rate")
	if err != nil || !slices.Equal(failOn, []string{release.HealthChangedOpErrors, release.HealthChangedOpLatency, release.HealthErrorRate}) {
		t.Errorf("ParseFailOn = %v, %v", failOn, err)
	}
	if _, err := Resolve(Options{FailOn: failOn}); err != nil {
		t.Errorf("fail_on naming the changed-op codes: %v", err)
	}
}

func TestChangedOpJSON(t *testing.T) {
	raw, err := json.Marshal(Finding{Code: release.HealthChangedOpErrors, Ref: "notify.Send", Op: "notify.Send", Commit: opSHA, PR: 42, Policy: PolicyWarn})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m["op"] != "notify.Send" || m["commit"] != opSHA || m["pr"] != float64(42) {
		t.Errorf("finding JSON = %s", raw)
	}
	raw, _ = json.Marshal(Finding{Code: release.HealthErrorRate})
	for _, k := range []string{`"op"`, `"commit"`, `"pr"`} {
		if strings.Contains(string(raw), k) {
			t.Errorf("a finding with no op carries %s: %s", k, raw)
		}
	}

	raw, _ = json.Marshal(Evaluate(input(t, true, healthy())))
	if strings.Contains(string(raw), `"changed_ops"`) {
		t.Errorf("a report with no ops carries changed_ops: %s", raw)
	}
	raw, _ = json.Marshal(OpHealth{Op: "todo.v1.TodoService/CreateTodo", Kind: OpKindRPC, Commits: []OpCommit{{SHA: "abc"}}})
	if want := `{"op":"todo.v1.TodoService/CreateTodo","kind":"rpc","before":{"requests":0,"error_rate":0,"p50_ms":0,"p95_ms":0,"p99_ms":0},"after":{"requests":0,"error_rate":0,"p50_ms":0,"p95_ms":0,"p99_ms":0},"commits":[{"sha":"abc"}]}`; string(raw) != want {
		t.Errorf("op JSON =\n%s\nwant\n%s", raw, want)
	}
}

// oldAnswer is what a control plane that predates the changed-op rule
// answers: no ops, no changed-op findings, no changed-op thresholds.
func oldAnswer(t *testing.T, failOn ...string) Report {
	in := input(t, true, healthy())
	if failOn != nil {
		in.Thresholds.FailOn = normalizeCodes(failOn)
	}
	r := Evaluate(in)
	r.Thresholds = slices.DeleteFunc(r.Thresholds, func(th Threshold) bool { return slices.Contains(ChangedOpThresholds, th.Name) })
	return r
}

func TestWithChangedOpsJudgesAnOldAnswerAsEvaluateDoes(t *testing.T) {
	ops := []OpHealth{op("notify.Send", 100, 100, 0, 0.2, 10, 50), op("thin.Op", 1, 1, 0, 1, 1, 1)}
	for name, failOn := range map[string][]string{
		"default fail_on":         nil,
		"fail_on changed_op_errs": {release.HealthErrorRate, release.HealthChangedOpErrors},
	} {
		t.Run(name, func(t *testing.T) {
			in := withOps(t, ops...)
			if failOn != nil {
				in.Thresholds.FailOn = normalizeCodes(failOn)
			}
			want := Evaluate(in)
			got := WithChangedOps(oldAnswer(t, failOn...), ops, nil)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("WithChangedOps over an old answer:\n got %+v\nwant %+v", got, want)
			}
			if again := WithChangedOps(got, ops, nil); !reflect.DeepEqual(again, got) {
				t.Errorf("not idempotent:\n got %+v\nwant %+v", again, got)
			}
			if own := WithChangedOps(got, nil, nil); !reflect.DeepEqual(own, got) {
				t.Errorf("nil ops does not re-judge the report's own:\n got %+v\nwant %+v", own, got)
			}
		})
	}
	if got := WithChangedOps(oldAnswer(t, release.HealthChangedOpErrors), ops, nil).Verdict; got != Fail {
		t.Errorf("fail_on names changed_op_errors: verdict %s", got)
	}

	// Nothing changed: the old answer as judged, with the thresholds listed.
	old := oldAnswer(t)
	got := WithChangedOps(old, nil, nil)
	if got.Verdict != old.Verdict || !slices.Equal(got.Findings, old.Findings) || len(got.Thresholds) != len(old.Thresholds)+len(ChangedOpThresholds) {
		t.Errorf("no ops: %+v", got)
	}
	if l := fromList(got.Thresholds); l[ThresholdChangedOpMinCalls] != (Threshold{ThresholdChangedOpMinCalls, "50", SourceDefault}) {
		t.Errorf("thresholds %+v", got.Thresholds)
	}
}

func TestWithChangedOpsRederives(t *testing.T) {
	failing := oldAnswer(t, release.HealthChangedOpErrors)
	failing = WithChangedOps(failing, []OpHealth{op("notify.Send", 100, 100, 0, 0.2, 10, 10)}, nil)
	if failing.Verdict != Fail {
		t.Fatalf("verdict %s", failing.Verdict)
	}
	// The op got better: the finding is gone, and so is the FAIL it rested on.
	fixed := WithChangedOps(failing, []OpHealth{op("notify.Send", 100, 100, 0, 0, 10, 10)}, nil)
	if len(fixed.Findings) != 0 || fixed.Verdict != Pass {
		t.Errorf("re-derived: %s %+v", fixed.Verdict, fixed.Findings)
	}
	if none := WithChangedOps(failing, []OpHealth{}, nil); len(none.Findings) != 0 || none.Verdict != Pass || none.ChangedOps == nil {
		t.Errorf("no changed op: %s %+v", none.Verdict, none.Findings)
	}

	// An override wins over the report's value; an invalid one is ignored.
	ops := []OpHealth{op("notify.Send", 100, 100, 0, 0.2, 10, 10)}
	raised := WithChangedOps(failing, ops, map[string]string{ThresholdChangedOpMinCalls: "200"})
	if len(raised.Findings) != 0 || raised.Verdict != Pass || len(raised.NotEvaluated) != 1 || !strings.HasPrefix(raised.NotEvaluated[0], NotEvaluatedChangedOps+" 1 op(s) below changed_op_min_calls (200 ") {
		t.Errorf("min calls raised: %s %+v %q", raised.Verdict, raised.Findings, raised.NotEvaluated)
	}
	if th := fromList(raised.Thresholds)[ThresholdChangedOpMinCalls]; th != (Threshold{ThresholdChangedOpMinCalls, "200", SourceRequest}) {
		t.Errorf("threshold %+v", th)
	}
	if back := WithChangedOps(raised, ops, nil); len(back.Findings) != 0 || len(back.NotEvaluated) != 1 {
		t.Errorf("the report's own threshold (200, from the request) holds: %+v %q", back.Findings, back.NotEvaluated)
	}
	if bad := WithChangedOps(failing, ops, map[string]string{ThresholdChangedOpMinCalls: "0"}); fromList(bad.Thresholds)[ThresholdChangedOpMinCalls].Value != "50" || bad.Verdict != Fail {
		t.Errorf("an invalid override was applied: %+v", bad.Thresholds)
	}
	if failing.NotEvaluated != nil || len(failing.Findings) != 1 {
		t.Error("WithChangedOps modified its argument")
	}
}

func TestRejudgeChangedOps(t *testing.T) {
	// A report with no ops: a fail-on naming the codes changes nothing.
	plain := Evaluate(input(t, true, healthy()))
	if got := Rejudge(plain, []string{release.HealthChangedOpErrors, release.HealthChangedOpLatency}); got.Verdict != Pass || len(got.Findings) != 0 {
		t.Errorf("no ops: %s %+v", got.Verdict, got.Findings)
	}
	// With ops: the fail-on set decides each changed-op finding's policy.
	r := Evaluate(withOps(t, op("notify.Send", 100, 100, 0, 0.2, 10, 50)))
	if r.Verdict != Pass {
		t.Fatalf("verdict %s", r.Verdict)
	}
	failed := Rejudge(r, []string{release.HealthChangedOpLatency})
	if failed.Verdict != Fail {
		t.Errorf("rejudged on changed_op_latency: %s", failed.Verdict)
	}
	for _, f := range failed.Findings {
		if want := map[string]string{release.HealthChangedOpErrors: PolicyWarn, release.HealthChangedOpLatency: PolicyFail}[f.Code]; f.Policy != want {
			t.Errorf("%s policy %s, want %s", f.Code, f.Policy, want)
		}
	}
	if back := Rejudge(failed, nil); back.Verdict != Pass {
		t.Errorf("rejudged on nothing: %s", back.Verdict)
	}
	// WithChangedOps reads the fail-on the rejudge recorded.
	if again := WithChangedOps(failed, nil, nil); again.Verdict != Fail || !reflect.DeepEqual(again.Findings, failed.Findings) {
		t.Errorf("WithChangedOps after Rejudge: %s %+v", again.Verdict, again.Findings)
	}
}

// TestChangedOpsAreAttributedOnTheReleasesOwnCalls: a changed op's after
// figures are the judged release's own version's calls, so a service the
// release served little of does not take its findings off the release, as it
// does error_rate's.
func TestChangedOpsAreAttributedOnTheReleasesOwnCalls(t *testing.T) {
	low, high := 0.1, 1.0
	api := healthy()
	api.After.Requests, api.VersionShare = 100, &low
	web := svc("web", 1000, 1000, 0, 0, 100, 100)
	web.VersionShare = &high
	in := input(t, true, api, web)
	in.Ops = []OpHealth{op("notify.Send", 100, 100, 0, 0.2, 10, 10)}
	in.Thresholds.FailOn = []string{release.HealthChangedOpErrors}

	r := Evaluate(in)
	if len(r.Findings) != 1 || r.Findings[0].Code != release.HealthChangedOpErrors || r.Findings[0].Unattributed != "" || r.Findings[0].Policy != PolicyFail {
		t.Fatalf("findings = %+v, want one attributed changed_op_errors failing by fail_on", r.Findings)
	}
	if r.Verdict != Fail {
		t.Errorf("verdict %s", r.Verdict)
	}

	old := Evaluate(Input{Subject: in.Subject, Windows: in.Windows, Services: in.Services, Thresholds: in.Thresholds})
	got := WithChangedOps(old, in.Ops, nil)
	if !reflect.DeepEqual(got.Findings, r.Findings) || got.Verdict != r.Verdict || got.RolloutIncomplete != r.RolloutIncomplete {
		t.Errorf("WithChangedOps attributes as Evaluate does:\n got %s %q %+v\nwant %s %q %+v", got.Verdict, got.RolloutIncomplete, got.Findings, r.Verdict, r.RolloutIncomplete, r.Findings)
	}

	// A window that is not the live release's still moves them.
	notLive := WithLiveAt(r, r.After.To.Add(time.Minute), LiveShipped)
	if f := notLive.Findings[0]; f.Unattributed == "" || f.Policy != PolicyWarn || notLive.Verdict == Fail {
		t.Errorf("window not live: %s %+v", notLive.Verdict, notLive.Findings)
	}
}

// TestChangedOpsAreJudgedOnTheReleasesVersion is the hosted case: requests of
// the old version (v1) still in rotation push the op's error rate up while
// the judged release's own (v2) calls are healthy.
func TestChangedOpsAreJudgedOnTheReleasesVersion(t *testing.T) {
	share := 0.6
	api := healthy()
	api.VersionShare = &share
	in := input(t, true, api)
	in.Thresholds.FailOn = []string{release.HealthChangedOpErrors}
	const unversioned = NotEvaluatedChangedOps + " 1 op(s) have no after-window figures of v2 (their spans carry no service.version, or none was read per version): notify.Send"
	changedOpEntries := func(r Report) []string {
		var out []string
		for _, e := range r.NotEvaluated {
			if strings.HasPrefix(e, NotEvaluatedChangedOps) {
				out = append(out, e)
			}
		}
		return out
	}

	// The caller read the after window per version and kept v2's calls.
	scoped := op("notify.Send", 100, 100, 0, 0, 10, 10)
	in.Ops = []OpHealth{scoped}
	r := Evaluate(in)
	if len(r.Findings) != 0 || r.Verdict != Pass || len(changedOpEntries(r)) != 0 {
		t.Errorf("v2's healthy figures: %s %+v %q", r.Verdict, r.Findings, r.NotEvaluated)
	}

	// The same op's unscoped figures (v1's errors among them) are not v2's:
	// not judged, and never a share-based unattributed finding.
	mixed := op("notify.Send", 100, 400, 0, 0.225, 10, 10)
	mixed.AfterVersion = ""
	in.Ops = []OpHealth{mixed}
	r = Evaluate(in)
	if len(r.Findings) != 0 || r.Verdict != Pass {
		t.Errorf("unscoped figures were judged: %s %+v", r.Verdict, r.Findings)
	}
	if got := changedOpEntries(r); !slices.Equal(got, []string{unversioned}) {
		t.Errorf("not_evaluated = %q, want %q", got, unversioned)
	}
	if !reflect.DeepEqual(r.ChangedOps, []OpHealth{mixed}) {
		t.Errorf("changed_ops = %+v: the unjudged op is still reported", r.ChangedOps)
	}

	// Another version's figures labelled as such are not judged either, and
	// with a thin op the two entries are separate, the version one first.
	other := op("notify.Send", 100, 400, 0, 0.225, 10, 10)
	other.AfterVersion = "v1"
	thin := op("thin.Op", 1, 1, 0, 1, 1, 1)
	in.Ops = []OpHealth{other, thin}
	r = Evaluate(in)
	if got := changedOpEntries(r); len(r.Findings) != 0 || len(got) != 2 || got[0] != unversioned || !strings.Contains(got[1], "below changed_op_min_calls") {
		t.Errorf("v1 figures and a thin op: %+v %q", r.Findings, got)
	}

	// WithChangedOps: the same rule, idempotent, and equal to Evaluate.
	old := Evaluate(Input{Subject: in.Subject, Windows: in.Windows, Services: in.Services, Thresholds: in.Thresholds})
	for _, ops := range [][]OpHealth{{scoped}, {mixed}, {other, thin}, {op("notify.Send", 100, 100, 0, 0.2, 10, 10), mixed}} {
		in.Ops = ops
		want := Evaluate(in)
		got := WithChangedOps(old, ops, nil)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("WithChangedOps(%+v):\n got %+v\nwant %+v", ops, got, want)
		}
		if again := WithChangedOps(got, nil, nil); !reflect.DeepEqual(again, got) {
			t.Errorf("not idempotent:\n got %+v\nwant %+v", again, got)
		}
	}

	// A local env names no release: every span is the judged code, and an
	// unscoped op is judged as it always was.
	in.Subject.Release = ""
	in.Ops = []OpHealth{mixed}
	r = Evaluate(in)
	if codes := codes(r.Findings); !slices.Equal(codes, []string{release.HealthChangedOpErrors}) || r.Findings[0].Unattributed != "" || len(changedOpEntries(r)) != 0 {
		t.Errorf("local: %+v %q", r.Findings, r.NotEvaluated)
	}
}

// TestChangedOpsNeedABaselineVersion: on a promotion an op is compared only
// against before figures of one version other than the judged release.
func TestChangedOpsNeedABaselineVersion(t *testing.T) {
	in := input(t, true, healthy())
	in.Thresholds.FailOn = []string{release.HealthChangedOpErrors}
	entries := func(r Report) []string {
		var out []string
		for _, e := range r.NotEvaluated {
			if strings.HasPrefix(e, NotEvaluatedChangedOps) {
				out = append(out, e)
			}
		}
		return out
	}
	const tail = " to compare with (none of their before-window spans carries a service.version other than v2, or none was read per version): notify.Send"
	failing := op("notify.Send", 100, 100, 0, 0.2, 10, 10)

	for name, tc := range map[string]struct {
		before, previous string
		want             string
	}{
		"no baseline version":               {"", "", NotEvaluatedChangedOps + " 1 op(s) have no before-window figures of a version before v2" + tail},
		"the judged release's own canary":   {"v2", "", NotEvaluatedChangedOps + " 1 op(s) have no before-window figures of a version before v2" + tail},
		"no baseline, previous release set": {"", "v1", NotEvaluatedChangedOps + " 1 op(s) have no before-window figures of the previous release v1" + tail},
	} {
		t.Run(name, func(t *testing.T) {
			in := in
			in.Subject.PreviousRelease = tc.previous
			o := failing
			o.BeforeVersion = tc.before
			in.Ops = []OpHealth{o}
			r := Evaluate(in)
			if len(r.Findings) != 0 || r.Verdict != Pass {
				t.Errorf("judged without a baseline version: %s %+v", r.Verdict, r.Findings)
			}
			if got := entries(r); !slices.Equal(got, []string{tc.want}) {
				t.Errorf("not_evaluated = %q, want %q", got, tc.want)
			}
		})
	}

	// Any version but the release is a baseline: the caller chose it.
	o := failing
	o.BeforeVersion = "v0"
	in.Ops = []OpHealth{o}
	if r := Evaluate(in); r.Verdict != Fail || len(entries(r)) != 0 {
		t.Errorf("baseline v0: %s %q", r.Verdict, r.NotEvaluated)
	}

	// The three entries, each its own, in a fixed order.
	noAfter, noBefore, thin := failing, failing, op("thin.Op", 1, 1, 0, 1, 1, 1)
	noAfter.Op, noAfter.AfterVersion = "a.NoAfter", ""
	noBefore.Op, noBefore.BeforeVersion = "b.NoBefore", ""
	in.Ops = []OpHealth{thin, noBefore, noAfter}
	r := Evaluate(in)
	got := entries(r)
	if len(r.Findings) != 0 || len(got) != 3 || !strings.Contains(got[0], "no after-window figures of v2") ||
		!strings.Contains(got[1], "no before-window figures") || !strings.Contains(got[2], "below changed_op_min_calls") {
		t.Errorf("entries %q, findings %+v", got, r.Findings)
	}

	// WithChangedOps: the same rule, equal to Evaluate, idempotent.
	old := Evaluate(Input{Subject: in.Subject, Windows: in.Windows, Services: in.Services, Thresholds: in.Thresholds})
	for _, ops := range [][]OpHealth{{failing}, {noBefore}, {thin, noBefore, noAfter}, {o, noBefore}} {
		in.Ops = ops
		want := Evaluate(in)
		got := WithChangedOps(old, ops, nil)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("WithChangedOps(%+v):\n got %+v\nwant %+v", ops, got, want)
		}
		if again := WithChangedOps(got, nil, nil); !reflect.DeepEqual(again, got) {
			t.Errorf("not idempotent:\n got %+v\nwant %+v", again, got)
		}
	}

	// A local env scopes nothing: no versions on either side, judged.
	in.Subject.Release = ""
	plain := failing
	plain.AfterVersion, plain.BeforeVersion = "", ""
	in.Ops = []OpHealth{plain}
	if r := Evaluate(in); r.Verdict != Fail || len(entries(r)) != 0 {
		t.Errorf("local: %s %q", r.Verdict, r.NotEvaluated)
	}
}
