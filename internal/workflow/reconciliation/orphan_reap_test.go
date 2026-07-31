// Copyright (c) 2025 Reliant Labs. All rights reserved.
package reconciliation

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

// Reaping orphaned descendants is the one repair the per-workflow loop
// structurally cannot make.
//
// reconcileWorkflow returns immediately for every workflow with a parent_id
// (TestReconciler_ChildWorkflow_Skipped locks that in) on the premise that a
// child's lifecycle belongs to its parent. A TerminateWorkflow breaks the
// premise: the hard kill skips the workflow's completion handler, so the
// terminal-status cascade never runs and the subtree is stranded at
// running/paused with nothing that will ever look at it again. That is what
// left forty dead rows being reported by `workflow ps` as live runs.
//
// So the reap must happen at the PASS level, and it must happen on every pass
// — including the passes where the enumerated per-workflow detectors have
// nothing to do, which is exactly the state a fully-cancelled run leaves
// behind (root terminal, descendants stranded, nothing left running).

func TestReconciler_ReapsOrphanedDescendantsEveryPass(t *testing.T) {
	repo := newMockRepo()
	repo.reapRows = 3
	// Deliberately EMPTY: after a cancel the root is terminal and nothing is
	// listed as running. The pre-fix pass returned here before touching
	// anything, which is why the orphans survived every 30s poll forever.
	repo.workflowsByStatus[db.WorkflowStatusRunning] = nil
	repo.workflowsByStatus[db.WorkflowStatusPaused] = nil

	reconciler := NewReconciler(repo, &mockReconcilerTemporalClient{}, nil)

	reconciled, errs := reconciler.ReconcileRunningWorkflows(context.Background())
	require.Empty(t, errs)

	assert.Equal(t, 1, repo.reapCalls,
		"a pass with nothing running must still reap: the orphan-producing state is precisely one where nothing is running")
	assert.Equal(t, 3, reconciled,
		"reaped rows are repairs and must be reported as such — a silent repair reads as a clean pass")
}

func TestReconciler_ReapRunsBeforeWorkflowsAreListed(t *testing.T) {
	repo := newMockRepo()
	repo.reapRows = 1
	repo.workflowsByStatus[db.WorkflowStatusRunning] = []*db.Workflow{runningWorkflow()}

	tempClient := &mockReconcilerTemporalClient{}
	tempClient.setPollersActive(true)
	reconciler := NewReconciler(repo, tempClient, nil)

	_, _ = reconciler.ReconcileRunningWorkflows(context.Background())

	assert.Equal(t, 1, repo.reapCalls,
		"the reap must run on a pass that also has live workflows to adjudicate")
	require.NotEmpty(t, repo.callOrder)
	assert.Equal(t, "reap", repo.callOrder[0],
		"reap before list: a row already known dead must never be adjudicated as a live one")
}

func TestReconciler_ReapFailureIsReportedAndDoesNotAbortThePass(t *testing.T) {
	repo := newMockRepo()
	repo.reapErr = errors.New("db unavailable")
	repo.workflowsByStatus[db.WorkflowStatusRunning] = []*db.Workflow{runningWorkflow()}

	tempClient := &mockReconcilerTemporalClient{}
	tempClient.setPollersActive(true)
	reconciler := NewReconciler(repo, tempClient, nil)

	_, errs := reconciler.ReconcileRunningWorkflows(context.Background())

	require.Len(t, errs, 1, "a failed reap is an error the caller must see, not a silent skip")
	assert.Contains(t, errs[0].Error(), "reap orphaned workflow descendants")
	assert.Equal(t, 1, repo.reapCalls)
	// The rest of the pass still ran: the reap is a backstop, not a gate on
	// the detectors that repair live workflows.
	assert.Contains(t, repo.callOrder, "list-"+db.WorkflowStatusRunning.String(),
		"the pass must continue past a failed reap")
}
