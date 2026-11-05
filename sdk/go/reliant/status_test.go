// Copyright (c) 2025 Reliant Labs
// SPDX-License-Identifier: Apache-2.0

package reliant

import (
	"context"
	"errors"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1"
)

func TestStatusKeepsLifecycleAndVerdictApart(t *testing.T) {
	fd := &feed{}
	fd.setRoot(completed, reasonOK, OutcomeFailure)
	f := &fake{}
	fd.install(f)
	c, _ := f.start(t)

	st, err := c.Runs.Status(context.Background(), "r-1")
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != "completed" || st.Outcome != OutcomeFailure || st.Succeeded {
		t.Fatalf("status = %+v: ran to its failure node, so completed AND not succeeded", st)
	}
}

func TestStatusStillRunningIsNotASuccess(t *testing.T) {
	fd := &feed{}
	fd.setRoot(reliantv1.WorkflowState_WORKFLOW_STATE_ACTIVE, 0, "")
	f := &fake{}
	fd.install(f)
	c, _ := f.start(t)

	st, err := c.Runs.Status(context.Background(), "r-1")
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != "running" || st.Succeeded {
		t.Fatalf("a run that has not finished = %+v, must not read as succeeded", st)
	}
}

func TestStatusMarksOnlyTheGatedThreadAndCountsOpenGates(t *testing.T) {
	meta := `{"type":"ask_user","questions":[{"question":"a"},{"question":"b"}]}`
	fd := &feed{
		pending:  []*reliantv1.QuestionInfo{{QuestionId: "q-1", StepId: "review", ThreadId: "th-gated", Metadata: &meta}},
		approval: []*reliantv1.Approval{{Id: "a-1", Status: reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING}},
	}
	fd.setRoot(reliantv1.WorkflowState_WORKFLOW_STATE_ACTIVE, 0, "")
	fd.root.Children = []*reliantv1.WorkflowExecution{
		{Id: "c1", Thread: "th-gated", WorkflowName: "builtin://agent", SpawnedByNodeId: sp("impl_1"), State: reliantv1.WorkflowState_WORKFLOW_STATE_ACTIVE},
		{Id: "c2", Thread: "th-free", WorkflowName: "builtin://agent", SpawnedByNodeId: sp("impl_2"), State: reliantv1.WorkflowState_WORKFLOW_STATE_ACTIVE},
	}
	f := &fake{}
	fd.install(f)
	c, _ := f.start(t)

	st, err := c.Runs.Status(context.Background(), "r-1")
	if err != nil {
		t.Fatal(err)
	}
	if st.OpenQuestions != 2 || st.OpenApprovals != 1 || len(st.Gates) != 2 {
		t.Fatalf("open questions=%d (a bundled question counts its sub-questions), approvals=%d, gates=%d", st.OpenQuestions, st.OpenApprovals, len(st.Gates))
	}
	if len(st.Nodes) != 2 {
		t.Fatalf("nodes = %+v", st.Nodes)
	}
	if st.Nodes[0].Gate != "review" {
		t.Fatalf("the gated thread = %+v, want it marked with the step that raised the question", st.Nodes[0])
	}
	if st.Nodes[1].Gate != "" {
		t.Fatalf("a sibling thread is still running, not gated: %+v", st.Nodes[1])
	}
}

func TestStatusAsksForTheFullView(t *testing.T) {
	var view reliantv1.WorkflowExecutionView = -1
	fd := &feed{}
	fd.setRoot(completed, reasonOK, "")
	f := &fake{}
	fd.install(f)
	inner := f.getWorkflowExecutions
	f.getWorkflowExecutions = func(r *reliantv1.GetWorkflowExecutionsRequest) (*reliantv1.GetWorkflowExecutionsResponse, error) {
		view = r.GetView()
		return inner(r)
	}
	c, _ := f.start(t)
	if _, err := c.Runs.Status(context.Background(), "r-1"); err != nil {
		t.Fatal(err)
	}
	if view != reliantv1.WorkflowExecutionView_WORKFLOW_EXECUTION_VIEW_FULL {
		t.Fatalf("view = %v: the BASIC view withholds the internal steps nearly every step is", view)
	}
}

func TestStatusOfARunWithNoExecutionIsNotFound(t *testing.T) {
	fd := &feed{}
	f := &fake{}
	fd.install(f)
	c, _ := f.start(t)
	if _, err := c.Runs.Status(context.Background(), "r-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
