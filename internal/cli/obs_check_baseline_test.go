package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/obsbackend"
)

// forge bug d1dc1977: a before window below min_requests is no baseline, so a
// release compared against it is not proven good — exit 2, as a thin after
// window already was — while an absolute finding still fails it.

func TestObsCheckLocalThinBaselineIsInconclusive(t *testing.T) {
	hr := &healthRunner{
		before: obsbackend.Result{Rows: [][]any{redRow("api", 15, 0, 100)}},
		after:  obsbackend.Result{Rows: [][]any{redRow("api", 1000, 0, 100)}},
	}
	out, _, err := newObsCheckHarness().run(t, devFacts(), nil, hr, "check", "dev", "--since", "20m", "--json")
	if got := exitCodeForError(err); got != 2 {
		t.Fatalf("exit = %d, want 2 (%v)", got, err)
	}
	doc := decode(t, out)
	if doc["verdict"] != "INCONCLUSIVE" || doc["reason_code"] != "thin_baseline" {
		t.Errorf("verdict = %v reason_code = %v", doc["verdict"], doc["reason_code"])
	}
	if reason := fmt.Sprint(doc["reason"]); !strings.Contains(reason, "api had 15 requests before the promotion") {
		t.Errorf("the reason must name the thin before window and its count: %s", reason)
	}
	if ne := fmt.Sprint(doc["not_evaluated"]); !strings.Contains(ne, "service:api") {
		t.Errorf("not_evaluated = %s, want service:api", ne)
	}

	hr = &healthRunner{before: hr.before, after: hr.after}
	_, _, err = newObsCheckHarness().run(t, devFacts(), nil, hr, "check", "dev", "--since", "20m", "--allow-inconclusive")
	if err != nil {
		t.Errorf("--allow-inconclusive: %v", err)
	}
}

func TestObsCheckLocalThinBaselineStillFailsOnANewIssue(t *testing.T) {
	hr := &healthRunner{
		before: obsbackend.Result{Rows: [][]any{redRow("api", 15, 0, 100)}},
		after:  obsbackend.Result{Rows: [][]any{redRow("api", 1000, 0, 100)}},
		issues: obsbackend.Result{Rows: [][]any{{"deadbeefdeadbeef", "boom: <n>", "x.go in Handle", "api", jn("7"), "2026-10-22 11:41:00.000000000"}}},
	}
	out, _, err := newObsCheckHarness().run(t, devFacts(), nil, hr, "check", "dev", "--since", "20m", "--json", "--allow-inconclusive")
	if got := exitCodeForError(err); got != 1 {
		t.Fatalf("exit = %d, want 1: a thin baseline never hides a new issue (%v)", got, err)
	}
	if doc := decode(t, out); doc["verdict"] != "FAIL" {
		t.Errorf("verdict = %v", doc["verdict"])
	}
}

// require_baseline is the user's gate: every holds a release one service
// cannot compare, none compares however thin.
func TestObsCheckLocalRequireBaseline(t *testing.T) {
	// A healthRunner answers one run: it counts the RED reads it serves.
	mixed := func() *healthRunner {
		return &healthRunner{
			before: obsbackend.Result{Rows: [][]any{redRow("api", 1000, 0, 100), redRow("new", 3, 0, 100)}},
			after:  obsbackend.Result{Rows: [][]any{redRow("api", 1000, 0, 100), redRow("new", 400, 0, 100)}},
		}
	}
	thin := func() *healthRunner {
		return &healthRunner{
			before: obsbackend.Result{Rows: [][]any{redRow("api", 15, 0, 100)}},
			after:  obsbackend.Result{Rows: [][]any{redRow("api", 1000, 0, 100)}},
		}
	}
	if _, _, err := newObsCheckHarness().run(t, devFacts(), nil, mixed(), "check", "dev", "--since", "20m"); err != nil {
		t.Errorf("default (any): %v", err)
	}
	out, _, err := newObsCheckHarness().run(t, devFacts(), nil, mixed(), "check", "dev", "--since", "20m", "--json", "--threshold", "require_baseline=every")
	if got := exitCodeForError(err); got != 2 {
		t.Fatalf("every: exit = %d, want 2 (%v)", got, err)
	}
	if doc := decode(t, out); doc["reason_code"] != "thin_baseline" || !strings.Contains(fmt.Sprint(doc["thresholds"]), "map[name:require_baseline source:request value:every]") {
		t.Errorf("reason_code %v thresholds %v", doc["reason_code"], doc["thresholds"])
	}

	if _, _, err := newObsCheckHarness().run(t, devFacts(), nil, thin(), "check", "dev", "--since", "20m", "--threshold", "require_baseline=none"); err != nil {
		t.Errorf("none: %v", err)
	}
	if _, _, err := newObsCheckHarness().run(t, devFacts(), nil, thin(), "check", "dev", "--since", "20m", "--threshold", "require_baseline=some"); exitCodeForError(err) != 1 {
		t.Errorf("an unknown policy is a wrong request: %v", err)
	}
}

// A control plane that predates the rule answers PASS over a thin baseline;
// the CLI applies the rule to its answer, so it says 2 as the updated control
// plane will.
func TestObsCheckCloudThinBaselineIsInconclusiveOverAnOlderPass(t *testing.T) {
	reply := strings.Replace(healthReply("RELEASE_VERDICT_PASS", true, "", ""), `"requests":"900"`, `"requests":"15"`, 1)
	caller := &seqCaller{replies: map[string][]string{procHealth: {reply}}}
	out, _, err := newObsCheckHarness().run(t, cloudFacts(), caller, nil, "check", "prod", "--json")
	if got := exitCodeForError(err); got != 2 {
		t.Fatalf("exit = %d, want 2 (%v)", got, err)
	}
	doc := decode(t, out)
	if doc["verdict"] != "INCONCLUSIVE" || doc["reason_code"] != "thin_baseline" {
		t.Errorf("verdict = %v reason_code = %v", doc["verdict"], doc["reason_code"])
	}
	if ne := fmt.Sprint(doc["not_evaluated"]); !strings.Contains(ne, "service:api") {
		t.Errorf("not_evaluated = %s, want service:api", ne)
	}
}

// The control plane's not_evaluated reaches the document.
func TestObsCheckCloudCarriesNotEvaluated(t *testing.T) {
	reply := healthReply("RELEASE_VERDICT_PASS", true, "", `"notEvaluated":["regressed_issue"],`)
	caller := &seqCaller{replies: map[string][]string{procHealth: {reply}}}
	out, _, err := newObsCheckHarness().run(t, cloudFacts(), caller, nil, "check", "prod", "--json")
	if err != nil {
		t.Fatalf("healthy baseline: %v", err)
	}
	if ne := fmt.Sprint(decode(t, out)["not_evaluated"]); ne != "[regressed_issue]" {
		t.Errorf("not_evaluated = %s, want [regressed_issue]", ne)
	}
}
