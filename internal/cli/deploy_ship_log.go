package cli

// What a deploy has CHANGED on a live target so far, so that a deploy which
// fails part-way reports what it shipped instead of claiming it shipped
// nothing.
//
// A deploy is a sequence of side effects against independent targets: the
// cluster apply, the control-plane publish, each frontend host. A failure in a
// later one does not undo an earlier one — there is no rollback, recovery is
// roll forward — so "the deploy failed" and "nothing shipped" are different
// facts. On 2026-10-09 a prod deploy applied sixteen cluster workloads and
// shipped its Firebase frontend, failed afterwards, and printed "nothing
// shipped". The run before it had done the same with the stages reversed in
// outcome: the cluster apply landed, the Firebase step failed for want of the
// `firebase` binary, and the ledger recorded the whole apply as FAILED while
// its images were serving traffic.
//
// The log is threaded through deployOptions the way deployReport is, and every
// method is nil-safe, so a caller that does not ask (a dry run, `forge env up`)
// runs the identical code path and records nothing.

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/internal/deploytarget"
)

// shipLog is the deploy's log when it is about to change a live target: nil
// for a dry run and for the preflight-only pass, which change nothing.
func (o deployOptions) shipLog() *shipLog {
	if o.dryRun || o.preflightOnly {
		return nil
	}
	return o.shipped
}

// applyStageLabels names what the local apply stage writes: each deploy group
// it dispatches, or — for an env whose cluster objects are applied as one
// stream with no workload group — the cluster and namespace. Empty when the
// apply writes nothing (a frontend-only env).
func applyStageLabels(groups []deploytarget.ServiceGroup, cluster bool, kubeContext, namespace string) []string {
	if len(groups) == 0 {
		if !cluster {
			return nil
		}
		return []string{fmt.Sprintf("[k8s-cluster] cluster=%s ns=%s", kubeContext, namespace)}
	}
	labels := make([]string, 0, len(groups))
	for _, g := range groups {
		labels = append(labels, deploytarget.FormatGroupSummary(g))
	}
	return labels
}

// hostedStageLabels names what a control-plane publish sends. Composed here
// rather than with FormatGroupSummary because a hosted group learns its
// endpoint inside the publish, after this label is needed.
func hostedStageLabels(groups []deploytarget.ServiceGroup) []string {
	labels := make([]string, 0, len(groups))
	for _, g := range groups {
		var names []string
		for _, s := range g.Services {
			names = append(names, s.Name)
		}
		for _, f := range g.Frontends {
			names = append(names, f.Name)
		}
		for _, f := range g.StaticSites {
			names = append(names, f.Name)
		}
		labels = append(labels, fmt.Sprintf("[%s] published to the control plane: %s", g.ProviderID, strings.Join(names, ", ")))
	}
	return labels
}

// shipLog accumulates the side-effecting stages of one deploy.
type shipLog struct {
	mu sync.Mutex
	// shipped are stages that completed: their change is live.
	shipped []string
	// partial are stages that STARTED and failed. Whether any of the
	// stage's change landed cannot be known from here (a kubectl apply that
	// fails on its tenth object applied the first nine), so it is reported
	// as possibly live — never as nothing.
	partial []string
}

// run performs one side-effecting stage and records its outcome under each of
// labels: shipped when fn succeeds, partial when it fails. With no labels, or
// on a nil log, it is just fn().
func (l *shipLog) run(labels []string, fn func() error) error {
	err := fn()
	if l == nil || len(labels) == 0 {
		return err
	}
	// A refusal decided before anything was sent changed nothing: recording
	// the stage as partly applied would tell the operator it may be live.
	if errors.Is(err, cluster.ErrNothingApplied) {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err != nil {
		l.partial = append(l.partial, labels...)
	} else {
		l.shipped = append(l.shipped, labels...)
	}
	return err
}

// Shipped is every stage that completed, in order.
func (l *shipLog) Shipped() []string {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.shipped...)
}

// Partial is every stage that started and failed, in order.
func (l *shipLog) Partial() []string {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.partial...)
}

// changedAnything reports whether any stage reached a live target, completed
// or not.
func (l *shipLog) changedAnything() bool {
	return len(l.Shipped()) > 0 || len(l.Partial()) > 0
}

// annotate is the apply's failure as the LEDGER should record it: when some of
// the deploy shipped before it failed, say so ahead of the error, so a later
// plan's "its apply FAILED (…)" carries the stages that are in fact live.
// Ahead, because the ledger keeps a bounded summary and the error is the part a
// reader can rediscover.
func (l *shipLog) annotate(err error) error {
	if err == nil || !l.changedAnything() {
		return err
	}
	var parts []string
	if s := l.Shipped(); len(s) > 0 {
		parts = append(parts, "shipped "+strings.Join(s, "; "))
	}
	if p := l.Partial(); len(p) > 0 {
		parts = append(parts, "partly applied "+strings.Join(p, "; "))
	}
	return fmt.Errorf("PARTLY APPLIED (%s): %w", strings.Join(parts, "; "), err)
}
