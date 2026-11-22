package cli

// The cluster-rollout gate: forge's report, on the promotion, that the cluster
// half of a MIXED env rolled out.
//
// THE ORDERING IT CARRIES. A mixed env has two actuators. forge applies the
// cluster parts from this machine; the control plane converges the hosted part
// (a static site, a hosted workload) from the promotion row. Nothing else
// orders them, and the hosted part is declared to come AFTER the cluster parts
// it calls (release_dependencies, or every server workload when that is
// unset). So the control plane holds the hosted part's activation until it
// reads this gate, passed, for the promotion and bundle it is about to
// activate. Without the hold the SPA of release N+1 serves against the API of
// release N for the length of the rollout — and for good when the cluster
// apply fails, or when a console promote has no forge process applying the
// cluster half at all.
//
// THE APPLIER IS THE ONE PARTY THAT KNOWS. forge's rollout wait already
// judges every workload it sent (cluster.AwaitedKinds) and fails the deploy
// if one does not become ready. The gate is that verdict, filed where the
// control plane can read it, so the control plane needs no identity on the
// clusters forge applies — which, for a customer's env, it never has.
//
// WHEN IT IS RECORDED: the moment the local stage finishes, BEFORE the hosted
// stage. The hosted stage publishes the hosted part and waits for the control
// plane to converge it, and the control plane will not converge it until it
// reads this gate. Recording it where the apply gate is recorded — after the
// whole deploy returns — would hold that wait until it timed out.
//
// WHAT EACH STATUS CLAIMS:
//
//	passed   the local stage applied the whole env and the rollout wait saw
//	         every awaited workload ready (rollout mode wait).
//	failed   the local stage, or the deploy before it, failed.
//	skipped  the local stage applied but did not prove readiness: --no-wait
//	         (rollout mode skip), or rollout mode warn, which reports a
//	         workload that never became ready without failing. NEVER passed:
//	         an apply that waited for nothing has not seen anything roll out.
//
// The gate's details name the bundle the apply shipped (bundle_digest) only
// for a WHOLE-env apply, by the same rule as the apply gate
// (appliedBundleDigest). A scoped apply (--target, --frontends-only) shipped
// part of the bundle, so its gate names none, and a control plane that binds
// activation to the digest keeps holding until a whole-env deploy reports.

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/pkg/release"
)

// clusterRolloutGateName is the recorded gate the local stage's rollout
// verdict is filed under on a mixed env's promotion: the contract a control
// plane reads it by (release.ClusterRolloutGateName).
const clusterRolloutGateName = release.ClusterRolloutGateName

// The cluster-rollout gate's details keys, beside applyGateBundleDigest.
const (
	clusterRolloutWaited = "waited"
	clusterRolloutMode   = "rollout_mode"
	clusterRolloutKinds  = "kinds"
)

// clusterRolloutGate is the evidence one local stage leaves on the promotion.
// policy is the rollout policy the stage applied under; bundleDigest is "" for
// a scoped apply.
func clusterRolloutGate(started, finished time.Time, stageErr error, policy cluster.RolloutPolicy, bundleDigest string) release.Gate {
	mode := policy.Normalize().Mode
	startedUTC, finishedUTC := started.UTC(), finished.UTC()
	gate := release.Gate{
		Name: clusterRolloutGateName, Status: release.GateStatusPassed,
		Summary:   "every awaited cluster workload rolled out",
		StartedAt: &startedUTC, FinishedAt: &finishedUTC,
	}
	switch {
	case stageErr != nil:
		gate.Status = release.GateStatusFailed
		gate.Summary = oneLine(stageErr.Error(), 300)
	case mode == cluster.RolloutSkip:
		gate.Status = release.GateStatusSkipped
		gate.Summary = "applied without waiting for the rollout (--no-wait)"
	case mode != cluster.RolloutWait:
		gate.Status = release.GateStatusSkipped
		gate.Summary = fmt.Sprintf("applied under rollout mode %q, which does not fail on a workload that never became ready", mode)
	}
	details := map[string]any{
		clusterRolloutWaited: mode != cluster.RolloutSkip,
		clusterRolloutMode:   string(mode),
		clusterRolloutKinds:  append([]string{}, cluster.AwaitedKinds...),
	}
	if bundleDigest != "" {
		details[applyGateBundleDigest] = bundleDigest
	}
	gate.Details = details
	return gate
}

// clusterRolloutRecorder files one apply's cluster-rollout gate, exactly once:
// from the local stage's own verdict when the deploy reaches it (observe), or
// as failed when the deploy failed before it did (finish). Nil when the env is
// not one a control plane holds on the gate, so every method is nil-safe.
type clusterRolloutRecorder struct {
	ctx     context.Context
	env     string
	plan    promotePlan
	o       promoteFollowOptions
	started time.Time
	// record files the gate. A field so a test can capture what would be
	// recorded without a control plane; production is recordClusterRollout.
	record func(ctx context.Context, env, promotionID string, gate release.Gate) error

	mu    sync.Mutex
	filed bool
}

// newClusterRolloutRecorder returns the recorder for a deploy that applies a
// MIXED env's cluster half from this machine — a hosted ledger with a hosted
// tier the control plane converges after it — or nil for every other shape:
// nothing reads the gate there. A dry run records nothing.
func newClusterRolloutRecorder(ctx context.Context, env string, plan promotePlan, ledger envLedger, started time.Time, o promoteFollowOptions) *clusterRolloutRecorder {
	if !ledger.Hosted || !ledger.Mixed || len(ledger.HostedTiers) == 0 || ledger.flux() ||
		plan.Recorded == nil || o.clientDeploy.dryRun {
		return nil
	}
	return &clusterRolloutRecorder{
		ctx: ctx, env: env, plan: plan, o: o, started: started,
		record: recordClusterRollout,
	}
}

// hook is the deployOptions.onLocalStage callback, or nil for a nil recorder.
func (r *clusterRolloutRecorder) hook() func(cluster.RolloutPolicy, error) {
	if r == nil {
		return nil
	}
	return r.observe
}

// observe files the local stage's verdict under the policy it ran with.
func (r *clusterRolloutRecorder) observe(policy cluster.RolloutPolicy, stageErr error) {
	r.file(policy, stageErr)
}

// finish runs after the whole apply returned. A deploy that failed before its
// local stage ran (a render, a preflight, a cluster guard) never reached
// observe, and its gate says so; one that succeeded without reaching it shipped
// no cluster half to report on, and records nothing. A publish still queued or
// applying is not a failed apply (recordApplyOutcome's rule).
func (r *clusterRolloutRecorder) finish(applyErr error) {
	if r == nil || applyErr == nil || queuedOf(applyErr) != nil || applyingOf(applyErr) != nil {
		return
	}
	r.file(r.o.clientDeploy.rollout, applyErr)
}

func (r *clusterRolloutRecorder) file(policy cluster.RolloutPolicy, stageErr error) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.filed {
		return
	}
	r.filed = true
	gate := clusterRolloutGate(r.started, time.Now(), stageErr, policy,
		appliedBundleDigest(r.env, r.plan.Recorded.Release, r.o.clientDeploy))
	// One row per apply, under the same attempt id the apply gate uses: the
	// control plane dedupes a gate on (promotion, name, run id), so a shared
	// id would hand a re-apply the FIRST apply's verdict (applyGateRunID).
	gate.RunID = applyGateRunID(r.ctx)
	if err := r.record(r.ctx, r.env, r.plan.Recorded.ID, gate); err != nil {
		// Best-effort, like the apply gate: the deploy's outcome is the
		// apply's. An unrecorded pass leaves the hosted part held, which the
		// hosted wait then reports; it never activates it early.
		r.o.notice("[deploy] Note: the %s gate (%s) could not be recorded on promotion %s: %v\n"+
			"  The control plane holds %s's hosted part until it reads this gate passed for this bundle; "+
			"re-run this deploy to record it.\n",
			clusterRolloutGateName, gate.Status, r.plan.Recorded.ID, err, r.env)
		return
	}
	r.o.notice("[deploy] Recorded the %s gate (%s) on promotion %s\n", clusterRolloutGateName, gate.Status, r.plan.Recorded.ID)
}

// recordClusterRollout files gate on promotionID through env's gate store.
func recordClusterRollout(ctx context.Context, env, promotionID string, gate release.Gate) error {
	backend, err := resolveGateBackend(ctx, env)
	if err != nil {
		return err
	}
	if backend.store == nil {
		return fmt.Errorf("no gate store for %s", env)
	}
	_, _, err = backend.store.recordGate(ctx, promotionID, gate)
	return err
}
