package cluster

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// The rollout WAIT, separated from the apply so that a deploy spanning several
// clusters can apply all of them before it waits on any.
//
// WHY THE WAIT IS SEPARABLE. One env may span several clusters, and a workload
// on one can be unable to become ready until a workload on another is running:
// control-plane's workspace-proxy-bridge cannot pass readiness until
// workspace-proxy, on the daemon cluster, answers it. Applying and awaiting
// each cluster in turn deadlocks that env on every fresh deploy — the first
// cluster's wait expires on a dependency the second cluster has not been sent
// yet, the deploy fails, and the second cluster is never applied at all.
// Kubernetes converges concurrently (RolloutPolicy.Order is documented as a
// wait ordering, not an apply ordering, for exactly this reason), so the
// deploy applies every cluster with ApplyNoWait and then hands every
// PendingRollout to ONE WaitRollouts.
//
// A single-cluster Apply is ApplyNoWait + WaitRollouts of that one pending
// rollout, and its output and error are exactly what they always were.

// PendingRollout is one apply's outstanding rollout: everything the wait needs
// to know about what that apply sent, and where. ApplyNoWait produces it and
// WaitRollouts consumes it; there is nothing to construct by hand.
type PendingRollout struct {
	opts   ApplyOpts
	policy RolloutPolicy
	// jobs is the post-rollout one-shot Job wait set: every Job the apply
	// sent, minus the pre-rollout Jobs the gate already saw complete.
	jobs []string
	// skipDeployments are Deployments the hub rolls, not this apply: they are
	// not awaited (delivery_unit_filter.go).
	skipDeployments []string
	// label prefixes this rollout's failure in WaitRollouts' verdict — the
	// caller's own name for the apply. Empty leaves the error unprefixed,
	// which is what Apply returns.
	label string
}

// WithLabel names the pending rollout in WaitRollouts' error, e.g.
// `k8s-cluster deploy (ns=…, cluster=…)`, so a verdict over several clusters
// says which cluster each failure belongs to. Nil-safe: ApplyNoWait returns
// nil when there is nothing to wait for.
func (p *PendingRollout) WithLabel(label string) *PendingRollout {
	if p != nil {
		p.label = label
	}
	return p
}

// where names this rollout's cluster and namespace in a multi-cluster wait's
// progress lines, where a bare Deployment name would be ambiguous.
func (p *PendingRollout) where() string {
	return p.opts.Context + "/" + p.opts.Namespace
}

// RolloutFailedError is WaitRollouts' verdict when more than one of the
// rollouts it awaited failed. Each entry is that rollout's own error, prefixed
// with its label, so the message names every cluster and every resource that
// did not become ready — not only the first one the wait reached.
type RolloutFailedError struct {
	Failures []error
}

func (e *RolloutFailedError) Error() string {
	parts := make([]string, len(e.Failures))
	for i, f := range e.Failures {
		parts[i] = f.Error()
	}
	return fmt.Sprintf("%d rollouts did not converge:\n  - %s", len(e.Failures), strings.Join(parts, "\n  - "))
}

// Unwrap exposes each rollout's error to errors.Is / errors.As.
func (e *RolloutFailedError) Unwrap() []error { return e.Failures }

// rolloutWait is one pending rollout's progress through a WaitRollouts call.
type rolloutWait struct {
	p *PendingRollout
	// failed names every resource that did not become ready, under a
	// failing policy. Warn-mode failures are reported and not recorded.
	failed []string
	// err is a failure to wait at all (the Deployment list failed); it ends
	// this rollout's wait and is its verdict.
	err error
}

// note reports one resource's failure in the policy's voice: a warning when
// the caller opted into warn-only, a recorded failure otherwise.
func (w *rolloutWait) note(kind, name string, err error) {
	if w.p.policy.Mode == RolloutWarn {
		fmt.Printf("  Warning: %s for %s: %v\n", kind, name, err)
		return
	}
	fmt.Printf("  FAILED: %s for %s: %v\n", kind, name, err)
	w.failed = append(w.failed, name)
}

// stopsOnFailure reports whether a failure under this rollout's policy ends
// the whole wait: --rollout-fail-fast, in the failing mode.
func (w *rolloutWait) stopsOnFailure() bool {
	return w.p.policy.FailFast && w.p.policy.Mode == RolloutWait
}

// verdict is this rollout's own error, labelled for the aggregate.
func (w *rolloutWait) verdict() error {
	err := w.err
	if err == nil {
		err = rolloutError(w.failed)
	}
	if err != nil && w.p.label != "" {
		err = fmt.Errorf("%s: %w", w.p.label, err)
	}
	return err
}

// deploymentWait is one Deployment to await, on one pending rollout's cluster.
type deploymentWait struct {
	w    *rolloutWait
	name string
}

// WaitRollouts waits on every pending rollout and grades them as ONE result.
//
// Nothing is applied here: by the time this runs every cluster in the deploy
// has its manifests and is converging concurrently, so a dependency between
// clusters resolves while forge waits rather than expiring because it was
// never sent.
//
// Every cluster's Deployments are awaited as one sequence ordered by
// RolloutPolicy.Order, so `--rollout-order` means the same thing across
// clusters as within one; the one-shot Jobs follow. Each resource is graded by
// its own apply's policy, exactly as Apply grades it: RolloutWait records
// every failure on every cluster and fails, RolloutWarn reports and succeeds,
// FailFast stops at the first failure anywhere. There is no rollback either
// way — what was applied stays applied, and recovery is to roll forward.
//
// nil entries (a dry run, a skip-mode apply, a platform-only apply) have
// nothing to wait for and are ignored. One pending rollout returns exactly the
// error Apply always has; several return a *RolloutFailedError when more than
// one of them failed.
func WaitRollouts(ctx context.Context, pending ...*PendingRollout) error {
	var waits []*rolloutWait
	for _, p := range pending {
		if p != nil {
			waits = append(waits, &rolloutWait{p: p})
		}
	}
	if len(waits) == 0 {
		return nil
	}
	multi := len(waits) > 1
	// subject names a resource in a progress line. With one cluster the
	// bare name is unambiguous and is what Apply has always printed.
	subject := func(w *rolloutWait, name string) string {
		if !multi {
			return name
		}
		return name + " (" + w.p.where() + ")"
	}

	quiet := true
	for _, w := range waits {
		quiet = quiet && w.p.opts.Quiet
	}
	if !quiet {
		if multi {
			where := make([]string, len(waits))
			for i, w := range waits {
				where[i] = w.p.where()
			}
			fmt.Printf("Waiting for rollouts on %s...\n", strings.Join(where, ", "))
		} else {
			fmt.Println("Waiting for rollouts...")
		}
	}

	var queue []deploymentWait
	for _, w := range waits {
		deployments, lerr := ListManagedDeployments(ctx, w.p.opts.Context, w.p.opts.Namespace)
		if lerr != nil {
			if multi {
				lerr = fmt.Errorf("%s: %w", w.p.where(), lerr)
			}
			w.err = reportRolloutListFailure(lerr, w.p.opts.Quiet, w.p.policy.Mode)
			// Warn mode tolerates the list failure, but there is still
			// nothing to wait on — the cluster's Jobs included, as before.
			w.p.jobs = nil
			if w.err != nil && w.stopsOnFailure() {
				return aggregateRolloutVerdicts(waits)
			}
			continue
		}
		hub := unitSet(w.p.skipDeployments)
		for _, dep := range deployments {
			if hub[dep] {
				continue
			}
			queue = append(queue, deploymentWait{w: w, name: dep})
		}
	}
	orderWaitQueue(queue)

	for _, item := range queue {
		w, opts := item.w, item.w.p.opts
		state, err := WaitRolloutObserved(ctx, opts.Context, item.name, opts.Namespace, w.p.policy.Timeout)
		observeRollout(opts, "Deployment", item.name, state, err)
		if err == nil {
			fmt.Printf("  %s: ready\n", subject(w, item.name))
			continue
		}
		w.note("rollout", subject(w, item.name), err)
		if w.stopsOnFailure() {
			return aggregateRolloutVerdicts(waits)
		}
	}

	for _, w := range waits {
		opts := w.p.opts
		for _, name := range w.p.jobs {
			fmt.Printf("Waiting for one-shot Job %q to complete...\n", subject(w, name))
			state, err := WaitJobCompleteObserved(ctx, opts.Context, name, opts.Namespace, w.p.policy.Timeout)
			observeRollout(opts, "Job", name, state, err)
			if err != nil {
				// A failed one-shot Job is if anything MORE serious than a
				// failed Deployment: it is the migration that did not run,
				// and every workload above it is now talking to a schema
				// that never moved.
				w.note("job", subject(w, name), err)
				if w.stopsOnFailure() {
					return aggregateRolloutVerdicts(waits)
				}
				continue
			}
			fmt.Printf("  %s: complete\n", subject(w, name))
		}
	}
	return aggregateRolloutVerdicts(waits)
}

// orderWaitQueue sorts the Deployment waits of every cluster into ONE
// sequence: the names RolloutPolicy.Order lists first, in Order's sequence,
// and everything else behind them in cluster then list order. So
// `--rollout-order` means the same thing across clusters as within one — the
// migration or API server it names surfaces its failure first, wherever it
// runs.
func orderWaitQueue(queue []deploymentWait) {
	sort.SliceStable(queue, func(i, j int) bool {
		return queue[i].w.p.policy.waitRank(queue[i].name) < queue[j].w.p.policy.waitRank(queue[j].name)
	})
}

// aggregateRolloutVerdicts folds every rollout's verdict into WaitRollouts'
// one return: nil, the single failing rollout's own error, or a
// *RolloutFailedError naming each.
func aggregateRolloutVerdicts(waits []*rolloutWait) error {
	var failures []error
	for _, w := range waits {
		if err := w.verdict(); err != nil {
			failures = append(failures, err)
		}
	}
	switch len(failures) {
	case 0:
		return nil
	case 1:
		return failures[0]
	default:
		return &RolloutFailedError{Failures: failures}
	}
}
