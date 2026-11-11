// Copyright (c) 2025 Reliant Labs
// SPDX-License-Identifier: Apache-2.0

package reliant

import (
	"fmt"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1"
)

func sp(s string) *string     { return &s }
func int32Ptr(i int32) *int32 { return &i }
func boolPtr(b bool) *bool    { return &b }

// A run with two phases, both running builtin://get-it-right, is the shape
// that misread. Phase impl_1 has review_enabled: false, so its `review` node
// is skipped and the SkippedStep activity records a row for it. Phase impl_2's
// reviewer RAN — as a workflow-type node, which executes as a child workflow
// and writes no step_executions row of its own.
//
// Flattened on step_id, those two phases produce ONE `review` row, sourced
// from the phase that skipped it, and the phase that actually reviewed
// contributes nothing. A reader concludes the reviewer was skipped. That
// happened, and the conclusion went to the owner.
func twoPhaseRun() *reliantv1.WorkflowExecution {
	return &reliantv1.WorkflowExecution{
		Id:           "root",
		WorkflowName: "builtin://forge-one-shot",
		State:        reliantv1.WorkflowState_WORKFLOW_STATE_STOPPED,
		StopReason:   reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_COMPLETED,
		CreatedAt:    "2026-07-26T10:00:00Z",
		Children: []*reliantv1.WorkflowExecution{
			{
				Id:              "wf-impl-1",
				WorkflowName:    "builtin://get-it-right",
				SpawnedByNodeId: sp("impl_1"),
				State:           reliantv1.WorkflowState_WORKFLOW_STATE_STOPPED,
				StopReason:      reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_COMPLETED,
				CreatedAt:       "2026-07-26T10:00:01Z",
				Steps: []*reliantv1.StepExecution{
					{StepId: "lint", ActivityName: "ExecuteRunStep", CreatedAt: "2026-07-26T10:00:02Z", Success: boolPtr(true), ExitCode: int32Ptr(0)},
					// review_enabled: false — this reviewer never ran.
					{StepId: "review", ActivityName: activitySkippedStep, CreatedAt: "2026-07-26T10:00:03Z"},
				},
			},
			{
				Id:              "wf-impl-2",
				WorkflowName:    "builtin://get-it-right",
				SpawnedByNodeId: sp("impl_2"),
				State:           reliantv1.WorkflowState_WORKFLOW_STATE_STOPPED,
				StopReason:      reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_COMPLETED,
				CreatedAt:       "2026-07-26T10:00:01Z",
				Steps: []*reliantv1.StepExecution{
					{StepId: "lint", ActivityName: "ExecuteRunStep", CreatedAt: "2026-07-26T10:00:02Z", Success: boolPtr(true), ExitCode: int32Ptr(0)},
				},
				Children: []*reliantv1.WorkflowExecution{
					{
						Id:              "wf-impl-2-review",
						WorkflowName:    "builtin://agent",
						SpawnedByNodeId: sp("review"),
						State:           reliantv1.WorkflowState_WORKFLOW_STATE_STOPPED,
						StopReason:      reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_COMPLETED,
						CreatedAt:       "2026-07-26T10:00:20Z",
						CompletedAt:     sp("2026-07-26T10:00:50Z"),
						Steps: []*reliantv1.StepExecution{
							{StepId: "call_llm", ActivityName: "CallLLM", CreatedAt: "2026-07-26T10:00:30Z", Success: boolPtr(true)},
						},
					},
				},
			},
		},
	}
}

// groupFor finds one node thread's steps, failing loudly when the derived set
// is empty — an assertion over a group that was never produced would otherwise
// pass by vacuity.
func groupFor(t *testing.T, groups []StatusStepGroup, node string) StatusStepGroup {
	t.Helper()
	if len(groups) == 0 {
		t.Fatalf("summarizeSteps produced no groups at all; nothing below is being checked")
	}
	for _, g := range groups {
		if g.Node == node {
			return g
		}
	}
	var have []string
	for _, g := range groups {
		have = append(have, g.Node)
	}
	t.Fatalf("no step group for node %q; groups present: %v", node, have)
	return StatusStepGroup{}
}

func stepIn(t *testing.T, g StatusStepGroup, stepID string) StatusStep {
	t.Helper()
	if len(g.Steps) == 0 {
		t.Fatalf("node %q recorded no steps at all", g.Node)
	}
	for _, s := range g.Steps {
		if s.StepID == stepID {
			return s
		}
	}
	var have []string
	for _, s := range g.Steps {
		have = append(have, s.StepID)
	}
	t.Fatalf("node %q has no step %q; steps present: %v", g.Node, stepID, have)
	return StatusStep{}
}

// The skipped reviewer belongs to impl_1 and to nothing else.
func TestStepsAreAttributedToTheOwningPhase(t *testing.T) {
	groups := SummarizeSteps(twoPhaseRun())

	skipped := stepIn(t, groupFor(t, groups, "impl_1"), "review")
	if skipped.Result != "SKIP" {
		t.Fatalf("impl_1's skipped reviewer reports %q, want SKIP", skipped.Result)
	}

	// impl_2's reviewer ran. Its row must not be the other phase's skip.
	ran := stepIn(t, groupFor(t, groups, "impl_2"), "review")
	if ran.Result == "SKIP" {
		t.Fatalf("impl_2's reviewer RAN but reports SKIP — this is the misattribution: one phase's skip standing in for another phase's work")
	}

	// Each phase must own its own lint row; a merged table would have exactly
	// one, with a run count of 2.
	for _, node := range []string{"impl_1", "impl_2"} {
		if runs := stepIn(t, groupFor(t, groups, node), "lint").Runs; runs != 1 {
			t.Fatalf("node %s reports %d lint runs, want 1 — rows from another phase merged into it", node, runs)
		}
	}
}

// A workflow-type node executes as a child workflow and writes no
// step_executions row. The phase that ran one showed nothing at all.
func TestWorkflowTypeNodeIsVisible(t *testing.T) {
	groups := SummarizeSteps(twoPhaseRun())

	review := stepIn(t, groupFor(t, groups, "impl_2"), "review")
	if review.Activity != "builtin://agent" {
		t.Fatalf("impl_2's review row names activity %q, want the child workflow it ran", review.Activity)
	}
	if review.LastMs != 30000 {
		t.Fatalf("impl_2's review row reports %dms, want the child workflow's own duration", review.LastMs)
	}

	// And the work it did is reachable, attributed to that node.
	inner := stepIn(t, groupFor(t, groups, "impl_2/review"), "call_llm")
	if inner.Result != "ok" {
		t.Fatalf("the reviewer's own step reports %q, want ok", inner.Result)
	}
}

// A retry loop runs the same step once per iteration, and the aggregate keeps
// only the most recent execution. Four red iterations followed by one green
// therefore printed as a single "ok" line: the four failures were not merely
// unattributed, they were gone. loop_node_id and loop_iteration have been
// populated columns and proto fields the whole time.
func TestFailedLoopIterationsSurviveAggregation(t *testing.T) {
	var steps []*reliantv1.StepExecution
	for i := 0; i < 5; i++ {
		exit := int32(1)
		success := false
		if i == 4 {
			exit, success = 0, true
		}
		steps = append(steps, &reliantv1.StepExecution{
			StepId:        "test",
			ActivityName:  "ExecuteRunStep",
			CreatedAt:     fmt.Sprintf("2026-07-26T10:0%d:00Z", i),
			Success:       &success,
			ExitCode:      &exit,
			LoopNodeId:    sp("attempt"),
			LoopIteration: int32Ptr(int32(i)),
		})
	}

	groups := SummarizeSteps(&reliantv1.WorkflowExecution{
		Id:           "root",
		WorkflowName: "builtin://get-it-right",
		CreatedAt:    "2026-07-26T10:00:00Z",
		Steps:        steps,
	})

	got := stepIn(t, groupFor(t, groups, ""), "test")
	if got.Runs != 5 {
		t.Fatalf("runs = %d, want 5", got.Runs)
	}
	if got.Failed != 4 {
		t.Fatalf("failed = %d, want 4 — four red iterations disappeared behind the one that passed", got.Failed)
	}

}

// A skipped step must never read as a step that passed.
func TestSkippedStepIsNotPassing(t *testing.T) {
	skipped := stepIn(t, groupFor(t, SummarizeSteps(twoPhaseRun()), "impl_1"), "review")
	if skipped.Result == "ok" {
		t.Fatalf("a step that never ran reads %q", skipped.Result)
	}
}
