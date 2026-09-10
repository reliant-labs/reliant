// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"go.temporal.io/sdk/workflow"
)

// StructuralNodesCompletedQuery is the query that reports every STRUCTURAL node
// this run drove to completion — `loop` and `workflow` nodes — as
// fully-qualified dotted node paths, together with the outputs each published.
//
// It exists for the same reason JoinsSatisfiedQuery does: a structural node
// runs no activity of its own. Its BODY dispatches activities, which is why
// `impl_loop.attempt.implement` is observable, but the node's OWN result is
// computed in-workflow and written straight into the node-output store
// (model.ProtoLoopOutputToMap -> nodeOutputStore.Set for a loop; the returned
// output map for a `workflow` node). Nothing at the activity layer ever sees
// it. Two consequences, and this query fixes both:
//
//   - The node's evaluated outputs are invisible. get-it-right's `attempt` loop
//     publishes `eval_strategy` and `review_grade`, the workflow-level outputs
//     derived from them agree byte-for-byte with the fast simulator, and yet
//     the per-node view held only the `_iterations` count the harness
//     synthesized from checkpoints.
//   - Its COMPLETION is invisible. A per-iteration WorkflowCheckpoint proves a
//     loop was entered, so it lands in `reached`, but nothing marks the moment
//     it finished — one-ring's `impl_loop` could never be asserted completed.
//
// A query rather than an activity, deliberately, and for the same reason the
// join observer gave: every activity goes through ActivityWrapper, which writes
// a step_executions row and emits a node execution event. A LoopCompleted
// activity would therefore make a test-visibility need change what production
// PERSISTS. A query handler reads state the workflow already holds in memory
// and writes nothing.
const StructuralNodesCompletedQuery = "get_structural_nodes_completed"

// StructuralCompletion is one completed structural node: its fully-qualified
// graph path and the output map it published, exactly as the value its caller
// is about to store in the node-output store.
//
// A slice rather than a map, so completion ORDER is preserved and the query
// result is deterministic across replays.
type StructuralCompletion struct {
	Path    string                 `json:"path"`
	Outputs map[string]interface{} `json:"outputs"`
}

// structuralObserver records the structural nodes a run completed, in order.
//
// It records rather than decides. Its only callers are the success returns of
// InlineLoopExecutor.Execute and InlineWorkflowExecutor.Execute — the single
// shared exit each node type already passes through — and each reads the value
// that call is about to return. Nothing here can change whether a node
// completes, how many iterations it ran, or what it publishes.
type structuralObserver struct {
	completions []StructuralCompletion
	index       map[string]int
}

func newStructuralObserver() *structuralObserver {
	return &structuralObserver{index: map[string]int{}}
}

// record notes that the structural node at this fully-qualified path completed
// with these outputs.
//
// A repeated path overwrites the outputs in place and keeps its original
// position. Re-entry is real — a node nested in an outer loop's body completes
// once per outer iteration — and the truthful per-node view is the LAST result
// that node published, which is exactly what the node-output store itself holds
// after the same sequence of writes.
//
// No mutex: workflow goroutines, including parallel loop iterations, are
// cooperatively scheduled by the Temporal SDK on a single thread, which is what
// makes replay deterministic. A lock here would guard against concurrency the
// runtime does not have.
func (o *structuralObserver) record(path string, outputs map[string]interface{}) {
	if path == "" {
		return
	}
	if i, ok := o.index[path]; ok {
		o.completions[i].Outputs = outputs
		return
	}
	o.index[path] = len(o.completions)
	o.completions = append(o.completions, StructuralCompletion{Path: path, Outputs: outputs})
}

func (o *structuralObserver) snapshot() []StructuralCompletion {
	return append([]StructuralCompletion(nil), o.completions...)
}

type structuralObserverKey struct{}

// withStructuralObserver attaches an observer to the workflow context so every
// nested executor — loop bodies, inline sub-workflows, parallel iterations —
// reports into the same one.
//
// Context is the transport for the same reason it is for joins: a loop can sit
// four levels down, and threading an extra constructor argument through
// InlineLoopExecutor and InlineWorkflowExecutor would put an observability
// concern into executor signatures with no other reason to know about it.
// Derived contexts inherit values, including the ones workflow.Go hands to
// parallel iterations.
func withStructuralObserver(ctx workflow.Context, obs *structuralObserver) workflow.Context {
	return workflow.WithValue(ctx, structuralObserverKey{}, obs)
}

// recordStructuralCompleted reports a completed structural node at its
// fully-qualified path. A no-op when no observer is attached, which is every
// production run: nothing queries this outside the scenario lane, and the
// workflow behaves identically either way.
func recordStructuralCompleted(ctx workflow.Context, path string, outputs map[string]interface{}) {
	if ctx == nil || path == "" {
		return
	}
	if obs, ok := ctx.Value(structuralObserverKey{}).(*structuralObserver); ok && obs != nil {
		obs.record(path, outputs)
	}
}
