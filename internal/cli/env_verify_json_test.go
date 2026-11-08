package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/reliant-labs/forge/internal/cluster"
)

// `forge env status --json` tests.
//
// These assert on the PARSED report rather than on substrings of the output,
// because the thing under test is a machine contract: a consumer does
// `jq '.images[] | select(.state == "drift")'`, and a test that only checked
// the word "drift" appeared somewhere on stdout would pass for output no
// parser could read.
//
// The exit code is asserted alongside the body in every case. The two are one
// contract — text and JSON mode must exit identically — and a report saying
// "ok": false while the process exits 0 is worse than no report at all,
// because CI believes the exit code.

// runEnvVerifyJSON runs the command in --json mode against a stubbed cluster,
// capturing stdout and parsing it. It returns the parsed report, the process
// exit code, and the raw output for diagnostics.
func runEnvVerifyJSON(t *testing.T, dir, envName string, lister clusterImageLister, target envTarget) (envStatusDocument, int, string) {
	t.Helper()
	t.Chdir(dir)

	var err error
	out := captureStdout(t, func() {
		err = runEnvStatusRelease(context.Background(), envName, envStatusOptions{
			JSON:     true,
			Lister:   lister,
			Resolver: stubResolver{target: target},
		})
	})

	var report envStatusDocument
	if jsonErr := json.Unmarshal([]byte(out), &report); jsonErr != nil {
		t.Fatalf("--json output must be parseable JSON, got error %v for output:\n%s", jsonErr, out)
	}
	return report, exitCodeOf(t, err), out
}

// defaultJSONTarget is the resolved cluster the stub resolver reports, matching
// the text-mode tests.
var defaultJSONTarget = envTarget{KubeContext: "test-context", Namespace: "test-ns"}

// TestEnvVerifyJSON_Match is the clean path: ok true, exit 0, and the whole
// declared shape present so a consumer can key off it.
func TestEnvVerifyJSON_Match(t *testing.T) {
	dir := t.TempDir()
	writeBinding(t, dir, "prod", "v1.5.15", map[string]string{"control-plane": digestDeclared})

	lister := &stubLister{images: []cluster.WorkloadImage{
		deployImage("control-plane", "ghcr.io/acme/control-plane@"+digestDeclared),
	}}

	report, code, out := runEnvVerifyJSON(t, dir, "prod", lister, defaultJSONTarget)

	if code != 0 {
		t.Errorf("a matching env must exit 0, got %d. Output:\n%s", code, out)
	}
	if !report.OK {
		t.Errorf("ok must be true for a matching env, got false (detail: %s)", report.Detail)
	}
	if !report.Bound {
		t.Error("a promoted env must report bound: true")
	}
	if report.Env != "prod" || report.Release != "v1.5.15" {
		t.Errorf("env/release = %q/%q, want prod/v1.5.15", report.Env, report.Release)
	}
	// The promote-time caveat is only useful if the value actually ships.
	cur, _, _ := testBindings(t, dir).Current(context.Background(), "prod")
	if report.PromotedAt == "" || report.PromotedAt != formatLedgerTime(cur.PromotedAt) {
		t.Errorf("promoted_at = %q, want the ledger entry's promote timestamp %s", report.PromotedAt, cur.PromotedAt)
	}
	if report.KubeContext != "test-context" || report.Namespace != "test-ns" {
		t.Errorf("target = %q/%q, want test-context/test-ns", report.KubeContext, report.Namespace)
	}
	if len(report.Images) != 1 || report.Images[0].State != imageMatch {
		t.Fatalf("expected a single MATCH verdict, got %+v", report.Images)
	}
	if report.Tally.Match != 1 {
		t.Errorf("tally = %+v, want 1 match", report.Tally)
	}
}

// TestEnvVerifyJSON_StateIsLowercaseString pins the wire spelling of the state
// field. Emitting the raw iota would make `select(.state == "drift")` match
// nothing while the report still looked structurally valid — a silent failure
// in exactly the consumer this flag exists for.
func TestEnvVerifyJSON_StateIsLowercaseString(t *testing.T) {
	cases := map[imageState]string{
		imageMatch:       "match",
		imageDrift:       "drift",
		imageMissing:     "missing",
		imageUntagged:    "untagged",
		imageUnreachable: "unreachable",
	}
	for state, want := range cases {
		got, err := json.Marshal(state)
		if err != nil {
			t.Fatalf("marshal %s: %v", state, err)
		}
		if string(got) != `"`+want+`"` {
			t.Errorf("imageState(%s) marshals to %s, want %q", state, got, want)
		}
	}
}

// TestEnvVerifyJSON_StateRoundTrips pins that the wire form decodes back to
// the same value it encoded from. Without this the contract is one-way and
// nothing catches a marshal/unmarshal pair drifting apart.
func TestEnvVerifyJSON_StateRoundTrips(t *testing.T) {
	for _, state := range []imageState{imageMatch, imageDrift, imageMissing, imageUntagged, imageUnreachable} {
		encoded, err := json.Marshal(state)
		if err != nil {
			t.Fatalf("marshal %s: %v", state, err)
		}
		var decoded imageState
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("unmarshal %s: %v", encoded, err)
		}
		if decoded != state {
			t.Errorf("round trip of %s produced %s", state, decoded)
		}
	}
}

// TestEnvVerifyJSON_UnknownStateIsRejected is the one that matters most about
// decoding. imageMatch is the zero value, so a lenient decoder would turn an
// unrecognized state — a typo, or a sixth state added by a newer forge — into
// a clean MATCH: a green verdict over an environment nobody checked, which is
// precisely the failure the five-state model exists to prevent.
func TestEnvVerifyJSON_UnknownStateIsRejected(t *testing.T) {
	var decoded imageState
	err := json.Unmarshal([]byte(`"quantum-superposition"`), &decoded)
	if err == nil {
		t.Fatalf("an unknown state must be rejected, got a clean decode to %s", decoded)
	}
	if decoded != imageMatch {
		// Not a requirement, just a guard on the premise: the zero value
		// IS match, which is why silent fallback would be dangerous.
		t.Logf("zero value decoded as %s", decoded)
	}
}

// TestEnvVerifyJSON_DriftExits1AndIsMachineReadable is the headline case. Both
// digests must survive into the JSON, for the same reason the text report
// prints both: a consumer that only learns "drift" has to re-run the kubectl
// query this command already ran.
func TestEnvVerifyJSON_DriftExits1AndIsMachineReadable(t *testing.T) {
	dir := t.TempDir()
	writeBinding(t, dir, "prod", "v1.5.15", map[string]string{"control-plane": digestDeclared})

	lister := &stubLister{images: []cluster.WorkloadImage{
		deployImage("control-plane", "ghcr.io/acme/control-plane@"+digestRunning),
	}}

	report, code, out := runEnvVerifyJSON(t, dir, "prod", lister, defaultJSONTarget)

	if code != 1 {
		t.Errorf("drift exit code = %d, want 1 — --json must exit identically to text mode. Output:\n%s", code, out)
	}
	if report.OK {
		t.Error("ok must be false when the env drifted")
	}
	if len(report.Images) != 1 {
		t.Fatalf("expected 1 verdict, got %d", len(report.Images))
	}
	got := report.Images[0]
	if got.State != imageDrift {
		t.Errorf("state = %s, want drift", got.State)
	}
	// Both digests, in full — an abbreviated digest is not actionable.
	if got.Declared != digestDeclared {
		t.Errorf("declared = %q, want %q", got.Declared, digestDeclared)
	}
	if got.Running != digestRunning {
		t.Errorf("running = %q, want %q", got.Running, digestRunning)
	}
	if report.Tally.Drift != 1 || report.Tally.Match != 0 {
		t.Errorf("tally = %+v, want exactly 1 drift", report.Tally)
	}
}

// TestEnvVerifyJSON_NoBindingExits0WithBoundFalse — an env that was never
// promoted has declared nothing, so there is nothing to be wrong about. It
// must still emit a complete, valid report: a consumer that got no output (or
// invalid JSON) for a healthy env would have to special-case the absence,
// which is how the unbound state gets miscoded as a failure downstream.
func TestEnvVerifyJSON_NoBindingExits0WithBoundFalse(t *testing.T) {
	dir := t.TempDir()
	writeBinding(t, dir, "prod", "v1.5.15", map[string]string{"control-plane": digestDeclared})

	// "staging" has no binding in this ledger.
	lister := &stubLister{}
	report, code, out := runEnvVerifyJSON(t, dir, "staging", lister, defaultJSONTarget)

	if code != 0 {
		t.Errorf("an unbound env must exit 0, got %d. Output:\n%s", code, out)
	}
	if report.Bound {
		t.Error("bound must be false for an env that was never promoted")
	}
	if !report.OK {
		t.Errorf("an unbound env is NOT a failure — ok must be true, got false (detail: %s)", report.Detail)
	}
	if report.Env != "staging" {
		t.Errorf("env = %q, want staging", report.Env)
	}
	// `[]`, never `null`: a consumer ranging over .images must not have to
	// nil-check first.
	if report.Images == nil {
		t.Error("images must be an empty array, not null")
	}
	if len(report.Images) != 0 {
		t.Errorf("an unbound env declares nothing, got %d verdicts", len(report.Images))
	}
	if len(lister.calls) != 0 {
		t.Errorf("an unbound env must not read the cluster at all, got %v", lister.calls)
	}
}

// TestEnvVerifyJSON_UnreachableDoesNotReadAsDrift is the separation the whole
// five-state model exists to protect, carried into the machine format. A VPN
// drop is not evidence a release is wrong, so it must be exit 2 (not 1), the
// state must be "unreachable" (not "drift"), and the drift/match tallies must
// both stay zero — a consumer alerting on drift must not page anyone for a
// network failure.
func TestEnvVerifyJSON_UnreachableDoesNotReadAsDrift(t *testing.T) {
	dir := t.TempDir()
	writeBinding(t, dir, "prod", "v1.5.15", map[string]string{"control-plane": digestDeclared})

	lister := &stubLister{err: fmt.Errorf("Unable to connect to the server: dial tcp: i/o timeout")}
	report, code, out := runEnvVerifyJSON(t, dir, "prod", lister, defaultJSONTarget)

	if code != 2 {
		t.Errorf("unreachable exit code = %d, want 2 — a network failure must not be reported as drift. Output:\n%s", code, out)
	}
	if report.OK {
		t.Error("ok must be false when the cluster could not be read: nothing was verified")
	}
	if len(report.Images) != 1 || report.Images[0].State != imageUnreachable {
		t.Fatalf("expected a single UNREACHABLE verdict, got %+v", report.Images)
	}
	if report.Tally.Unreachable != 1 {
		t.Errorf("tally = %+v, want 1 unreachable", report.Tally)
	}
	// The two states this must never collapse into.
	if report.Tally.Drift != 0 {
		t.Errorf("an unreadable cluster must never count as drift, tally = %+v", report.Tally)
	}
	if report.Tally.Match != 0 {
		t.Errorf("an unreadable cluster must never count as match — that is a green report over an env nobody looked at, tally = %+v", report.Tally)
	}

	// And the same separation at the wire level, where consumers read it.
	var raw struct {
		Images []struct {
			State string `json:"state"`
		} `json:"images"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if len(raw.Images) != 1 || raw.Images[0].State != "unreachable" {
		t.Errorf(`state must serialize as "unreachable", got %+v`, raw.Images)
	}
}

// TestEnvVerifyJSON_MissingExits1 — declared but running nowhere fails at the
// same code as drift: both mean the env is not what the ledger says.
func TestEnvVerifyJSON_MissingExits1(t *testing.T) {
	dir := t.TempDir()
	writeBinding(t, dir, "prod", "v1.5.15", map[string]string{"control-plane": digestDeclared})

	report, code, out := runEnvVerifyJSON(t, dir, "prod", &stubLister{images: nil}, defaultJSONTarget)

	if code != 1 {
		t.Errorf("missing exit code = %d, want 1. Output:\n%s", code, out)
	}
	if report.OK {
		t.Error("ok must be false when a declared image runs nowhere")
	}
	if report.Tally.Missing != 1 {
		t.Errorf("tally = %+v, want 1 missing", report.Tally)
	}
}

// TestEnvVerifyJSON_UntaggedDoesNotFlipOK pins that UNTAGGED matches text
// mode's behaviour: a mutable tag proves nothing either way, so it is reported
// but does not fail the command. `ok` is false exactly when text mode exits
// non-zero, and text mode exits 0 here with a note.
func TestEnvVerifyJSON_UntaggedDoesNotFlipOK(t *testing.T) {
	dir := t.TempDir()
	writeBinding(t, dir, "prod", "v1.5.15", map[string]string{"control-plane": digestDeclared})

	lister := &stubLister{images: []cluster.WorkloadImage{
		deployImage("control-plane", "ghcr.io/acme/control-plane:v1.5.15"),
	}}
	report, code, out := runEnvVerifyJSON(t, dir, "prod", lister, defaultJSONTarget)

	if code != 0 {
		t.Errorf("untagged exit code = %d, want 0 — nothing was proven wrong. Output:\n%s", code, out)
	}
	if !report.OK {
		t.Errorf("untagged must not flip ok, got false (detail: %s)", report.Detail)
	}
	if report.Tally.Untagged != 1 || report.Tally.Match != 0 || report.Tally.Drift != 0 {
		t.Errorf("tally = %+v, want exactly 1 untagged and no match/drift", report.Tally)
	}
}

// TestEnvVerifyJSON_NoDeclaredClusterIsUnreachable covers an env whose KCL
// names no cluster. Nothing can be learned about what it runs, so the machine
// report must say "unreachable" and exit 2 — never drift, which would accuse a
// release on the basis of a question that was never asked.
func TestEnvVerifyJSON_NoDeclaredClusterIsUnreachable(t *testing.T) {
	dir := t.TempDir()
	writeBinding(t, dir, "prod", "v1.5.15", map[string]string{"control-plane": digestDeclared})

	lister := &stubLister{}
	report, code, out := runEnvVerifyJSON(t, dir, "prod", lister, envTarget{}) // nothing declared

	if code != 2 {
		t.Errorf("unresolvable target exit code = %d, want 2. Output:\n%s", code, out)
	}
	if report.OK {
		t.Error("ok must be false when the target could not be resolved")
	}
	if report.Tally.Unreachable != 1 || report.Tally.Drift != 0 {
		t.Errorf("tally = %+v, want 1 unreachable and 0 drift", report.Tally)
	}
	// The cause must survive into the machine report, or the consumer
	// learns only that something was unreachable and not what to fix.
	if len(report.Images) != 1 || report.Images[0].Detail == "" {
		t.Errorf("the unreachable verdict must carry its cause, got %+v", report.Images)
	}
	if len(lister.calls) != 0 {
		t.Errorf("must not attempt a cluster read with no context, got %v", lister.calls)
	}
}
