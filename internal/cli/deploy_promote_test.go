package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// Tests for the compare-and-set a release deploy asserts — `forge env deploy
// <env> vX` (control-plane docs/design/hosted-deploy-primitives.md §3.1;
// docs/adr/env-verbs.md task V3 moved it here from `forge env promote`).
//
// The hosted cases run the real hostedStore over the real cloud.Client
// against fakeDeployService (hosted_ledger_test.go), which applies the
// SERVER's order — pinned, idempotent no-op, compare-and-set, in flight — and
// answers refusals in control-plane's wire form (reason header + a
// DeployPromoteRefusal detail). So each case asserts what forge SENT and what
// it made of the answer, through the same bytes production carries.

// declaredLedger resolves the env's ledger exactly as `forge env deploy` does,
// for the tests whose subject IS the production file-ledger path (they chdir
// into a project and assert what lands in .forge/promotions/). promoteOptions
// .Ledger is required, so stating it through the real resolver is what keeps
// those tests on the production path instead of substituting a fake for the
// thing under test.
func declaredLedger(t *testing.T, projectDir, env string) envLedger {
	t.Helper()
	l, err := resolveReleaseLedger(context.Background(), projectDir, env)
	if err != nil {
		t.Fatalf("resolve the ledger for env %q: %v", env, err)
	}
	return l
}

// hostedPromoteFixture is a hosted ledger with releases v1..v3 cut and the
// env "prod" promoted along `promoted` in order.
func hostedPromoteFixture(t *testing.T, promoted ...string) (*fakeDeployService, *hostedStore) {
	t.Helper()
	fake := newFakeDeployService(map[string]string{"prod": "env-prod-uuid"})
	store, _ := newHostedTestStore(t, fake)
	ctx := context.Background()
	for i, v := range []string{"v1", "v2", "v3"} {
		r := ociRelease(v, map[string]string{"api": sha(fmt.Sprint(i + 1))})
		r.CreatedAt = parseFixtureTime(fmt.Sprintf("2026-0%d-01T00:00:00Z", i+1))
		if _, err := store.Cut(ctx, r); err != nil {
			t.Fatalf("cut %s: %v", v, err)
		}
	}
	for _, v := range promoted {
		if _, err := store.Append(ctx, release.Promotion{Env: "prod", Release: v, Kind: release.KindPromote}, appendGuard{}); err != nil {
			t.Fatalf("seed promote %s: %v", v, err)
		}
	}
	return fake, store
}

// lastPromoteBody is the request body of the most recent Promote call.
func (f *fakeDeployService) lastPromoteBody(t *testing.T) map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.bodies) - 1; i >= 0; i-- {
		if f.bodies[i].Path == "/"+procPromote {
			return f.bodies[i].Body
		}
	}
	t.Fatal("no Promote call was made")
	return nil
}

// runHostedPromote runs the command body against the fixture's store, with
// stdout captured.
func runHostedPromote(t *testing.T, store *hostedStore, version string, opts promoteOptions) (string, error) {
	t.Helper()
	opts.ProjectDir = t.TempDir()
	opts.Git = allCommitsPresent()
	// Hosted:true is not a separate knob — it travels WITH the stores,
	// because this store IS a control plane: it records the promotion and
	// it converges it. Stating them apart is what would let a test describe
	// an env whose ledger is a control plane that applies nothing.
	if opts.Ledger.Bindings == nil {
		opts.Ledger = envLedger{Bindings: store, Releases: store, Hosted: true}
	}
	opts.Run.None = true // the test process may itself be running in CI
	// A PURE hosted deploy publishes (applyHostedPublish), through the same
	// client-side apply a self-managed env uses. These tests have no project
	// on disk and no cluster, so it is stubbed — unless the test installed
	// its OWN capturedClientDeploy, which is how the mixed-env tests assert
	// what the apply received. Checked rather than assigned, because a
	// blanket stub here would clobber theirs.
	if !promoteClientDeployStubbed {
		prevApply := runPromoteClientDeploy
		promoteClientDeployStubbed = true
		runPromoteClientDeploy = func(context.Context, string, deployOptions) error { return nil }
		t.Cleanup(func() {
			runPromoteClientDeploy = prevApply
			promoteClientDeployStubbed = false
		})
	}
	var err error
	out := captureStdout(t, func() { err = runPromote(context.Background(), version, "prod", opts) })
	return out, err
}

// TestPromote_ApplySendsThePlannedPromotionID is the headline: with NO flag,
// a promote asserts the env is still on the promotion the plan read. That is
// what makes anti-stomp the default rather than a flag CI has to remember.
func TestPromote_ApplySendsThePlannedPromotionID(t *testing.T) {
	fake, store := hostedPromoteFixture(t, "v1")
	planned, _, err := store.Current(context.Background(), "prod")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := runHostedPromote(t, store, "v2", promoteOptions{}); err != nil {
		t.Fatalf("promote: %v", err)
	}
	body := fake.lastPromoteBody(t)
	if got := body["expectedCurrentPromotionId"]; got != planned.ID {
		t.Fatalf("expectedCurrentPromotionId = %v, want the planned %q (body %v)", got, planned.ID, body)
	}
	if _, set := body["expectUnbound"]; set {
		t.Errorf("expectUnbound must not be sent beside an id — the wire field is a oneof: %v", body)
	}
	if _, set := body["supersedeInFlight"]; set {
		t.Errorf("supersedeInFlight must be sent only with --supersede: %v", body)
	}
}

// An env the plan saw as never promoted is promoted with expect_unbound —
// the other half of "always CAS". Without it, two first promotes would both
// land.
func TestPromote_UnboundEnvSendsExpectUnbound(t *testing.T) {
	fake, store := hostedPromoteFixture(t)
	if _, err := runHostedPromote(t, store, "v1", promoteOptions{}); err != nil {
		t.Fatalf("first promote: %v", err)
	}
	body := fake.lastPromoteBody(t)
	if body["expectUnbound"] != true {
		t.Fatalf("a first promote must send expectUnbound, got %v", body)
	}
	if _, set := body["expectedCurrentPromotionId"]; set {
		t.Errorf("no id may be sent beside expectUnbound: %v", body)
	}
}

// TestPromote_ExpectCurrentOverridesThePlan: the value captured when an
// approval was REQUESTED wins over the plan read after it, so a hotfix that
// landed while the approval waited is caught.
func TestPromote_ExpectCurrentOverridesThePlan(t *testing.T) {
	fake, store := hostedPromoteFixture(t, "v1")
	captured, _, _ := store.Current(context.Background(), "prod")
	// The hotfix lands while the pipeline waits for approval.
	if _, err := store.Append(context.Background(), release.Promotion{Env: "prod", Release: "v2", Kind: release.KindPromote}, appendGuard{}); err != nil {
		t.Fatal(err)
	}
	hotfix, _, _ := store.Current(context.Background(), "prod")

	out, err := runHostedPromote(t, store, "v3", promoteOptions{ExpectCurrent: captured.ID})
	if body := fake.lastPromoteBody(t); body["expectedCurrentPromotionId"] != captured.ID {
		t.Fatalf("--expect-current must replace the planned id %q with %q, sent %v", hotfix.ID, captured.ID, body)
	}
	if got := exitCodeForError(err); got != exitConflict {
		t.Fatalf("a promote expecting the pre-hotfix promotion must exit %d, got %d (%v)", exitConflict, got, err)
	}
	cur, _, _ := store.Current(context.Background(), "prod")
	if cur.ID != hotfix.ID {
		t.Fatalf("the hotfix was overwritten: current is %+v", cur)
	}
	for _, want := range []string{"REFUSED", "promotion_conflict", "NOTHING WRITTEN", hotfix.ID} {
		if !strings.Contains(out, want) {
			t.Errorf("the refused report must say %q, got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Promoted env") {
		t.Errorf("a refused promote must never read as a success:\n%s", out)
	}
}

// `--expect-current unbound` round-trips a captured "no promotion yet"
// through one CI variable.
func TestPromote_ExpectCurrentUnboundLiteral(t *testing.T) {
	fake, store := hostedPromoteFixture(t, "v1")
	_, err := runHostedPromote(t, store, "v2", promoteOptions{ExpectCurrent: expectUnboundLiteral})
	body := fake.lastPromoteBody(t)
	if body["expectUnbound"] != true || body["expectedCurrentPromotionId"] != nil {
		t.Fatalf("--expect-current unbound must send expectUnbound and no id, got %v", body)
	}
	if got := exitCodeForError(err); got != exitConflict {
		t.Fatalf("expecting unbound on a bound env must exit %d, got %d (%v)", exitConflict, got, err)
	}
}

// TestPromote_RefusalExitCodes pins §3.A's mapping end to end: the server's
// reason, not its message, picks the code.
func TestPromote_RefusalExitCodes(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(*fakeDeployService)
		opts   promoteOptions
		reason string
		want   int
	}{
		{"conflict", nil, promoteOptions{ExpectCurrent: "promo-stale"}, reasonPromotionConflict, exitConflict},
		{"pinned", func(f *fakeDeployService) { f.pinned = true }, promoteOptions{}, reasonEnvironmentPinned, exitRefused},
		{"in flight", func(f *fakeDeployService) { f.inFlightPhase = wireRolloutPhaseProgressing }, promoteOptions{}, reasonRolloutInFlight, exitRefused},
		{"in flight, header only", func(f *fakeDeployService) {
			f.inFlightPhase = wireRolloutPhaseProgressing
			f.refusalWithoutDetail = true
		}, promoteOptions{}, reasonRolloutInFlight, exitRefused},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake, store := hostedPromoteFixture(t, "v1")
			if tc.setup != nil {
				tc.setup(fake)
			}
			_, err := runHostedPromote(t, store, "v2", tc.opts)
			var refused *promoteRefusedError
			if !errors.As(err, &refused) {
				t.Fatalf("want a *promoteRefusedError, got %T: %v", err, err)
			}
			if refused.Reason != tc.reason {
				t.Errorf("reason = %q, want %q", refused.Reason, tc.reason)
			}
			if got := exitCodeForError(err); got != tc.want {
				t.Errorf("exit code = %d, want %d", got, tc.want)
			}
			if cur, _, _ := store.Current(context.Background(), "prod"); cur.Release != "v1" {
				t.Errorf("a refused promote wrote: prod is on %s", cur.Release)
			}
		})
	}
}

// --supersede is sent, admits the in-flight promote, and the server records
// it on the new entry.
func TestPromote_SupersedeIsSentAndRecorded(t *testing.T) {
	fake, store := hostedPromoteFixture(t, "v1")
	fake.inFlightPhase = wireRolloutPhaseProgressing
	if _, err := runHostedPromote(t, store, "v2", promoteOptions{Supersede: true}); err != nil {
		t.Fatalf("--supersede must admit an in-flight promote: %v", err)
	}
	if body := fake.lastPromoteBody(t); body["supersedeInFlight"] != true {
		t.Fatalf("supersedeInFlight not sent: %v", body)
	}
	cur, _, _ := store.Current(context.Background(), "prod")
	if cur.Release != "v2" || !cur.SupersededInFlight {
		t.Fatalf("the superseding promote must land and be recorded, got %+v", cur)
	}
}

// TestPromote_JSONCarriesTheRefusal: --json is one document on a refusal
// too, applied:false, with what was expected and what is actually there,
// and its ok/exit_code agree with the process status.
func TestPromote_JSONCarriesTheRefusal(t *testing.T) {
	fake, store := hostedPromoteFixture(t, "v1", "v2")
	fake.inFlightPhase = wireRolloutPhaseStabilizing
	planned, _, _ := store.Current(context.Background(), "prod")

	out, err := runHostedPromote(t, store, "v3", promoteOptions{JSON: true})
	if got := exitCodeForError(err); got != exitRefused {
		t.Fatalf("exit code = %d, want %d (%v)", got, exitRefused, err)
	}
	var doc map[string]any
	if jerr := json.Unmarshal([]byte(out), &doc); jerr != nil {
		t.Fatalf("stdout must be exactly one JSON document: %v\n%s", jerr, out)
	}
	if doc["applied"] != false || doc["ok"] != false || doc["exit_code"] != float64(exitRefused) {
		t.Errorf("applied/ok/exit_code = %v/%v/%v, want false/false/%d", doc["applied"], doc["ok"], doc["exit_code"], exitRefused)
	}
	if doc["expected"] != planned.ID {
		t.Errorf("expected = %v, want the planned %q", doc["expected"], planned.ID)
	}
	refusal, ok := doc["refusal"].(map[string]any)
	if !ok {
		t.Fatalf("a refused --json document must carry `refusal`:\n%s", out)
	}
	if refusal["reason"] != reasonRolloutInFlight || refusal["actual_phase"] != "stabilizing" {
		t.Errorf("refusal reason/phase = %v/%v", refusal["reason"], refusal["actual_phase"])
	}
	if refusal["expected_current_promotion_id"] != planned.ID {
		t.Errorf("the refusal must echo the expectation, got %v", refusal["expected_current_promotion_id"])
	}
	actual, _ := refusal["actual_current"].(map[string]any)
	if actual["id"] != planned.ID || actual["release"] != "v2" {
		t.Errorf("actual_current must name what is there (v2, %s), got %v", planned.ID, refusal["actual_current"])
	}
	if _, present := doc["recorded"]; present {
		t.Errorf("a refused promote recorded nothing, so `recorded` must be absent:\n%s", out)
	}
	current, _ := doc["current"].(map[string]any)
	if current["promotion_id"] != planned.ID {
		t.Errorf("current.promotion_id = %v, want %q — it is what CI captures for --expect-current", current["promotion_id"], planned.ID)
	}
}

// --plan --json shows the id a later --expect-current should carry, and
// sends nothing.
func TestPromote_PlanShowsThePromotionIDAndWritesNothing(t *testing.T) {
	fake, store := hostedPromoteFixture(t, "v1")
	planned, _, _ := store.Current(context.Background(), "prod")
	before := fake.callCount(procPromote)

	out, err := runHostedPromote(t, store, "v2", promoteOptions{DryRun: true, JSON: true})
	if err != nil {
		t.Fatalf("--plan: %v", err)
	}
	if n := fake.callCount(procPromote); n != before {
		t.Fatalf("--plan called Promote %d time(s)", n-before)
	}
	var doc promotePlan
	if jerr := json.Unmarshal([]byte(out), &doc); jerr != nil {
		t.Fatalf("decode: %v\n%s", jerr, out)
	}
	if doc.Current.PromotionID != planned.ID || doc.Expected != planned.ID {
		t.Fatalf("current.promotion_id/expected = %q/%q, want %q", doc.Current.PromotionID, doc.Expected, planned.ID)
	}
	if !doc.OK || doc.ExitCode != exitOK || doc.Applied {
		t.Errorf("a plan is ok, exit 0, not applied; got ok=%v exit=%d applied=%v", doc.OK, doc.ExitCode, doc.Applied)
	}
}

// TestPromote_RetryOfALandedPromoteIsANoOp pins the server's step order on
// the forge side: a CI retry of a promote that already landed carries the
// PREVIOUS id as its expectation, and must succeed, not conflict. The file
// ledger applies the same order itself.
func TestPromote_RetryOfALandedPromoteIsANoOp(t *testing.T) {
	store := newMemBindingStore(map[string]release.Promotion{"prod": {ID: "p-1", Release: "v1"}})
	releases := newMemReleaseLedger(
		rel("v1", "2026-01-01T00:00:00Z", "", false, map[string]string{"api": sha("1")}),
		rel("v2", "2026-02-01T00:00:00Z", "", false, map[string]string{"api": sha("2")}),
	)
	opts := promoteOptions{ProjectDir: t.TempDir(), Git: allCommitsPresent(), ExpectCurrent: "p-1",
		Ledger: envLedger{Bindings: store, Releases: releases}}
	opts.Run.None = true
	for attempt := 1; attempt <= 2; attempt++ {
		var err error
		captureStdout(t, func() { err = runPromote(context.Background(), "v2", "prod", opts) })
		if err != nil {
			t.Fatalf("attempt %d: a retry of a landed promote must be a no-op, got %v", attempt, err)
		}
	}
	if n := len(store.history["prod"]); n != 2 {
		t.Fatalf("history has %d entries, want 2 (v1, v2 once)", n)
	}
}

// The FILE ledger (self-managed envs) applies the same compare-and-set,
// against the history it reads at write time.
func TestFileLedger_CompareAndSet(t *testing.T) {
	dir := t.TempDir()
	store := testBindings(t, dir)
	ctx := context.Background()
	p := func(v string) release.Promotion {
		return release.Promotion{Env: "prod", Release: v, Kind: release.KindPromote, Resolved: map[string]string{"api": sha("a")}}
	}

	first, err := store.Append(ctx, p("v1"), appendGuard{ExpectUnbound: true})
	if err != nil {
		t.Fatalf("expect-unbound on an empty log must write: %v", err)
	}
	if _, err := store.Append(ctx, p("v2"), appendGuard{ExpectUnbound: true}); exitCodeForError(err) != exitConflict {
		t.Fatalf("expect-unbound on a bound log must conflict (exit 3), got %v", err)
	}
	var refused *promoteRefusedError
	_, err = store.Append(ctx, p("v2"), appendGuard{ExpectedCurrentID: "someone-elses"})
	if !errors.As(err, &refused) || refused.ActualCurrent == nil || refused.ActualCurrent.ID != first.ID {
		t.Fatalf("a stale expectation must be refused naming the actual current %q, got %v", first.ID, err)
	}
	if _, err := store.Append(ctx, p("v2"), appendGuard{ExpectedCurrentID: first.ID}); err != nil {
		t.Fatalf("a matching expectation must write: %v", err)
	}
	// The no-op precedes the CAS: re-promoting v2 with the now-stale id of
	// v1 is a retry of a success, not a conflict.
	if _, err := store.Append(ctx, p("v2"), appendGuard{ExpectedCurrentID: first.ID}); err != nil {
		t.Fatalf("a retry of the landed promote must be a no-op: %v", err)
	}
	history, _ := storeHistory(t, store, "prod")
	if len(history) != 2 {
		t.Fatalf("log has %d lines, want 2", len(history))
	}
	data, _ := os.ReadFile(testPromotionLogPath(t, dir, "prod"))
	if strings.Count(string(data), "\n") != 2 {
		t.Fatalf("a refused append wrote a line:\n%s", data)
	}
}

// Every flag the release half needs is declared on `forge env deploy`, which
// is now the ONLY command that records a promotion.
func TestDeployCmd_DeclaresEveryReleaseFlag(t *testing.T) {
	cmd := newDeployCmd()
	for _, name := range []string{
		// The release half, absorbed from `env promote`.
		"plan", "note", "actor",
		"expect-current", "expect-unbound", "supersede",
		"gate", "from", "from-promotion",
		"run-id", "run-url", "no-run",
		// The health gate, opt-OUT — and --wait, which waits THROUGH a
		// deploy the control plane queued on billing (exit 7 otherwise).
		"no-wait", "timeout", "fail-fast", "wait",
		// The apply half, which a release deploy forwards.
		"target", "namespace", "dry-run", "json",
	} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("--%s is not declared on `forge env deploy`", name)
		}
	}
	// `promote --deploy` is gone, not renamed: applying is what the verb
	// means. (`--wait` was retired with it and came back with ONE new
	// meaning — wait through a queue on billing, which the default does not
	// — so it no longer asks for what a deploy already has.)
	for _, gone := range []string{"deploy"} {
		if cmd.Flags().Lookup(gone) != nil {
			t.Errorf("--%s must not exist on `forge env deploy`", gone)
		}
	}
	// `promote --to` is gone too. `--to` exists again with ONE meaning —
	// the break-glass rollback target of --offline-ledger — and is refused
	// on its own, so the retired online spelling still does nothing.
	if err := dispatchDeployCmd(context.Background(), "prod", deployCmdFlags{offlineTo: "previous"}); err == nil ||
		!strings.Contains(err.Error(), "belongs to --offline-ledger") {
		t.Errorf("--to without --offline-ledger must be refused, got %v", err)
	}
	cmd.SetArgs([]string{"prod", "v1", "--expect-current", "x", "--expect-unbound"})
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "expect-current") {
		t.Fatalf("--expect-current and --expect-unbound together must be refused, got %v", err)
	}
}

// `forge env promote` is GONE — no alias, no hidden name. A surviving spelling
// would be a copy-pasteable command that records a promotion and never applies
// it, which is the failure mode V3 removed.
func TestEnvCmd_HasNoPromoteVerb(t *testing.T) {
	for _, c := range newEnvCmd().Commands() {
		if c.Name() == "promote" || c.HasAlias("promote") {
			t.Fatalf("`forge env promote` still resolves (as %q)", c.Use)
		}
	}
}

// TestDeploy_ReleaseFlagsWithoutAReleaseAreRefused: a flag that only means
// something beside a release must not be silently ignored on a deploy that
// names none. `--expect-current abc123` with no version reads as an anti-stomp
// guard and has none, so the deploy would apply and report success while the
// guard the caller asked for never ran.
func TestDeploy_ReleaseFlagsWithoutAReleaseAreRefused(t *testing.T) {
	cases := map[string]promoteCmdFlags{
		"--plan":           {plan: true},
		"--note":           {note: "why"},
		"--actor":          {actor: "ci"},
		"--expect-current": {expectCurrent: "p-1"},
		"--expect-unbound": {expectUnbound: true},
		"--supersede":      {supersede: true},
		"--gate":           {gates: []string{"name=e2e,status=passed"}},
	}
	for name, f := range cases {
		t.Run(name, func(t *testing.T) {
			err := refusePromoteFlagsWithoutRelease(f)
			if err == nil {
				t.Fatalf("%s without a release must be refused", name)
			}
			if !strings.Contains(err.Error(), name) {
				t.Errorf("the refusal must name %s, got: %v", name, err)
			}
			if !strings.Contains(err.Error(), "forge env deploy <env> <version>") {
				t.Errorf("the refusal must say how to name a release, got: %v", err)
			}
		})
	}
	// The flags that mean something EITHER WAY are not refused: the gate
	// flags tune the apply's own rollout wait on a spec-change deploy.
	if err := refusePromoteFlagsWithoutRelease(promoteCmdFlags{noWait: true, timeout: time.Minute, failFast: true}); err != nil {
		t.Fatalf("the health-gate flags tune a spec-change deploy too and must be accepted: %v", err)
	}
}

// A version, or a --from that supplies one, is what forks the verb.
func TestPromoteCmdFlags_RequestedRelease(t *testing.T) {
	cases := []struct {
		name string
		in   promoteCmdFlags
		want bool
	}{
		{"bare deploy", promoteCmdFlags{}, false},
		{"a version", promoteCmdFlags{version: "v1"}, true},
		{"--from", promoteCmdFlags{fromEnv: "staging"}, true},
		{"--from-promotion alone", promoteCmdFlags{fromPromotionID: "p-1"}, true},
		{"apply flags only", promoteCmdFlags{noWait: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.requestedRelease(); got != tc.want {
				t.Fatalf("requestedRelease() = %v, want %v", got, tc.want)
			}
		})
	}
}

// The --run-id flag reaches the wire: §3.A's run identity on promote.
func TestPromote_RunIdentityIsSent(t *testing.T) {
	fake, store := hostedPromoteFixture(t, "v1")
	opts := promoteOptions{}
	_, err := runHostedPromote(t, store, "v2", opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, set := fake.lastPromoteBody(t)["run"]; set {
		t.Fatal("--no-run must send no run")
	}

	opts = promoteOptions{ProjectDir: t.TempDir(), Git: allCommitsPresent(),
		Ledger: envLedger{Bindings: store, Releases: store, Hosted: true},
		Run:    runOptions{ID: "manual-42", URL: "https://ci.example/42"}}
	captureStdout(t, func() { err = runPromote(context.Background(), "v3", "prod", opts) })
	if err != nil {
		t.Fatal(err)
	}
	run, _ := fake.lastPromoteBody(t)["run"].(map[string]any)
	if run["id"] != "manual-42" || run["url"] != "https://ci.example/42" {
		t.Fatalf("run = %v, want id manual-42 with its url", run)
	}
}
