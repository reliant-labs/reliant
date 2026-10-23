package cli

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/pkg/release"
)

// Tests for the APPLY and WAIT halves of `forge env deploy <env> vX`
// (docs/adr/env-verbs.md, task V3). Was promote_wait_test.go, where the same
// machinery was reached by `promote --wait` / `--deploy`.
//
// These run the REAL release path through the real hostedStore against
// fakeDeployService, so the promotion the wait is handed is the one the server
// actually wrote — not a value the test chose.

// capturedWait records the options followPromote handed the hosted wait, and
// answers with a stated outcome. Installed over runPromoteWait, which exists
// for exactly this: the fact under test is WHICH promotion the wait was scoped
// to, and that is only observable here.
type capturedWait struct {
	calls []envWaitOptions
	env   []string
	err   error
}

func (c *capturedWait) install(t *testing.T) {
	t.Helper()
	prev := runPromoteWait
	runPromoteWait = func(_ context.Context, env string, opts envWaitOptions) error {
		c.env = append(c.env, env)
		c.calls = append(c.calls, opts)
		return c.err
	}
	t.Cleanup(func() { runPromoteWait = prev })
}

// capturedClientDeploy records the self-managed client-side apply. Installed
// over runPromoteClientDeploy for the same reason: the facts under test are
// THAT the apply ran for a file-ledger env and with which options, and the
// real apply needs a cluster.
type capturedClientDeploy struct {
	calls []deployOptions
	env   []string
	err   error
	// preflights are the PRE-WRITE preflight runs (preflightOnly), kept
	// apart from calls so "the apply ran once" still counts applies.
	preflights   []deployOptions
	preflightErr error
}

// promoteClientDeployStubbed records that SOME stub owns the client-side
// apply, so runHostedPromote does not install a second one over it. Tests in
// this package run sequentially within a file and never in parallel, which is
// what makes a package-level flag sound here.
var promoteClientDeployStubbed bool

func (c *capturedClientDeploy) install(t *testing.T) {
	t.Helper()
	prev := runPromoteClientDeploy
	promoteClientDeployStubbed = true
	t.Cleanup(func() { promoteClientDeployStubbed = false })
	runPromoteClientDeploy = func(_ context.Context, env string, opts deployOptions) error {
		if opts.preflightOnly {
			c.preflights = append(c.preflights, opts)
			return c.preflightErr
		}
		c.env = append(c.env, env)
		c.calls = append(c.calls, opts)
		return c.err
	}
	t.Cleanup(func() { runPromoteClientDeploy = prev })
}

// waitByDefault is the follow-through a bare `forge env deploy <env> vX`
// produces: no flag set, so the health gate is ON.
func waitByDefault() *promoteFollowOptions { return &promoteFollowOptions{} }

// selfManagedFixture is a SELF-MANAGED env's ledger: prod on v1, with v2
// available. In-memory rather than the file store because what these tests
// assert is which half of the follow-through ran, not how a jsonl line is
// appended — promote_test.go's TestFileLedger_CompareAndSet owns that.
func selfManagedFixture() (*memBindingStore, *memReleaseLedger) {
	return newMemBindingStore(map[string]release.Promotion{"prod": {ID: "p-1", Release: "v1"}}),
		newMemReleaseLedger(
			rel("v1", "2026-01-01T00:00:00Z", "", false, map[string]string{"api": sha("1")}),
			rel("v2", "2026-02-01T00:00:00Z", "", false, map[string]string{"api": sha("2")}),
		)
}

// ─── Hosted: the control plane converges, forge waits ────────────────────────

// TestDeployRelease_WaitsByDefault is the headline of V3. No flag asked for
// the gate; `deploy` means record + apply + wait, so it ran.
//
// The old spelling was `promote --wait`, and every pipeline that forgot the
// flag reported success before any byte had moved.
func TestDeployRelease_WaitsByDefault(t *testing.T) {
	var wait capturedWait
	wait.install(t)

	_, store := hostedPromoteFixture(t, "v1")
	if _, err := runHostedPromote(t, store, "v2", promoteOptions{Follow: waitByDefault()}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if len(wait.calls) != 1 {
		t.Fatalf("a deploy with NO flags ran the health gate %d time(s), want 1 — waiting is the default", len(wait.calls))
	}
}

// TestDeployRelease_WaitsOnThePromotionTheWriteReturned is why the wait is not
// just "record; env status --wait".
//
// If the wait re-read the env's CURRENT promotion, a hotfix landing in the
// seconds after this deploy would silently become the thing being waited on —
// and the pipeline would report its own release healthy on the strength of
// somebody else's. So the id comes from plan.Recorded, the entry the ledger
// returned.
func TestDeployRelease_WaitsOnThePromotionTheWriteReturned(t *testing.T) {
	var wait capturedWait
	wait.install(t)

	_, store := hostedPromoteFixture(t, "v1")
	if _, err := runHostedPromote(t, store, "v2", promoteOptions{Follow: waitByDefault()}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	written, _, _ := store.Current(context.Background(), "prod")
	if written.Release != "v2" {
		t.Fatalf("the release deploy did not land: prod is on %s", written.Release)
	}
	if len(wait.calls) != 1 {
		t.Fatalf("the wait ran %d time(s), want 1", len(wait.calls))
	}
	if got := wait.calls[0].PromotionID; got != written.ID {
		t.Fatalf("the wait was scoped to %q, want the promotion the write RETURNED (%q)", got, written.ID)
	}
	if wait.env[0] != "prod" {
		t.Errorf("the wait ran against env %q", wait.env[0])
	}
}

// A deploy the ledger treated as an idempotent NO-OP still waits — on the
// EXISTING promotion. A CI retry whose first attempt landed and whose wait then
// timed out must be able to re-run the whole step and still gate on its own
// release.
func TestDeployRelease_NoOpRetryWaitsOnTheExistingPromotion(t *testing.T) {
	var wait capturedWait
	wait.install(t)

	_, store := hostedPromoteFixture(t, "v1", "v2")
	existing, _, _ := store.Current(context.Background(), "prod")

	if _, err := runHostedPromote(t, store, "v2", promoteOptions{Follow: waitByDefault()}); err != nil {
		t.Fatalf("re-deploying the release prod already runs must be a no-op that still waits: %v", err)
	}
	if len(wait.calls) != 1 {
		t.Fatalf("a no-op deploy must still wait, ran %d time(s)", len(wait.calls))
	}
	if got := wait.calls[0].PromotionID; got != existing.ID {
		t.Fatalf("the wait was scoped to %q, want the existing promotion %q", got, existing.ID)
	}
}

// The wait's exit code IS the deploy's: a deploy whose release rolled out
// degraded must not report success because the pointer moved.
func TestDeployRelease_TheWaitsExitCodeIsTheDeploys(t *testing.T) {
	wait := capturedWait{err: &exitCodeError{code: exitTimedOut, msg: "still progressing"}}
	wait.install(t)

	_, store := hostedPromoteFixture(t, "v1")
	_, err := runHostedPromote(t, store, "v2", promoteOptions{Follow: waitByDefault()})
	if got := exitCodeForError(err); got != exitTimedOut {
		t.Fatalf("exit code = %d, want the wait's %d (%v)", got, exitTimedOut, err)
	}
	// And the promotion still LANDED. The gate reporting red does not undo
	// the pointer — recovery is roll forward, and a binding silently
	// reverted by its own gate would be the worst of both.
	if cur, _, _ := store.Current(context.Background(), "prod"); cur.Release != "v2" {
		t.Fatalf("a failed wait must not revert the recorded release: prod is on %s", cur.Release)
	}
}

// TestDeployRelease_FailedWaitStillEmitsTheJSONDocument is the write/wait seam,
// and it is the case that matters MOST for a pipeline.
//
// A wait that degrades, times out or is superseded arrives in runPromote as a
// post-plan error — the same shape a FAILED WRITE has. A failed write has no
// document worth reading because nothing was recorded. A failed WAIT is the
// opposite: the promotion was applied, `recorded.id` exists, and that id is
// precisely what the next step needs.
//
// The scaffolded release workflow does `deploy --json > deploy.json` and then
// `jq -r .recorded.id` to record gates against the promotion. A failed rollout
// is exactly when that evidence must be recorded, so dropping the document
// leaves `gate record` with no promotion id — the gate evidence for the bad
// release is the evidence that goes missing.
//
// So: the document is emitted whenever the plan was APPLIED or refused, and its
// exit_code is the wait's.
func TestDeployRelease_FailedWaitStillEmitsTheJSONDocument(t *testing.T) {
	wait := capturedWait{err: &exitCodeError{code: exitTimedOut, msg: "still progressing"}}
	wait.install(t)

	_, store := hostedPromoteFixture(t, "v1")
	out, err := runHostedPromote(t, store, "v2", promoteOptions{JSON: true, Follow: waitByDefault()})
	if got := exitCodeForError(err); got != exitTimedOut {
		t.Fatalf("exit code = %d, want the wait's %d (%v)", got, exitTimedOut, err)
	}

	var doc map[string]any
	if jerr := json.Unmarshal([]byte(out), &doc); jerr != nil {
		t.Fatalf("a failed wait after an APPLIED write must still emit the document: %v\nstdout=%q", jerr, out)
	}
	// applied:true is the fact that distinguishes this from a refusal. The
	// pointer DID move; only the gate went red.
	if doc["applied"] != true {
		t.Errorf("applied = %v, want true — the promotion landed", doc["applied"])
	}
	// The envelope carries the WAIT's code, so the document and the process
	// status still agree.
	if doc["ok"] != false || doc["exit_code"] != float64(exitTimedOut) {
		t.Errorf("ok/exit_code = %v/%v, want false/%d", doc["ok"], doc["exit_code"], exitTimedOut)
	}
	// recorded.id is what CI pipes into `gate record`.
	recorded, ok := doc["recorded"].(map[string]any)
	if !ok {
		t.Fatalf("`recorded` must be present — CI reads recorded.id to record gates against this promotion:\n%s", out)
	}
	written, _, _ := store.Current(context.Background(), "prod")
	if recorded["id"] != written.ID {
		t.Errorf("recorded.id = %v, want the promotion that landed (%q)", recorded["id"], written.ID)
	}
	if _, present := doc["refusal"]; present {
		t.Errorf("a failed WAIT is not a refusal — nothing declined the write:\n%s", out)
	}
}

// The same seam in TEXT mode: the change set still prints, so a human sees what
// moved before they read why the gate went red.
func TestDeployRelease_FailedWaitStillRendersTheChangeSet(t *testing.T) {
	wait := capturedWait{err: &exitCodeError{code: exitWrong, msg: "api: CrashLoopBackOff"}}
	wait.install(t)

	_, store := hostedPromoteFixture(t, "v1")
	out, err := runHostedPromote(t, store, "v2", promoteOptions{Follow: waitByDefault()})
	if got := exitCodeForError(err); got != exitWrong {
		t.Fatalf("exit code = %d, want %d (%v)", got, exitWrong, err)
	}
	if !strings.Contains(out, "v2") {
		t.Errorf("the change set must still render after a failed wait, got:\n%s", out)
	}
}

// --timeout and --fail-fast reach the wait rather than being accepted and
// ignored.
func TestDeployRelease_TuningFlagsReachTheWait(t *testing.T) {
	var wait capturedWait
	wait.install(t)

	_, store := hostedPromoteFixture(t, "v1")
	if _, err := runHostedPromote(t, store, "v2", promoteOptions{
		Follow: &promoteFollowOptions{Timeout: 90 * time.Second, FailFast: true},
	}); err != nil {
		t.Fatal(err)
	}
	got := wait.calls[0]
	if got.Timeout != 90*time.Second || !got.FailFast {
		t.Fatalf("timeout/fail-fast = %s/%v, want 90s/true", got.Timeout, got.FailFast)
	}
}

// --no-wait records and has the release applied, but runs no gate. The hosted
// env is converged server-side, so there is nothing for forge to apply either
// — and the message has to say where the gate went, or an operator who opted
// out cannot tell "forge is done" from "the release is live".
func TestDeployRelease_NoWaitSkipsTheGateAndSaysWhereItWent(t *testing.T) {
	var wait capturedWait
	wait.install(t)

	_, store := hostedPromoteFixture(t, "v1")
	out, err := runHostedPromote(t, store, "v2", promoteOptions{
		Follow: &promoteFollowOptions{NoWait: true},
	})
	if err != nil {
		t.Fatalf("--no-wait: %v", err)
	}
	if len(wait.calls) != 0 {
		t.Fatalf("--no-wait ran the health gate %d time(s)", len(wait.calls))
	}
	// The write still happened: --no-wait drops the gate, not the deploy.
	if cur, _, _ := store.Current(context.Background(), "prod"); cur.Release != "v2" {
		t.Fatalf("--no-wait must still record: prod is on %s", cur.Release)
	}
	if !strings.Contains(out, "forge env status prod --wait") {
		t.Errorf("--no-wait must name how to gate on it later, got:\n%s", out)
	}
}

// A REFUSED release deploy must not wait and must not apply. There is nothing
// to wait on — the pointer did not move — and waiting would gate on whatever
// promotion is current, which belongs to whoever won the race.
func TestDeployRelease_RefusedDeployNeitherAppliesNorWaits(t *testing.T) {
	var wait capturedWait
	var apply capturedClientDeploy
	wait.install(t)
	apply.install(t)

	fake, store := hostedPromoteFixture(t, "v1")
	fake.pinned = true
	_, err := runHostedPromote(t, store, "v2", promoteOptions{Follow: waitByDefault()})
	if got := exitCodeForError(err); got != exitRefused {
		t.Fatalf("exit code = %d, want %d (%v)", got, exitRefused, err)
	}
	if len(wait.calls) != 0 {
		t.Fatalf("a refused deploy waited %d time(s)", len(wait.calls))
	}
	if len(apply.calls) != 0 {
		t.Fatalf("a refused deploy applied %d time(s)", len(apply.calls))
	}
}

// --plan writes nothing, so it applies nothing and waits for nothing either. A
// preview that blocked for fifteen minutes would not be a preview.
func TestDeployRelease_PlanNeitherAppliesNorWaits(t *testing.T) {
	var wait capturedWait
	var apply capturedClientDeploy
	wait.install(t)
	apply.install(t)

	_, store := hostedPromoteFixture(t, "v1")
	if _, err := runHostedPromote(t, store, "v2", promoteOptions{
		DryRun: true, Follow: waitByDefault(),
	}); err != nil {
		t.Fatal(err)
	}
	if len(wait.calls) != 0 || len(apply.calls) != 0 {
		t.Fatalf("--plan applied %d time(s) and waited %d time(s)", len(apply.calls), len(wait.calls))
	}
}

// ─── Self-managed: this command applies it ───────────────────────────────────

// TestDeployRelease_SelfManagedAppliesClientSide is the other half of the
// hosted/self-managed parity. A file-ledger env has nothing watching its
// ledger, so the same command renders the binding it just wrote and applies it
// from here — rather than waiting on a server-computed rollout that nothing
// will ever produce.
func TestDeployRelease_SelfManagedAppliesClientSide(t *testing.T) {
	var wait capturedWait
	var apply capturedClientDeploy
	wait.install(t)
	apply.install(t)

	store, releases := selfManagedFixture()
	opts := promoteOptions{
		ProjectDir: t.TempDir(), Git: allCommitsPresent(), Follow: waitByDefault(),
		Ledger: envLedger{Bindings: store, Releases: releases},
	}
	opts.Run.None = true
	var err error
	captureStdout(t, func() { err = runPromote(context.Background(), "v2", "prod", opts) })
	if err != nil {
		t.Fatalf("self-managed deploy: %v", err)
	}
	if len(apply.calls) != 1 {
		t.Fatalf("a self-managed deploy ran the client-side apply %d time(s), want 1", len(apply.calls))
	}
	if apply.env[0] != "prod" {
		t.Errorf("the apply ran against env %q", apply.env[0])
	}
	// And NOT the hosted wait: there is no server-computed rollout, so
	// polling one could only ever time out and would blame the release for
	// a converger that was never going to run.
	if len(wait.calls) != 0 {
		t.Fatalf("a self-managed deploy polled the hosted rollout %d time(s)", len(wait.calls))
	}
}

// ─── Mixed: the control plane converges its half, forge applies the rest ─────

// mixedLedger is a MIXED env's ledger: a control plane that converges one
// hosted tier, beside workloads forge applies from here. The hosted tier is
// what makes it two halves — without one the env is ledger-only, and its
// control plane has nothing to wait on (deploy_promote_convergence_test.go).
func mixedLedger(store *hostedStore) envLedger {
	return envLedger{Bindings: store, Releases: store, Hosted: true, Mixed: true,
		HostedTiers: []hostedTier{{Name: "api", Kind: hostedTierWorkload}}}
}

// TestDeployRelease_MixedEnvAppliesItsClusterHalfAndWaits is the third shape,
// and the one a two-way `hosted bool` could not express. A MIXED env keeps its
// ledger on a control plane AND declares workloads that control plane does not
// run — a cluster Deployment, a compose service, infra, a shipped frontend.
//
// Nothing converges that half. So a deploy that only recorded and waited on the
// hosted rollout reported the release live while the cluster workloads still
// ran the previous one — the precise failure V3 set out to close, reintroduced
// for the env shape most likely to be mid-migration. Both halves must run: the
// client-side apply for what forge owns, and the hosted wait for what the
// control plane owns.
func TestDeployRelease_MixedEnvAppliesItsClusterHalfAndWaits(t *testing.T) {
	var wait capturedWait
	var apply capturedClientDeploy
	wait.install(t)
	apply.install(t)

	_, store := hostedPromoteFixture(t, "v1")
	if _, err := runHostedPromote(t, store, "v2", promoteOptions{
		Ledger: mixedLedger(store),
		Follow: waitByDefault(),
	}); err != nil {
		t.Fatalf("mixed deploy: %v", err)
	}
	if len(apply.calls) != 1 {
		t.Fatalf("a mixed env ran the client-side apply %d time(s), want 1 — "+
			"no control plane converges its cluster half", len(apply.calls))
	}
	if len(wait.calls) != 1 {
		t.Fatalf("a mixed env waited on the hosted rollout %d time(s), want 1 — "+
			"its hosted half IS converged server-side", len(wait.calls))
	}
}

// On a mixed env the apply runs BEFORE the hosted wait, and a failed apply
// short-circuits it. Waiting out a 15-minute hosted budget after the half this
// command owns has already failed costs the pipeline the time and tells it
// nothing it did not know.
func TestDeployRelease_MixedEnvApplyFailureSkipsTheHostedWait(t *testing.T) {
	var wait capturedWait
	wait.install(t)
	apply := capturedClientDeploy{err: &exitCodeError{code: exitWrong, msg: "worker: CrashLoopBackOff"}}
	apply.install(t)

	_, store := hostedPromoteFixture(t, "v1")
	_, err := runHostedPromote(t, store, "v2", promoteOptions{
		Ledger: mixedLedger(store),
		Follow: waitByDefault(),
	})
	if got := exitCodeForError(err); got != exitWrong {
		t.Fatalf("exit code = %d, want the apply's %d (%v)", got, exitWrong, err)
	}
	if len(wait.calls) != 0 {
		t.Fatalf("a failed cluster apply must not go on to wait on the hosted half, waited %d time(s)", len(wait.calls))
	}
}

// --no-wait on a mixed env still APPLIES. It removes the gate, never the
// deploy, and the cluster half does not ship at all unless this command sends
// it.
func TestDeployRelease_MixedEnvNoWaitStillApplies(t *testing.T) {
	var wait capturedWait
	var apply capturedClientDeploy
	wait.install(t)
	apply.install(t)

	_, store := hostedPromoteFixture(t, "v1")
	if _, err := runHostedPromote(t, store, "v2", promoteOptions{
		Ledger: mixedLedger(store),
		Follow: &promoteFollowOptions{NoWait: true},
	}); err != nil {
		t.Fatalf("mixed --no-wait: %v", err)
	}
	if len(apply.calls) != 1 {
		t.Fatalf("--no-wait dropped the cluster apply (%d call(s)); it drops the GATE, not the deploy", len(apply.calls))
	}
	if len(wait.calls) != 0 {
		t.Fatalf("--no-wait ran the hosted gate %d time(s)", len(wait.calls))
	}
}

// A self-managed apply that fails makes the deploy fail — the release is not
// live, and reporting success because the ledger entry landed is the gap this
// verb exists to close.
func TestDeployRelease_SelfManagedApplyFailureFailsTheDeploy(t *testing.T) {
	apply := capturedClientDeploy{err: &exitCodeError{code: exitWrong, msg: "api: CrashLoopBackOff"}}
	apply.install(t)

	store, releases := selfManagedFixture()
	opts := promoteOptions{
		ProjectDir: t.TempDir(), Git: allCommitsPresent(), Follow: waitByDefault(),
		Ledger: envLedger{Bindings: store, Releases: releases},
	}
	opts.Run.None = true
	var err error
	captureStdout(t, func() { err = runPromote(context.Background(), "v2", "prod", opts) })
	if got := exitCodeForError(err); got != exitWrong {
		t.Fatalf("exit code = %d, want the apply's %d (%v)", got, exitWrong, err)
	}
	// The entry still landed. Recovery is roll forward.
	if cur, _, _ := store.Current(context.Background(), "prod"); cur.Release != "v2" {
		t.Fatalf("a failed apply must not revert the recorded release: prod is on %s", cur.Release)
	}
}

// The self-managed apply's rollout wait IS its health gate, so --no-wait,
// --timeout and --fail-fast are mapped onto that policy rather than onto a
// server-side wait that has nothing to read.
func TestDeployRelease_SelfManagedGateFlagsTuneTheRolloutPolicy(t *testing.T) {
	cases := []struct {
		name   string
		follow *promoteFollowOptions
		want   cluster.RolloutPolicy
	}{
		{
			"default waits",
			&promoteFollowOptions{},
			cluster.RolloutPolicy{},
		},
		{
			"--no-wait skips the rollout wait",
			&promoteFollowOptions{NoWait: true},
			cluster.RolloutPolicy{Mode: cluster.RolloutSkip},
		},
		{
			"--timeout bounds it",
			&promoteFollowOptions{Timeout: 90 * time.Second},
			cluster.RolloutPolicy{Timeout: 90 * time.Second},
		},
		{
			"--fail-fast stops at the first failure",
			&promoteFollowOptions{FailFast: true},
			cluster.RolloutPolicy{FailFast: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var apply capturedClientDeploy
			apply.install(t)

			store, releases := selfManagedFixture()
			opts := promoteOptions{
				ProjectDir: t.TempDir(), Git: allCommitsPresent(), Follow: tc.follow,
				Ledger: envLedger{Bindings: store, Releases: releases},
			}
			opts.Run.None = true
			var err error
			captureStdout(t, func() { err = runPromote(context.Background(), "v2", "prod", opts) })
			if err != nil {
				t.Fatal(err)
			}
			if len(apply.calls) != 1 {
				t.Fatalf("the apply ran %d time(s), want 1", len(apply.calls))
			}
			if got := apply.calls[0].rollout; got.Mode != tc.want.Mode || got.Timeout != tc.want.Timeout || got.FailFast != tc.want.FailFast {
				t.Fatalf("rollout policy = %+v, want mode/timeout/fail-fast %v/%s/%v",
					got, tc.want.Mode, tc.want.Timeout, tc.want.FailFast)
			}
		})
	}
}

// The apply flags a release deploy was given reach the client-side apply, so
// `deploy prod v1.4.0 --target api` and `deploy prod --target api` apply the
// same thing. Without this the release path would silently ignore half the
// verb's own flag surface.
func TestDeployRelease_SelfManagedForwardsTheApplyFlags(t *testing.T) {
	var apply capturedClientDeploy
	apply.install(t)

	store, releases := selfManagedFixture()
	opts := promoteOptions{
		ProjectDir: t.TempDir(), Git: allCommitsPresent(),
		Ledger: envLedger{Bindings: store, Releases: releases},
		Follow: &promoteFollowOptions{clientDeploy: deployOptions{
			targets: []string{"api"}, namespace: "custom-ns", dryRun: true,
		}},
	}
	opts.Run.None = true
	var err error
	captureStdout(t, func() { err = runPromote(context.Background(), "v2", "prod", opts) })
	if err != nil {
		t.Fatal(err)
	}
	got := apply.calls[0]
	if len(got.targets) != 1 || got.targets[0] != "api" || got.namespace != "custom-ns" || !got.dryRun {
		t.Fatalf("the apply got %+v, want --target api --namespace custom-ns --dry-run", got)
	}
}

// ─── The ledger-only callers ─────────────────────────────────────────────────

// A nil Follow is ledger-write only: nothing applied, nothing waited on. That
// is what `forge release`'s fixtures and the plan/CAS/gates tests use, and it
// has to stay inert — otherwise asserting what was WRITTEN would need a
// cluster.
func TestDeployRelease_NilFollowNeitherAppliesNorWaits(t *testing.T) {
	var wait capturedWait
	var apply capturedClientDeploy
	wait.install(t)
	apply.install(t)

	_, store := hostedPromoteFixture(t, "v1")
	if _, err := runHostedPromote(t, store, "v2", promoteOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(wait.calls) != 0 || len(apply.calls) != 0 {
		t.Fatalf("a nil Follow applied %d time(s) and waited %d time(s)", len(apply.calls), len(wait.calls))
	}
}

// ─── Validation ──────────────────────────────────────────────────────────────

// TestDeployRelease_ContradictoryGateFlagsRefuseBeforeAnyWrite: a gate-tuning
// flag beside --no-wait contradicts itself, and silently resolving it either
// way is the failure worth refusing — a pipeline that passed --fail-fast and
// got no gate would believe it had one. It refuses BEFORE the write, so the
// pointer does not move for a command that was going to fail anyway.
func TestDeployRelease_ContradictoryGateFlagsRefuseBeforeAnyWrite(t *testing.T) {
	cases := map[string]promoteFollowOptions{
		"--fail-fast with --no-wait": {NoWait: true, FailFast: true},
		"--timeout with --no-wait":   {NoWait: true, Timeout: time.Minute},
	}
	for name, follow := range cases {
		t.Run(name, func(t *testing.T) {
			fake, store := hostedPromoteFixture(t, "v1")
			before := fake.callCount(procPromote)
			_, err := runHostedPromote(t, store, "v2", promoteOptions{Follow: &follow})
			if err == nil || !strings.Contains(err.Error(), "--no-wait") {
				t.Fatalf("%s must be refused naming --no-wait, got %v", name, err)
			}
			if n := fake.callCount(procPromote); n != before {
				t.Fatalf("%s refused AFTER writing: Promote called %d time(s)", name, n-before)
			}
		})
	}
}

// validatePromoteFollow runs BEFORE the plan, which is what makes an invalid
// combination refuse the whole command rather than moving the pointer first.
func TestValidatePromoteFollow(t *testing.T) {
	cases := []struct {
		name    string
		in      promoteFollowOptions
		wantErr bool
	}{
		{"the default (wait)", promoteFollowOptions{}, false},
		{"no-wait alone", promoteFollowOptions{NoWait: true}, false},
		{"wait with tuning", promoteFollowOptions{Timeout: time.Minute, FailFast: true}, false},
		{"fail-fast with no-wait", promoteFollowOptions{NoWait: true, FailFast: true}, true},
		{"timeout with no-wait", promoteFollowOptions{NoWait: true, Timeout: time.Minute}, true},
		{"negative timeout", promoteFollowOptions{Timeout: -time.Second}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validatePromoteFollow(tc.in); (err != nil) != tc.wantErr {
				t.Fatalf("validatePromoteFollow(%+v) = %v, wantErr %v", tc.in, err, tc.wantErr)
			}
		})
	}
}

// A hosted-ledger env whose clusters are all hub-converged applies nothing
// from here and publishes nothing: the control plane's reconciler owns it.
// The wait still runs on the promotion that was written.
func TestDeployRelease_HubConvergedEnvDoesNoClientApply(t *testing.T) {
	var wait capturedWait
	var apply capturedClientDeploy
	wait.install(t)
	apply.install(t)

	_, store := hostedPromoteFixture(t, "v1")
	if _, err := runHostedPromote(t, store, "v2", promoteOptions{
		Ledger: envLedger{Bindings: store, Releases: store, Hosted: true, Units: allHubUnits()},
		Follow: waitByDefault(),
	}); err != nil {
		t.Fatalf("hub-converged deploy: %v", err)
	}
	if len(apply.calls) != 0 {
		t.Fatalf("a hub-converged env ran the client-side apply %d time(s), want 0", len(apply.calls))
	}
	if len(wait.calls) != 1 {
		t.Fatalf("waited %d time(s), want 1", len(wait.calls))
	}
}

// The inference an undeclared env keeps: bound clusters and nothing the hub
// cannot converge means the hub converges the whole env.
func TestUnitSplit_InferredHubConvergence(t *testing.T) {
	bound := map[string]string{"gke_prod": "prod-control-plane"}
	cp := &ControlPlaneEntity{Endpoint: "https://cp.example"}
	cases := []struct {
		name string
		e    *KCLEntities
		want bool
	}{
		{"nil", nil, false},
		{"no control plane", &KCLEntities{ConnectedClusters: bound}, false},
		{"bound clusters only", &KCLEntities{ControlPlane: cp, ConnectedClusters: bound}, true},
		{"no bound cluster", &KCLEntities{ControlPlane: cp}, false},
		{"hosted database", &KCLEntities{ControlPlane: cp, ConnectedClusters: bound, Databases: []DatabaseEntity{{Name: "d", Runtime: RuntimeHosted}}}, false},
	}
	for _, c := range cases {
		split, err := unitSplitOf(c.e)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := split.AllHub(); got != c.want {
			t.Errorf("%s: AllHub = %v, want %v (%s)", c.name, got, c.want, split.Describe())
		}
	}
}

// ─── An env mid-move onto the hub ────────────────────────────────────────────

// splitLedger is a hosted ledger whose delivery units are split: the hub
// converges `reliant`, forge applies `base` and `infra`.
func splitLedger(store *hostedStore) envLedger {
	units := unitSplit{Hub: []string{"reliant"}, Forge: []string{"base", "infra"}, Source: unitSplitDeclared}
	return envLedger{Bindings: store, Releases: store, Hosted: true, Units: units, Mixed: true}
}

// THE FOLLOW-THROUGH OF A SPLIT ENV IS BOTH HALVES, IN ORDER. forge applies the
// units it still owns, and THEN waits on what the hub reports for the units it
// converges. Either half alone would report a release live while the other half
// still ran the previous one.
func TestDeployRelease_SplitEnvAppliesItsUnitsThenWaitsOnTheHub(t *testing.T) {
	var order []string
	var wait capturedWait
	var apply capturedClientDeploy
	wait.install(t)
	apply.install(t)
	prevWait, prevApply := runPromoteWait, runPromoteClientDeploy
	t.Cleanup(func() { runPromoteWait, runPromoteClientDeploy = prevWait, prevApply })
	runPromoteWait = func(ctx context.Context, env string, o envWaitOptions) error {
		order = append(order, "wait")
		return prevWait(ctx, env, o)
	}
	runPromoteClientDeploy = func(ctx context.Context, env string, o deployOptions) error {
		if !o.preflightOnly {
			order = append(order, "apply")
		}
		return prevApply(ctx, env, o)
	}

	_, store := hostedPromoteFixture(t, "v1")
	if _, err := runHostedPromote(t, store, "v2", promoteOptions{
		Ledger: splitLedger(store),
		Follow: waitByDefault(),
	}); err != nil {
		t.Fatalf("split deploy: %v", err)
	}
	if got := strings.Join(order, ","); got != "apply,wait" {
		t.Fatalf("follow-through ran %q, want apply then wait: forge's units first, then the hub's", got)
	}
}

// An env whose every unit the hub converges applies nothing from here and
// publishes nothing: the recorded bundle is all the hub needs.
func TestDeployRelease_FullyMovedEnvAppliesNothingFromHere(t *testing.T) {
	var wait capturedWait
	var apply capturedClientDeploy
	wait.install(t)
	apply.install(t)

	_, store := hostedPromoteFixture(t, "v1")
	ledger := envLedger{Bindings: store, Releases: store, Hosted: true,
		Units: unitSplit{Hub: []string{"base", "reliant"}, Source: unitSplitDeclared}}
	if _, err := runHostedPromote(t, store, "v2", promoteOptions{Ledger: ledger, Follow: waitByDefault()}); err != nil {
		t.Fatalf("fully moved deploy: %v", err)
	}
	if len(apply.calls) != 0 {
		t.Errorf("an env the hub converges whole was applied from here %d time(s)", len(apply.calls))
	}
	if len(wait.calls) != 1 {
		t.Errorf("waited %d time(s), want 1: the hub is the only thing left to wait on", len(wait.calls))
	}
}

// FRONTENDS SHIP AFTER THE PROMOTION IS RECORDED. The pre-record pass renders
// and checks (it can refuse: no `firebase` CLI, a mock build), but ships
// nothing: a frontend that went live before the binding moved would serve a
// release the ledger does not yet say is promoted.
//
// Proved against the record itself: each client-side run notes how many
// promotions the control plane had by then.
func TestDeployRelease_FrontendsShipAfterThePromotionIsRecorded(t *testing.T) {
	var wait capturedWait
	var apply capturedClientDeploy
	wait.install(t)
	apply.install(t)

	type run struct {
		preflight bool
		dryRun    bool
		promoted  int
	}
	var runs []run
	fake, store := hostedPromoteFixture(t, "v1")
	useGateBackend(t, gateBackend{hosted: true, store: newGateTestStore(t, fake), bindings: store})
	prev := runPromoteClientDeploy
	t.Cleanup(func() { runPromoteClientDeploy = prev })
	runPromoteClientDeploy = func(ctx context.Context, env string, o deployOptions) error {
		runs = append(runs, run{o.preflightOnly, o.dryRun, fake.callCount(procPromote)})
		return prev(ctx, env, o)
	}

	seeded := fake.callCount(procPromote) // the fixture promotes v1 to start
	if _, err := runHostedPromote(t, store, "v2", promoteOptions{Ledger: splitLedger(store), Follow: waitByDefault()}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("expected a pre-record preflight and one shipping apply, got %+v", runs)
	}
	pre, ship := runs[0], runs[1]
	pre.promoted, ship.promoted = pre.promoted-seeded, ship.promoted-seeded
	if !pre.preflight || !pre.dryRun || pre.promoted != 0 {
		t.Errorf("the pre-record pass must be a dry-run preflight before any promotion is written, got %+v", pre)
	}
	if ship.preflight || ship.dryRun || ship.promoted != 1 {
		t.Errorf("the shipping apply (which carries the frontends) must run after the promotion is recorded, got %+v", ship)
	}
}

// The apply gate says who applied what, so a ledger reader asking whether forge
// touched the `reliant` unit on a promotion need not re-render an old KCL file.
func TestWithUnitSplit_RecordsWhoAppliedWhat(t *testing.T) {
	g := withUnitSplit(release.Gate{Name: applyGateName, Status: release.GateStatusPassed},
		unitSplit{Hub: []string{"reliant"}, Forge: []string{"base", "infra"}, Source: unitSplitDeclared})
	if got := g.Details[applyGateUnitsHub]; !reflect.DeepEqual(got, []string{"reliant"}) {
		t.Errorf("units_hub = %v", got)
	}
	if got := g.Details[applyGateUnitsForge]; !reflect.DeepEqual(got, []string{"base", "infra"}) {
		t.Errorf("units_forge = %v", got)
	}
	// An env that declares no split leaves the gate as it always was.
	plain := withUnitSplit(release.Gate{Name: applyGateName}, unitSplit{})
	if plain.Details != nil {
		t.Errorf("an undeclared split added details to the gate: %v", plain.Details)
	}
	// The baseline details a ledger-only env's next plan reads survive.
	withDigest := applyOutcomeGate(time.Now(), time.Now(), nil, "sha256:abc", "")
	both := withUnitSplit(withDigest, unitSplit{Hub: []string{"reliant"}, Source: unitSplitDeclared})
	if both.Details[applyGateBundleDigest] != "sha256:abc" {
		t.Errorf("recording the split lost the applied bundle digest: %v", both.Details)
	}
}
