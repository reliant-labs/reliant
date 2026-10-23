package deploytarget

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// The phase value names this test states but the implementation does not
// branch on. Spelled literally HERE rather than exported from the
// implementation, so a typo in either one fails the test instead of moving
// with it — the same reason fakeDeployService spells the proto's field names.
const (
	phaseProgressing = "DEPLOY_ROLLOUT_PHASE_PROGRESSING"
	phaseDegraded    = "DEPLOY_ROLLOUT_PHASE_DEGRADED"
)

// Tests for the GetRollout-backed deploy wait (control-plane
// docs/design/hosted-deploy-primitives.md §3.2, task F3).
//
// The point of each case is a question the GetStatus poll CANNOT answer, so
// each one fails against the previous implementation: a rollout scoped to a
// promotion, a workload whose observation claims the new digest while no new
// pod serves it, and an overtaking promote.

// rolloutCP is a control plane that serves GetRollout as well as the publish
// RPCs, scripting one rollout phase per GetRollout call (the last repeats).
type rolloutCP struct {
	mu     sync.Mutex
	calls  []fakeCall
	phases []string
	nRoll  int
	// workloads overrides the default pinned set.
	workloads string
	// unpinned is the rollout's unpinned rows, as raw JSON.
	unpinned string
	// rolloutErr, when set, is the Connect code GetRollout fails with.
	rolloutErr string
	// holds is the rollout's holds, as raw JSON (a HELD phase carries them).
	holds string
}

func (f *rolloutCP) Call(_ context.Context, proc string, req, out any) error {
	raw, _ := json.Marshal(req)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{Proc: proc, Body: body})
	short := proc[strings.LastIndex(proc, "/")+1:]
	var reply string
	switch short {
	case "EnsureEnvironment":
		reply = `{"environment":{"id":"env-1","name":"prod","namespace":"env-env-1"},"created":true}`
	case "EnsureDeployment":
		reply = fmt.Sprintf(`{"deployment":{"id":"dep-%s","name":%q},"created":true}`, body["name"], body["name"])
	case "PublishDeploymentConfig":
		reply = `{"digest":"sha256:cfg","reference":"reg/cfg@sha256:cfg"}`
	case "GetStatus":
		// Deliberately NOT ready: every readiness verdict in these
		// tests must come from the rollout, so a case that passes
		// could not have passed on the status poll.
		reply = `{"environmentVerdict":"DEPLOY_VERDICT_CONVERGING","deployments":[
		 {"deployment":{"id":"dep-api","name":"api","observed":{"state":"DEPLOY_OBSERVED_STATE_PENDING"}},"verdict":"DEPLOY_VERDICT_CONVERGING"},
		 {"deployment":{"id":"dep-orders","name":"orders","observed":{"state":"DEPLOY_OBSERVED_STATE_PENDING"}},"verdict":"DEPLOY_VERDICT_CONVERGING"}]}`
	case "GetRollout":
		if f.rolloutErr != "" {
			code := f.rolloutErr
			f.mu.Unlock()
			return &codedTestError{code: code}
		}
		phase := f.phases[len(f.phases)-1]
		if f.nRoll < len(f.phases) {
			phase = f.phases[f.nRoll]
		}
		f.nRoll++
		workloads := f.workloads
		if workloads == "" {
			workloads = fmt.Sprintf(`[{"deploymentId":"dep-api","name":"api","artifact":"ghcr.io/acme/api",
			 "pinnedDigest":%q,"observedDigest":%q,"phase":%q,"observedState":"DEPLOY_OBSERVED_STATE_READY",
			 "updatedReplicas":3,"desiredReplicas":3}]`, digestA, digestA, phase)
		}
		unpinned := f.unpinned
		if unpinned == "" {
			unpinned = `[{"deploymentId":"dep-orders","name":"orders","observedState":"DEPLOY_OBSERVED_STATE_READY","verdict":"DEPLOY_VERDICT_CONVERGED"}]`
		}
		holds := f.holds
		if holds == "" {
			holds = "[]"
		}
		reply = fmt.Sprintf(`{"rollout":{"promotion":{"id":"promo-1","releaseVersion":"v1"},
		 "phase":%q,"workloads":%s,"unpinned":%s,"stabilityWindowMs":"120000",
		 "convergesPromotions":true,"reason":"api: %s","holds":%s}}`,
			phase, workloads, unpinned, rolloutPhaseLabel(phase), holds)
	default:
		f.mu.Unlock()
		return fmt.Errorf("unexpected procedure %s", proc)
	}
	f.mu.Unlock()
	if out == nil {
		return nil
	}
	return json.Unmarshal([]byte(reply), out)
}

func (f *rolloutCP) procs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, c.Proc[strings.LastIndex(c.Proc, "/")+1:])
	}
	return out
}

func (f *rolloutCP) rolloutBodies() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, c := range f.calls {
		if strings.HasSuffix(c.Proc, "/GetRollout") {
			out = append(out, c.Body)
		}
	}
	return out
}

// codedTestError carries one Connect code, which is all the deploy wait reads
// off a control-plane failure (codedWireError).
type codedTestError struct{ code string }

func (e *codedTestError) Error() string            { return "control plane said " + e.code }
func (e *codedTestError) HasCode(code string) bool { return e.code == code }

// promotedGroup is hostedGroup bound to a promotion — which is what switches
// the wait onto GetRollout.
func promotedGroup(promotionID string) ServiceGroup {
	g := hostedGroup("v1", map[string]string{"api": digestA}, v1alpha1.Resources{})
	g.Hosted.PromotionID = promotionID
	return g
}

// TestHostedWaitUsesGetRolloutScopedToThePromotion is the headline: a deploy
// that published a promotion's pins judges "done" by THAT promotion's
// rollout, and it sends the promotion id so the server judges against the
// frozen pins rather than whatever the rows declare now.
//
// This is what stops a promote landing mid-deploy from making the wait
// succeed on bytes it was never asked about.
func TestHostedWaitUsesGetRolloutScopedToThePromotion(t *testing.T) {
	cp := &rolloutCP{phases: []string{wireRolloutPhaseSucceeded}}
	var outcomes []cluster.RolloutObservation
	// A BOUNDED budget, deliberately: this fake's GetStatus answers
	// PENDING forever, so an implementation that ignored the rollout and
	// fell back to the status poll must fail here in milliseconds rather
	// than hanging out the default five-minute policy timeout. A test that
	// catches a regression only by timing out is a test nobody will wait
	// for.
	p := HostedProvider{Client: cp, PollInterval: time.Millisecond,
		Rollout:   cluster.RolloutPolicy{Timeout: 50 * time.Millisecond},
		OnRollout: func(o cluster.RolloutObservation) { outcomes = append(outcomes, o) }}
	if err := p.Deploy(context.Background(), promotedGroup("promo-1")); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	procs := cp.procs()
	if !strings.Contains(strings.Join(procs, ","), "GetRollout") {
		t.Fatalf("the wait never called GetRollout: %v", procs)
	}
	bodies := cp.rolloutBodies()
	if len(bodies) == 0 {
		t.Fatal("no GetRollout body recorded")
	}
	if bodies[0]["environmentId"] != "env-1" || bodies[0]["promotionId"] != "promo-1" {
		t.Fatalf("GetRollout body = %v, want env-1 scoped to promo-1", bodies[0])
	}
	if len(outcomes) != 2 || outcomes[0].State != cluster.RolloutStateReady {
		t.Errorf("rollout outcomes = %+v, want both workloads ready", outcomes)
	}
}

// An UNBOUND deploy (no promotion) keeps the GetStatus poll: there is no
// promotion to scope a rollout to, and sending an empty id would be a claim
// about a promotion with no id.
func TestHostedWaitWithoutAPromotionKeepsTheStatusPoll(t *testing.T) {
	cp := &fakeCP{status: readyStatus(digestA)}
	err := HostedProvider{Client: cp, PollInterval: time.Millisecond}.
		Deploy(context.Background(), hostedGroup("v1", map[string]string{"api": digestA}, v1alpha1.Resources{}))
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if joined := strings.Join(cp.procs(), ","); strings.Contains(joined, "GetRollout") {
		t.Fatalf("an unbound deploy must not call GetRollout: %v", cp.procs())
	}
}

// TestHostedWaitStabilizingCompletesTheDeploy is the DOCUMENTED threshold
// difference: a deploy completes on "published and serving", so STABILIZING
// is done. Treating it as pending would make every deploy as slow as the
// server's stability window — which is the question `forge env status --wait` is the
// verb for, not this one.
func TestHostedWaitStabilizingCompletesTheDeploy(t *testing.T) {
	cp := &rolloutCP{phases: []string{wireRolloutPhaseStabilizing}}
	err := HostedProvider{Client: cp, PollInterval: time.Millisecond,
		Rollout: cluster.RolloutPolicy{Timeout: 30 * time.Millisecond}}.
		Deploy(context.Background(), promotedGroup("promo-1"))
	if err != nil {
		t.Fatalf("a STABILIZING rollout is published and serving, so the deploy is done: %v", err)
	}
}

// TestHostedWaitProgressingTimesOut: the deploy does not pass a rollout that
// never takes over. The failure NAMES the replica pair, which is the H1
// evidence and the one thing that distinguishes "looks ready, is not" from a
// genuinely slow start.
func TestHostedWaitProgressingTimesOut(t *testing.T) {
	// The case problem 4 in §3.2 describes: the observation claims the new
	// digest, the workload reads READY (old pods keep readyReplicas up),
	// and NO new pod serves it. The status poll calls this ready.
	cp := &rolloutCP{
		phases: []string{phaseProgressing},
		workloads: fmt.Sprintf(`[{"deploymentId":"dep-api","name":"api","artifact":"ghcr.io/acme/api",
		 "pinnedDigest":%q,"observedDigest":%q,"phase":%q,"observedState":"DEPLOY_OBSERVED_STATE_READY",
		 "updatedReplicas":0,"desiredReplicas":3}]`, digestA, digestA, phaseProgressing),
	}
	err := HostedProvider{Client: cp, PollInterval: time.Millisecond,
		Rollout: cluster.RolloutPolicy{Timeout: 20 * time.Millisecond}}.
		Deploy(context.Background(), promotedGroup("promo-1"))
	if err == nil {
		t.Fatal("a rollout where no new pod serves the pin must NOT pass the deploy")
	}
	for _, want := range []string{"TIMED OUT", "api", "0/3 updated replicas"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the timeout must say %q, got:\n%v", want, err)
		}
	}
}

// TestHostedWaitSupersededIsTerminal: a newer promotion replaced the one this
// deploy published, so the bytes being waited on are no longer what the env
// wants. Waiting out the budget would report "this deploy failed" for a
// deploy that was simply overtaken, and send the operator to the wrong
// promotion.
func TestHostedWaitSupersededIsTerminal(t *testing.T) {
	cp := &rolloutCP{phases: []string{wireRolloutPhaseSuperseded}}
	start := time.Now()
	err := HostedProvider{Client: cp, PollInterval: 50 * time.Millisecond,
		Rollout: cluster.RolloutPolicy{Timeout: 10 * time.Second}}.
		Deploy(context.Background(), promotedGroup("promo-1"))
	if err == nil || !strings.Contains(err.Error(), "newer promotion") {
		t.Fatalf("err = %v, want a superseded failure naming the newer promotion", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("superseded must be terminal, not waited out: took %s of a 10s budget", elapsed)
	}
}

// A control plane that does not serve GetRollout, or does not hold the
// promotion, FALLS BACK to the status poll rather than failing the deploy: a
// deploy must keep working against an older control plane. The fallback is
// announced, because it answers a weaker question.
func TestHostedWaitFallsBackWhenRolloutUnavailable(t *testing.T) {
	for _, code := range []string{wireCodeUnimplemented, wireCodeNotFound} {
		t.Run(code, func(t *testing.T) {
			cp := &rolloutCP{phases: []string{wireRolloutPhaseSucceeded}, rolloutErr: code}
			err := HostedProvider{Client: cp, PollInterval: time.Millisecond,
				Rollout: cluster.RolloutPolicy{Timeout: 50 * time.Millisecond}}.
				Deploy(context.Background(), promotedGroup("promo-1"))
			// The status poll this fake serves is PENDING, so the
			// fallback times out rather than passing — which is the
			// proof it fell back to the status poll at all instead
			// of failing on the rollout error.
			if err == nil || !strings.Contains(err.Error(), "TIMED OUT") {
				t.Fatalf("err = %v, want the status-poll timeout (proving the fallback ran)", err)
			}
			// And it asked ONCE. Re-asking every poll would spend a
			// call per tick re-learning the same answer.
			if n := len(cp.rolloutBodies()); n != 1 {
				t.Errorf("GetRollout was retried %d times; the fallback must be sticky", n)
			}
		})
	}
}

// TestHostedWaitRolloutReadFailureFallsBackRatherThanTimingOut: a rollout
// read that keeps failing must NOT consume the deploy's budget. Without the
// bound, a deploy whose GetStatus was answering perfectly well the whole time
// would still time out reporting "last status read failed" — blaming the
// deploy for a defect in one RPC.
//
// This is what the pre-existing F1 test caught: its control plane does not
// serve GetRollout at all, and an unbounded retry hung it for the full
// budget.
func TestHostedWaitRolloutReadFailureFallsBackRatherThanTimingOut(t *testing.T) {
	// An error with no Connect code at all — a transport failure, or a
	// control plane that answers the procedure with something unexpected.
	cp := &rolloutCP{phases: []string{wireRolloutPhaseSucceeded}, rolloutErr: "no-such-code"}
	err := HostedProvider{Client: cp, PollInterval: time.Millisecond,
		Rollout: cluster.RolloutPolicy{Timeout: 200 * time.Millisecond}}.
		Deploy(context.Background(), promotedGroup("promo-1"))
	// The status poll this fake serves is PENDING, so the deploy times out
	// — but on the STATUS poll's reasons, which is the proof it fell back
	// rather than spending the budget on a failing rollout read.
	if err == nil || !strings.Contains(err.Error(), "TIMED OUT") {
		t.Fatalf("err = %v, want the status-poll timeout", err)
	}
	if strings.Contains(err.Error(), "last status read failed") {
		t.Errorf("the deploy must not be blamed for the rollout read's failure:\n%v", err)
	}
	if n := len(cp.rolloutBodies()); n != rolloutReadAttempts {
		t.Errorf("GetRollout was tried %d times, want exactly %d before the fallback", n, rolloutReadAttempts)
	}
}

// A QUEUED promotion (phase HELD: accepted, recorded, waiting on billing) is
// reported at once, as a HeldError carrying the hold — not waited on for the
// whole budget and reported as a timeout of a release nothing is wrong with.
// Both ways a deploy can meet it: the loop's own rollout read, and — when the
// deploy recorded a bundle the platform has not applied — the one check made
// while waiting for the apply.
func TestHostedWaitReportsAQueuedPromotionAtOnce(t *testing.T) {
	for name, recorded := range map[string]bool{
		"by the rollout read":                false,
		"while waiting for the bundle apply": true,
	} {
		t.Run(name, func(t *testing.T) {
			cp := &rolloutCP{
				phases: []string{WireRolloutPhaseHeld},
				holds: `[{"kind":"DEPLOY_HOLD_KIND_BILLING","promotionId":"promo-1",
				  "reason":"this runs compute (1 workload) and the organization has no active compute plan",
				  "fix":"Subscribe… nothing needs to be re-run.","actionUrl":"https://app.example/forge/env/prod?forgeProject=app"}]`,
			}
			provider := HostedProvider{Client: cp, PollInterval: time.Millisecond,
				Rollout: cluster.RolloutPolicy{Mode: cluster.RolloutWait, Timeout: 10 * time.Second}}
			if recorded {
				provider.RecordBundle = func(context.Context, string) (string, error) { return digestA, nil }
			}
			start := time.Now()
			err := provider.Deploy(context.Background(), promotedGroup("promo-1"))

			var held *HeldError
			if !errors.As(err, &held) {
				t.Fatalf("err = %v, want a *HeldError for a queued promotion", err)
			}
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Errorf("a queued promotion took %s to report; it must not wait out the budget", elapsed)
			}
			if len(held.Holds) != 1 || held.Holds[0].Label() != "billing" || held.Env != "prod" ||
				held.Holds[0].ActionURL == "" || held.Release != "v1" || held.PromotionID != "promo-1" {
				t.Errorf("held = %+v, want the billing hold with its link, for prod promo-1 / v1", held)
			}
			if n := len(cp.rolloutBodies()); n != 1 {
				t.Errorf("GetRollout was called %d times, want 1", n)
			}
		})
	}
}

// --rollout skip waits for nothing, rollout or not.
func TestHostedWaitSkipMakesNoRolloutCall(t *testing.T) {
	cp := &rolloutCP{phases: []string{phaseDegraded}}
	err := HostedProvider{Client: cp, Rollout: cluster.RolloutPolicy{Mode: cluster.RolloutSkip}}.
		Deploy(context.Background(), promotedGroup("promo-1"))
	if err != nil {
		t.Fatalf("--rollout skip must not fail: %v", err)
	}
	if n := len(cp.rolloutBodies()); n != 0 {
		t.Errorf("--rollout skip made %d GetRollout call(s)", n)
	}
}

// --rollout warn reports and does not fail, for both the timeout and the
// terminal superseded path.
func TestHostedWaitWarnDoesNotFail(t *testing.T) {
	for name, phases := range map[string][]string{
		"progressing": {phaseProgressing},
		"superseded":  {wireRolloutPhaseSuperseded},
	} {
		t.Run(name, func(t *testing.T) {
			cp := &rolloutCP{phases: phases}
			err := HostedProvider{Client: cp, PollInterval: time.Millisecond,
				Rollout: cluster.RolloutPolicy{Mode: cluster.RolloutWarn, Timeout: 20 * time.Millisecond}}.
				Deploy(context.Background(), promotedGroup("promo-1"))
			if err != nil {
				t.Fatalf("--rollout warn must not fail the deploy: %v", err)
			}
		})
	}
}

// rolloutPendingOf is the pure matching rule, worth its own table: a plan
// item is matched by NAME against the pinned rows and then the unpinned
// ones, and an item in NEITHER is pending. A deployment the control plane has
// not observed at all must never read as ready.
func TestRolloutPendingOf(t *testing.T) {
	plan := []hostedPlanItem{{Name: "api"}, {Name: "orders"}, {Name: "ghost"}}
	rollout := wireRollout{
		Workloads: []wireWorkloadRollout{{Name: "api", Phase: wireRolloutPhaseSucceeded}},
		Unpinned:  []wireWorkloadRollout{{Name: "orders", ObservedState: wireObservedReady}},
	}
	pending, reasons := rolloutPendingOf(plan, rollout)
	if len(pending) != 1 || pending[0] != "ghost" {
		t.Fatalf("pending = %v, want only the unobserved ghost", pending)
	}
	if !strings.Contains(reasons["ghost"], "not in the rollout yet") {
		t.Errorf("ghost's reason = %q", reasons["ghost"])
	}

	// A degraded UNPINNED row does hold up a DEPLOY — unlike a release
	// gate, where a database must not fail a release. The deploy just
	// published this database and owes the operator a word on it.
	rollout.Unpinned = []wireWorkloadRollout{{Name: "orders",
		ObservedState: "DEPLOY_OBSERVED_STATE_DEGRADED", Phase: phaseDegraded, LastError: "disk pressure"}}
	pending, reasons = rolloutPendingOf(plan, rollout)
	if len(pending) != 2 {
		t.Fatalf("pending = %v, want orders and ghost", pending)
	}
	if !strings.Contains(reasons["orders"], "disk pressure") {
		t.Errorf("orders' reason must carry its error, got %q", reasons["orders"])
	}
}

// rolloutPhaseServing is the deploy threshold, pinned as a table so a change
// to it is a deliberate edit rather than a side effect.
func TestRolloutPhaseServing(t *testing.T) {
	serving := map[string]bool{
		wireRolloutPhaseSucceeded:      true,
		wireRolloutPhaseStabilizing:    true,
		"DEPLOY_ROLLOUT_PHASE_PENDING": false,
		phaseProgressing:               false,
		phaseDegraded:                  false,
		wireRolloutPhaseSuperseded:     false,
		"DEPLOY_ROLLOUT_PHASE_UNKNOWN": false,
		"":                             false,
		// A phase forge does not recognise must never read as serving:
		// that is the dangerous default.
		"DEPLOY_ROLLOUT_PHASE_FUTURE": false,
	}
	for phase, want := range serving {
		if got := rolloutPhaseServing(phase); got != want {
			t.Errorf("rolloutPhaseServing(%q) = %v, want %v", phase, got, want)
		}
	}
}
