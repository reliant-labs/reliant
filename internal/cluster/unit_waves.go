package cluster

// Delivery units, honoured by direct apply.
//
// WHY. An env declares the order its delivery units roll out in
// (forge.DeliveryUnit, `after`). A Flux-applied bundle has always expressed it
// as trees chained by dependsOn (pkg/release/bundle_trees.go); direct apply
// used to ignore it and send every workload in one pass. That let a consumer
// roll beside the very thing it depends on: on control-plane's v1.14.0-rc.1
// rollout a new daemon-gateway pod subscribed to a NATS subject before the
// nats-cluster StatefulSet, rolling in the same pass, had loaded the
// permission that granted it, and the broker refused it. `nats-broker` before
// its consumers is a declaration; direct apply now keeps it.
//
// THE SHAPE. Only the WORKLOADS are ordered (release.RolloutWorkloadKind).
// Everything a release already sends ahead of its workloads still goes first,
// for the whole env and every unit at once, exactly as before: CRDs and
// Namespaces, the config pass, the secret-store stages, the support objects,
// and the pre-rollout Jobs (a migration still completes before any new code,
// whichever unit that code is in). Then, wave by wave (release.UnitWaves),
// each wave's workloads are applied on every cluster and awaited on every
// cluster before the next wave is applied — "after" means the earlier unit's
// last workload is Ready everywhere it runs, as it does for the bundle's
// trees. After the last wave the post-rollout Jobs are applied, the
// apply-time prunes run, and the ordinary rollout wait (and its owned-object
// prune) finishes as it always has.
//
// A WAVE THAT DOES NOT FULLY APPLY OR ROLL OUT STOPS THE DEPLOY, in every
// rollout mode, like the pre-rollout gate: the later units are never applied, and NOTHING is
// pruned. `--rollout=warn` / `--no-wait` say how to grade the release's own
// convergence; they never meant "roll a consumer before what it depends on".
//
// BACKWARD COMPATIBLE BY CONSTRUCTION. Staging engages only when the caller
// passed the env's declared units (ApplyOpts.DeliveryUnits) AND the env's
// workloads span at least two waves. An env that declares no unit, or whose
// workloads all sit in one, is applied exactly as before, pass for pass.
//
// The decision is made on the env-wide stream (after --target and the hub's
// units, before the per-cluster scope), so every cluster of one deploy makes
// the same decision: a cluster whose own workloads happen to sit in one unit
// must still wait for another cluster's earlier unit.
//
// Units the hub converges (ApplyOpts.SkipUnits) are not applied here and are
// not awaited: their wave is empty for this applier.

import (
	"context"
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/release"
)

// unitWave is one wave of one cluster's staged apply.
type unitWave struct {
	// units are the delivery units of the wave, sorted.
	units []string
	// stream is this cluster's workloads in those units ("" when it has none).
	stream string
	// workloads are the rollouts the wave waits on: every Deployment,
	// StatefulSet and DaemonSet in stream.
	workloads []workloadRef
}

// unitStages is the staged remainder of one cluster's apply: the workload
// waves, then the post-rollout Jobs.
type unitStages struct {
	waves []unitWave
	// final is applied after the last wave: post-rollout Jobs and anything
	// that does not parse (kubectl is the right thing to report on it, and
	// the held-back pass has always been the last).
	final string
}

// planUnitWaves is the env-wide decision: the waves to stage the workloads
// in, or nil when direct apply must behave exactly as it always has (no unit
// declared, or every workload in one wave). envStream is the whole env's
// stream after --target and the hub's units are taken out, before the
// per-cluster scope.
func planUnitWaves(declared []release.DeliveryUnit, envStream string) ([][]string, error) {
	if len(declared) == 0 {
		return nil, nil
	}
	var present []string
	seen := map[string]bool{}
	workloadUnits := map[string]bool{}
	for _, doc := range splitDocs(envStream) {
		m, ok := parseDoc(doc)
		if !ok {
			continue
		}
		u := docUnit(m)
		if !seen[u] {
			seen[u] = true
			present = append(present, u)
		}
		if release.RolloutWorkloadKind(m.Kind) {
			workloadUnits[u] = true
		}
	}
	waves, err := release.UnitWaves(declared, present)
	if err != nil {
		return nil, fmt.Errorf("delivery units: %w; nothing was applied", err)
	}
	carrying := 0
	for _, wave := range waves {
		for _, u := range wave {
			if workloadUnits[u] {
				carrying++
				break
			}
		}
	}
	if carrying < 2 {
		return nil, nil
	}
	return waves, nil
}

// splitUnitStages cuts one cluster's held-back stream into what is applied
// now (head: support objects not yet sent), the waves, and the final pass.
// Every document lands in exactly one place, in stream order.
func splitUnitStages(rest string, waves [][]string, namespace string) (head string, st *unitStages) {
	waveOf := map[string]int{}
	for i, wave := range waves {
		for _, u := range wave {
			waveOf[u] = i
		}
	}
	var headDocs, finalDocs []string
	waveDocs := make([][]string, len(waves))
	for _, doc := range splitDocs(rest) {
		m, ok := parseDoc(doc)
		switch {
		case !ok, m.Kind == "Job":
			finalDocs = append(finalDocs, doc)
		case release.RolloutWorkloadKind(m.Kind):
			i, known := waveOf[docUnit(m)]
			if !known {
				// planUnitWaves read every unit of the env stream, so this
				// cannot happen; the safe place for a stranger is last.
				i = len(waves) - 1
			}
			waveDocs[i] = append(waveDocs[i], doc)
		default:
			headDocs = append(headDocs, doc)
		}
	}
	st = &unitStages{final: strings.Join(finalDocs, docDelimiter)}
	for i, wave := range waves {
		stream := strings.Join(waveDocs[i], docDelimiter)
		st.waves = append(st.waves, unitWave{units: wave, stream: stream, workloads: waveWorkloads(stream, namespace)})
	}
	return strings.Join(headDocs, docDelimiter), st
}

// waveWorkloads names the rollouts a wave waits on: its Deployments,
// StatefulSets and DaemonSets, a namespace-less one in namespace.
func waveWorkloads(stream, namespace string) []workloadRef {
	seen := map[workloadRef]bool{}
	var out []workloadRef
	for _, doc := range splitDocs(stream) {
		m, ok := parseDoc(doc)
		if !ok || m.Metadata.Name == "" {
			continue
		}
		switch m.Kind {
		case "Deployment", "StatefulSet", "DaemonSet":
		default:
			continue
		}
		ns := m.Metadata.Namespace
		if ns == "" {
			ns = namespace
		}
		ref := workloadRef{Kind: m.Kind, Namespace: ns, Name: m.Metadata.Name}
		if !seen[ref] {
			seen[ref] = true
			out = append(out, ref)
		}
	}
	return out
}

// UnitRolloutError is a deploy stopped between delivery units: a wave's
// workloads did not roll out, so no later unit was applied and nothing was
// pruned. A type, so a caller (and a test) can tell it from a release that
// rolled out and then failed its final wait.
type UnitRolloutError struct {
	// Units are the units of the wave that did not roll out.
	Units []string
	// Held are the units that were never applied, in wave order.
	Held []string
	// Failures are the workloads that did not become ready, each naming its
	// cluster and its error.
	Failures []error
}

func (e *UnitRolloutError) Error() string {
	parts := make([]string, len(e.Failures))
	for i, f := range e.Failures {
		parts[i] = f.Error()
	}
	held := "none"
	if len(e.Held) > 0 {
		held = strings.Join(e.Held, ", ")
	}
	return fmt.Sprintf("delivery unit(s) %s did not roll out:\n  - %s\nThe units after it were NOT applied (%s), and nothing was pruned; their previous release is still serving. Fix the cause and deploy again.",
		strings.Join(e.Units, ", "), strings.Join(parts, "\n  - "), held)
}

// Unwrap exposes each workload's error to errors.Is / errors.As.
func (e *UnitRolloutError) Unwrap() []error { return e.Failures }

// convergeUnitWaves applies and awaits every staged pending rollout's waves,
// wave by wave across all of them, then their final passes and their
// deferred apply-time prunes. A pending rollout with no stages is untouched,
// so a deploy that staged nothing does nothing here.
func convergeUnitWaves(ctx context.Context, pending []*PendingRollout) error {
	var staged []*PendingRollout
	waves := 0
	for _, p := range pending {
		if p != nil && p.units != nil {
			staged = append(staged, p)
			waves = max(waves, len(p.units.waves))
		}
	}
	if len(staged) == 0 {
		return nil
	}
	multi := len(staged) > 1
	where := func(p *PendingRollout, what string) string {
		if !multi {
			return what
		}
		return what + " (" + p.where() + ")"
	}
	// THE BARRIER IS GLOBAL. Each iteration first applies wave i on every
	// cluster that has workloads in it, then waits for ALL of wave i's
	// workloads on every cluster, and only then moves to wave i+1. A cluster
	// with nothing in wave i sends nothing in it, but it cannot get ahead:
	// its wave i+1 is applied by the next iteration, after the wait.
	for i := 0; i < waves; i++ {
		var units []string
		for _, p := range staged {
			if i < len(p.units.waves) {
				units = p.units.waves[i].units
				break
			}
		}
		held := heldUnits(staged, i)
		for _, p := range staged {
			if i >= len(p.units.waves) || strings.TrimSpace(p.units.waves[i].stream) == "" {
				continue
			}
			if !p.opts.Quiet {
				fmt.Printf("Applying delivery unit(s) %s%s...\n", strings.Join(units, ", "), where(p, ""))
			}
			// Not classifyApplyResult: warn mode tolerates an incomplete
			// apply of the release, but a wave that did not fully land is
			// not a unit that rolled out, and the order holds in every mode.
			if err := KubectlApply(ctx, p.opts.Context, p.units.waves[i].stream); err != nil {
				return fmt.Errorf("kubectl apply failed (delivery unit(s) %s on %s; the units after it (%s) were not applied and nothing was pruned): %w",
					strings.Join(units, ", "), p.where(), strings.Join(held, ", "), err)
			}
		}
		var failures []error
		for _, p := range staged {
			if i >= len(p.units.waves) {
				continue
			}
			for _, ref := range p.units.waves[i].workloads {
				var (
					state RolloutState
					err   error
				)
				if ref.Kind == "Deployment" {
					state, err = WaitRolloutObserved(ctx, p.opts.Context, ref.Name, ref.Namespace, p.policy.Timeout)
				} else {
					state, err = WaitWorkloadObserved(ctx, p.opts.Context, ref.Kind, ref.Name, ref.Namespace, p.policy.Timeout)
				}
				observeRollout(p.opts, ref.Kind, ref.Name, state, err)
				label := strings.ToLower(ref.Kind) + "/" + ref.Name
				if err != nil {
					fmt.Printf("  FAILED: rollout for %s: %v\n", where(p, label), err)
					failures = append(failures, fmt.Errorf("%s on %s: %w", label, p.where(), err))
					continue
				}
				fmt.Printf("  %s: ready\n", where(p, label))
				if p.waveReady == nil {
					p.waveReady = map[workloadRef]bool{}
				}
				p.waveReady[ref] = true
			}
		}
		if len(failures) > 0 {
			return &UnitRolloutError{Units: units, Held: held, Failures: failures}
		}
	}
	for _, p := range staged {
		if strings.TrimSpace(p.units.final) != "" {
			if err := p.policy.classifyApplyResult(KubectlApply(ctx, p.opts.Context, p.units.final)); err != nil {
				return fmt.Errorf("kubectl apply failed (post-rollout Jobs on %s, after every delivery unit rolled out; nothing was pruned): %w", p.where(), err)
			}
		}
	}
	for _, p := range staged {
		if p.deferredPrune != nil {
			p.deferredPrune(ctx)
		}
	}
	return nil
}

// heldUnits are the units of every wave after wave i: what a failure in wave
// i leaves unapplied. Every staged apply of one deploy has the same waves
// (planUnitWaves reads the env-wide stream), so the first one speaks for all.
func heldUnits(staged []*PendingRollout, i int) []string {
	var out []string
	waves := staged[0].units.waves
	for _, w := range waves[min(i+1, len(waves)):] {
		out = append(out, w.units...)
	}
	return out
}

// unitStagedInput is what applyUnitStaged needs from the apply that held its
// workloads back.
type unitStagedInput struct {
	order [][]string
	// rest is the stream still to send: the held-back workloads and the
	// post-rollout Jobs, plus (ungated) the support objects.
	rest string
	// manifests and desired are the apply's sent and desired streams, for the
	// wait set and the deferred prunes; crdOwner is the CRD prune's owner.
	manifests, desired, crdOwner string
	// phases and stages are the apply's pre-rollout gate and secret stages:
	// a Job either already awaited is not awaited again.
	phases rolloutPhases
	stages secretStages
}

// applyUnitStaged sends what goes ahead of every unit now and returns the
// pending rollout that carries the waves (WaitRollouts applies them).
func applyUnitStaged(ctx context.Context, opts ApplyOpts, policy RolloutPolicy, in unitStagedInput) (*PendingRollout, error) {
	head, staged := splitUnitStages(in.rest, in.order, opts.Namespace)
	if strings.TrimSpace(head) != "" {
		if err := policy.classifyApplyResult(KubectlApply(ctx, opts.Context, head)); err != nil {
			return nil, fmt.Errorf("kubectl apply failed (support objects; no workload was applied): %w", err)
		}
	}
	if !opts.Quiet {
		order := make([]string, 0, len(staged.waves))
		for _, w := range staged.waves {
			order = append(order, strings.Join(w.units, "+"))
		}
		fmt.Printf("Delivery units: workloads roll out in the declared order %s, each unit awaited before the next\n", strings.Join(order, " → "))
	}
	var jobs []string
	stageJobs := in.stages.awaitedJobs()
	for _, name := range oneShotWaitSet(in.manifests) {
		if !in.phases.awaited(name) && !stageJobs[name] {
			jobs = append(jobs, name)
		}
	}
	pending := &PendingRollout{
		opts: opts, policy: policy, jobs: jobs,
		skipDeployments: DeploymentsInUnits(in.desired, opts.SkipUnits),
		workloads:       streamWorkloads(in.manifests, opts.Namespace),
		units:           staged,
		skipFinalWait:   policy.Mode == RolloutSkip,
		deferredPrune: func(ctx context.Context) {
			pruneAfterApply(ctx, opts, in.desired, in.manifests, in.crdOwner)
		},
	}
	pending.prune = ownedPruneFor(opts, in.desired)
	return pending, nil
}
