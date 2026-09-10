// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"go.temporal.io/sdk/workflow"
)

// JoinsSatisfiedQuery is the query that reports every join node this run has
// satisfied, as fully-qualified dotted node paths.
//
// It exists because a join is the one node type that announces itself NOWHERE
// else. A join runs no activity — every dispatch loop filters join steps out,
// since a join's work is done by its SOURCES completing — so no activity-level
// observer can see one. And it has no nodes underneath it, so unlike a `loop`
// or a `workflow` node it cannot be recovered from the composed NodePath of its
// children either.
//
// A query rather than an activity, deliberately. The obvious alternative is a
// JoinSatisfied activity in the shape of SkippedStep, but every activity is
// wrapped by ActivityWrapper, which writes a step_executions row and emits a
// node execution event. That would make a test-visibility need change what
// production PERSISTS — new rows, for a node that has never had any. A query
// handler reads state the workflow already holds in memory and writes nothing.
const JoinsSatisfiedQuery = "get_joins_satisfied"

// joinObserver records the joins a run satisfied, in order.
//
// It records rather than decides: the only caller is the satisfaction branch of
// processJoinEvents, which is the single shared implementation both the real
// runtime and the fast simulator use to decide a join fired. Nothing here can
// change whether a join is satisfied or what happens next.
type joinObserver struct {
	paths []string
	seen  map[string]bool
}

func newJoinObserver() *joinObserver {
	return &joinObserver{seen: map[string]bool{}}
}

// record notes that the join at this fully-qualified path was satisfied.
//
// Deduplicated because a join inside a loop body is satisfied once per
// iteration and reports the same path each time — the same "first reach wins"
// convention reached-node reporting already uses.
//
// No mutex: workflow goroutines (including parallel loop iterations) are
// cooperatively scheduled by the Temporal SDK on a single thread, which is what
// makes replay deterministic. A lock here would guard against concurrency the
// runtime does not have.
func (o *joinObserver) record(path string) {
	if path == "" || o.seen[path] {
		return
	}
	o.seen[path] = true
	o.paths = append(o.paths, path)
}

func (o *joinObserver) snapshot() []string {
	return append([]string(nil), o.paths...)
}

type joinObserverKey struct{}

// withJoinObserver attaches an observer to the workflow context so every
// nested executor — loop bodies, inline sub-workflows, parallel iterations —
// reports into the same one.
//
// Context is the transport because a join can be satisfied four levels down,
// and threading an extra constructor argument through InlineLoopExecutor,
// InlineWorkflowExecutor and ParallelLoopExecutor would put an observability
// concern into three executor signatures that have no other reason to know
// about it. Derived contexts inherit values, including the ones workflow.Go
// hands to parallel iterations.
func withJoinObserver(ctx workflow.Context, obs *joinObserver) workflow.Context {
	return workflow.WithValue(ctx, joinObserverKey{}, obs)
}

// recordJoinSatisfied reports a satisfied join at its fully-qualified path.
// A no-op when no observer is attached, which is every production run: nothing
// queries this outside the scenario lane, and the workflow behaves identically
// either way.
func recordJoinSatisfied(ctx workflow.Context, path string) {
	if ctx == nil {
		return
	}
	if obs, ok := ctx.Value(joinObserverKey{}).(*joinObserver); ok && obs != nil {
		obs.record(path)
	}
}
