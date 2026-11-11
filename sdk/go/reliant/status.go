// Copyright (c) 2025 Reliant Labs
// SPDX-License-Identifier: Apache-2.0

package reliant

import (
	"context"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1"
)

// activitySkippedStep is the activity name a skipped node records under: the
// only thing that survives to a step reader saying "this node did not run".
// (internal/buildmode.TestSDKSkippedStepActivityMatchesTheEngine holds it to
// the engine's own constant.)
const activitySkippedStep = "SkippedStep"

// RunStatus is a one-shot snapshot of a run: its lifecycle and verdict as two
// separate facts, the tree of node threads, what each thread's steps did, and
// what it is waiting on.
type RunStatus struct {
	RunID    string
	Workflow string
	// Status is the LIFECYCLE: did the execution finish, and how.
	Status string
	// Outcome is the run's own VERDICT, "success" or "failure", declared by the
	// terminal node it reached. Empty when the workflow declared none; that is
	// "it never said", not failure.
	Outcome string
	// Succeeded combines the two ([Succeeded]). A run that routed to its failure
	// node is Status "completed", Outcome "failure", Succeeded false.
	Succeeded bool
	Started   string
	Completed string

	// OpenQuestions counts unanswered sub-questions (a gated thread's question
	// may bundle several); OpenApprovals counts pending approvals.
	OpenQuestions int
	OpenApprovals int
	// Gates are what the run is parked on.
	Gates []Gate

	// Nodes is the tree of child workflows, one per node thread.
	Nodes []StatusNode
	// Steps are the recorded step executions, grouped by the node thread that
	// ran them. Never flattened: two phases running the same workflow have steps
	// with the same names, and a merged table attributes one phase's rows to the
	// other.
	Steps []StatusStepGroup
}

// StatusNode is one node thread of the tree.
type StatusNode struct {
	NodeID string
	// Record is the workflow the thread ran.
	Record string
	Thread string
	Status string
	// Gate is the step that raised this thread's open question, when it has
	// one. Only that thread is marked: its siblings are still running.
	Gate       string
	Started    string
	Completed  string
	DurationMs int64
	Iteration  *int32
	Children   []StatusNode
}

// StatusStep is one step of a thread, aggregated over every time it ran.
type StatusStep struct {
	StepID   string
	Activity string
	// Runs is how many times the step ran; Failed how many of those did not
	// pass. Runs and Result alone report a lane that failed four loop
	// iterations and passed the fifth as plainly "ok": the aggregate keeps only
	// the most recent execution, and the red ones would vanish.
	Runs   int
	Failed int
	// Result is the most recent execution's verdict: "ok", "FAIL", "SKIP" (the
	// node's condition was false and it never ran), "-" (no verdict), or for a
	// child-workflow node its lifecycle word.
	Result   string
	ExitCode *int32
	LastMs   int64
}

// StatusStepGroup is the steps recorded by ONE node thread.
type StatusStepGroup struct {
	// Node is the path of node ids from the root graph down to the thread that
	// recorded these steps: "impl_2" for a phase, "impl_2/agent_loop" for a
	// workflow node inside it. Empty for the root graph's own steps.
	Node string
	// Record is the workflow this thread ran.
	Record string
	Steps  []StatusStep
}

// Status takes a snapshot of a run. It reads the full step history, which is
// heavy for a long run; use [RunsClient.Get] for the state alone.
//
// It reports this run alone: its own execution and the child workflows under
// it, and only their gates, even when the session holds other runs.
func (r *RunsClient) Status(ctx context.Context, runID string) (*RunStatus, error) {
	sessionID, err := r.sessionOf(ctx, "runs.status", runID)
	if err != nil {
		return nil, err
	}
	return r.statusOf(ctx, runID, sessionID, newRunScope(runID))
}

// StatusSession is [RunsClient.Status] for a session id (see
// [RunsClient.FollowSession]); it reports the session's current run.
func (r *RunsClient) StatusSession(ctx context.Context, sessionID string) (*RunStatus, error) {
	if sessionID == "" {
		return nil, r.c.fail("runs.statusSession", ErrInvalidArgument, "session id is required")
	}
	return r.statusOf(ctx, sessionID, sessionID, nil)
}

// statusOf reports one run when scope is set, else the session's newest root.
func (r *RunsClient) statusOf(ctx context.Context, runID, sessionID string, scope *runScope) (*RunStatus, error) {
	const op = "runs.status"
	// FULL: SummarizeSteps counts every step, and BASIC withholds the internal
	// ones (SaveMessage, CallLLM, ...) that make up nearly all of them.
	roots, err := r.sessionTree(ctx, sessionID, reliantv1.WorkflowExecutionView_WORKFLOW_EXECUTION_VIEW_FULL)
	if err != nil {
		return nil, r.c.wrap(op, err)
	}
	var root *reliantv1.WorkflowExecution
	switch {
	case scope != nil:
		root = scope.load(roots)
	case len(roots) > 0:
		root = roots[0] // newest first
	}
	if root == nil {
		return nil, r.c.fail(op, ErrNotFound, "no workflow execution found for run %s", runID)
	}

	st := &RunStatus{
		RunID:     runID,
		Workflow:  root.GetWorkflowName(),
		Status:    StatusLabel(root.GetState(), root.GetStopReason()),
		Outcome:   root.GetOutcome(),
		Started:   root.GetCreatedAt(),
		Completed: root.GetCompletedAt(),
	}
	st.Succeeded = IsTerminal(st.Status) && Succeeded(st.Status, st.Outcome)

	// Gates are best effort here, as the CLI has always had them: a snapshot is
	// still worth having when the gate lists are momentarily unavailable.
	gateByThread := map[string]string{}
	gates, _ := r.c.Gates.pending(ctx, sessionID)
	if scope != nil {
		gates = scope.filter(gates)
	}
	if len(gates) > 0 {
		st.Gates = gates
		for _, g := range gates {
			switch g.Kind {
			case GateQuestion:
				open := 1
				if len(g.Prompts) > 1 {
					open = len(g.Prompts)
				}
				st.OpenQuestions += open
				if g.ThreadID != "" {
					// Oldest first, so a thread's newest question wins.
					gateByThread[g.ThreadID] = dash(g.StepID)
				}
			case GateApproval:
				st.OpenApprovals++
			}
		}
	}

	for _, child := range root.GetChildren() {
		st.Nodes = append(st.Nodes, buildStatusNode(child, gateByThread))
	}
	st.Steps = SummarizeSteps(root)
	return st, nil
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func buildStatusNode(wf *reliantv1.WorkflowExecution, gateByThread map[string]string) StatusNode {
	node := StatusNode{
		NodeID:    wf.GetSpawnedByNodeId(),
		Record:    wf.GetWorkflowName(),
		Thread:    wf.GetThread(),
		Status:    StatusLabel(wf.GetState(), wf.GetStopReason()),
		Gate:      gateByThread[wf.GetThread()],
		Started:   wf.GetCreatedAt(),
		Completed: wf.GetCompletedAt(),
	}
	if node.NodeID == "" {
		node.NodeID = wf.GetWorkflowName()
	}
	if wf.Iteration != nil {
		it := wf.GetIteration()
		node.Iteration = &it
	}
	node.DurationMs = durationMs(wf.GetCreatedAt(), wf.GetCompletedAt())
	for _, child := range wf.GetChildren() {
		node.Children = append(node.Children, buildStatusNode(child, gateByThread))
	}
	return node
}

// SummarizeSteps groups step executions by the node thread that recorded them,
// and within a thread by step id, reporting run count and the most recent
// result.
//
// Two things a flat version cannot say, and this one must:
//
//   - WHICH phase a row belongs to. Steps are already scoped by workflow_id and
//     the tree already says which node spawned each child workflow, so the
//     attribution is in the response: it was being discarded, not missing.
//   - That a workflow-type node ran at all. Such a node executes as a child
//     workflow and writes NO step_executions row of its own, so the phase whose
//     reviewer actually ran showed nothing while the phase that skipped its
//     reviewer showed a row. One row is synthesized per child workflow, in the
//     parent's group, from the child's own execution record.
func SummarizeSteps(root *reliantv1.WorkflowExecution) []StatusStepGroup {
	type agg struct {
		activity string
		runs     int
		failed   int
		result   string
		exit     *int32
		lastMs   int64
		lastAt   string
	}
	type group struct {
		record string
		byStep map[string]*agg
		order  []string
	}

	groups := map[string]*group{}
	var groupOrder []string

	groupFor := func(node, record string) *group {
		g := groups[node]
		if g == nil {
			g = &group{record: record, byStep: map[string]*agg{}}
			groups[node] = g
			groupOrder = append(groupOrder, node)
		}
		return g
	}

	// record folds one execution of one step into its group. createdAt orders
	// repeats so the reported result is the most recent one.
	record := func(g *group, stepID, activity, createdAt string, ms int64, result string, exit *int32) {
		a := g.byStep[stepID]
		if a == nil {
			a = &agg{}
			g.byStep[stepID] = a
			g.order = append(g.order, stepID)
		}
		a.runs++
		// Counted across every execution, not just the surviving one: a loop
		// that ran this step five times and failed four of them must not report
		// as a single "ok".
		if result == "FAIL" {
			a.failed++
		}
		if createdAt >= a.lastAt {
			a.lastAt = createdAt
			a.activity = activity
			a.lastMs = ms
			a.result = result
			a.exit = exit
		}
	}

	var walk func(wf *reliantv1.WorkflowExecution, node string)
	walk = func(wf *reliantv1.WorkflowExecution, node string) {
		g := groupFor(node, wf.GetWorkflowName())

		for _, s := range wf.GetSteps() {
			var exit *int32
			if s.ExitCode != nil {
				ec := s.GetExitCode()
				exit = &ec
			}
			record(g, s.GetStepId(), s.GetActivityName(), s.GetCreatedAt(), s.GetDurationMs(), stepResult(s), exit)
		}

		for _, c := range wf.GetChildren() {
			childNode := c.GetSpawnedByNodeId()
			if childNode == "" {
				childNode = c.GetWorkflowName()
			}
			record(g, childNode, c.GetWorkflowName(), c.GetCreatedAt(),
				durationMs(c.GetCreatedAt(), c.GetCompletedAt()), childWorkflowResult(c), nil)
			walk(c, childNodePath(node, childNode))
		}
	}
	walk(root, "")

	out := make([]StatusStepGroup, 0, len(groupOrder))
	for _, node := range groupOrder {
		g := groups[node]
		if len(g.order) == 0 {
			continue
		}
		steps := make([]StatusStep, 0, len(g.order))
		for _, id := range g.order {
			a := g.byStep[id]
			steps = append(steps, StatusStep{
				StepID: id, Activity: a.activity, Runs: a.runs, Failed: a.failed,
				Result: a.result, ExitCode: a.exit, LastMs: a.lastMs,
			})
		}
		out = append(out, StatusStepGroup{Node: node, Record: g.record, Steps: steps})
	}
	return out
}

// childNodePath joins a parent node path with a child node id. The root graph
// has the empty path, so its immediate children are named by node id alone.
func childNodePath(parent, child string) string {
	if parent == "" {
		return child
	}
	return parent + "/" + child
}

// stepResult is what one recorded step execution claims.
//
// SKIP is its own word. A skipped step records a NULL verdict rather than
// success=true, but "-" would report it the same as a row that simply has no
// verdict, and the distinction that matters is precisely "did this check run".
// The activity name is the emitter's stamp for it; output_json, which carries
// the `skipped` field itself, is deliberately not sent over this RPC because it
// can be megabytes per run.
func stepResult(s *reliantv1.StepExecution) string {
	if s.GetActivityName() == activitySkippedStep {
		return "SKIP"
	}
	if s.Success == nil {
		return "-"
	}
	if s.GetSuccess() {
		return "ok"
	}
	return "FAIL"
}

// childWorkflowResult is what a synthesized row for a workflow-type node
// claims. A child workflow has a lifecycle AND a verdict, exactly like the run
// as a whole, and the two are not the same fact: a child that finished without
// declaring an outcome reports its lifecycle word, never "ok".
func childWorkflowResult(c *reliantv1.WorkflowExecution) string {
	switch c.GetOutcome() {
	case OutcomeFailure:
		return "FAIL"
	case OutcomeSuccess:
		return "ok"
	}
	status := StatusLabel(c.GetState(), c.GetStopReason())
	switch status {
	case "failed", "cancelled":
		return "FAIL"
	}
	return status
}

// durationMs computes elapsed milliseconds between two RFC3339 timestamps,
// returning 0 when either is missing or unparseable.
func durationMs(start, end string) int64 {
	if start == "" || end == "" {
		return 0
	}
	s, err1 := time.Parse(time.RFC3339, start)
	e, err2 := time.Parse(time.RFC3339, end)
	if err1 != nil || err2 != nil {
		return 0
	}
	d := e.Sub(s).Milliseconds()
	if d < 0 {
		return 0
	}
	return d
}
