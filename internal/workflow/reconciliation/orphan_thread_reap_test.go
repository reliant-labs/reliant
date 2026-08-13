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

// Reaping orphaned threads is the thread-lifecycle counterpart of
// TestReconciler_ReapsOrphanedDescendantsEveryPass — read that file's header
// comment first, this repairs the exact same shape one level over.
//
// threads.status is written only by ThreadStatusActivity on the live path.
// CascadeTerminalStatusToDescendants moving a workflow subtree to a terminal
// status has never implied anything about the thread rows those workflows
// own — measured on the live DB: 288 threads stranded at status=2 under an
// already-terminal workflow (see
// docs/incidents/2026-08-12-spawn-history-cap.md). That measurement is
// exactly what a forgotten (or Temporal-terminate-skipped) cascade call site
// looks like at scale, and it is a strictly WORSE failure than the workflow
// case alone: ListThreadsWithOrphanedAgentMessages only matches threads
// already in a terminal status, so a stranded thread's own orphaned mailbox
// becomes permanently invisible to the sweep that exists to resolve it.
//
// So this reap must run at the PASS level too, on every pass, for the same
// reason the workflow reap does: a fully-cancelled run leaves the root
// terminal, its threads stranded, and nothing left "running" for the
// per-workflow loop to visit.

func TestReconciler_ReapsOrphanedThreadsEveryPass(t *testing.T) {
	repo := newMockRepo()
	repo.reapThreadsRows = 3
	// Deliberately EMPTY, mirroring the workflow-reap test: after a cancel
	// the root is terminal and nothing is listed as running. A pass that
	// returns early here is exactly the state that let the 288 rows survive
	// every poll forever.
	repo.workflowsByStatus[db.WorkflowStatusRunning] = nil
	repo.workflowsByStatus[db.WorkflowStatusPaused] = nil

	reconciler := NewReconciler(repo, &mockReconcilerTemporalClient{}, nil)

	reconciled, errs := reconciler.ReconcileRunningWorkflows(context.Background())
	require.Empty(t, errs)

	assert.Equal(t, 1, repo.reapThreadsCalls,
		"a pass with nothing running must still reap threads: the orphan-producing state is precisely one where nothing is running")
	assert.Equal(t, 3, reconciled,
		"reaped thread rows are repairs and must be reported as such — a silent repair reads as a clean pass")
}

func TestReconciler_ReapThreadsRunsAfterWorkflowReapSameBefore(t *testing.T) {
	repo := newMockRepo()
	repo.reapRows = 1
	repo.reapThreadsRows = 1
	repo.workflowsByStatus[db.WorkflowStatusRunning] = []*db.Workflow{runningWorkflow()}

	tempClient := &mockReconcilerTemporalClient{}
	tempClient.setPollersActive(true)
	reconciler := NewReconciler(repo, tempClient, nil)

	_, _ = reconciler.ReconcileRunningWorkflows(context.Background())

	assert.Equal(t, 1, repo.reapThreadsCalls,
		"the thread reap must run on a pass that also has live workflows to adjudicate")
	require.NotEmpty(t, repo.callOrder)
	require.GreaterOrEqual(t, len(repo.callOrder), 2)
	assert.Equal(t, "reap", repo.callOrder[0],
		"the workflow reap runs first")
	assert.Equal(t, "reap-threads", repo.callOrder[1],
		"the thread reap runs immediately after: a workflow the first reap just closed is exactly the condition the second looks for, and both must land in the same pass")
}

func TestReconciler_ReapThreadsFailureIsReportedAndDoesNotAbortThePass(t *testing.T) {
	repo := newMockRepo()
	repo.reapThreadsErr = errors.New("db unavailable")
	repo.workflowsByStatus[db.WorkflowStatusRunning] = []*db.Workflow{runningWorkflow()}

	tempClient := &mockReconcilerTemporalClient{}
	tempClient.setPollersActive(true)
	reconciler := NewReconciler(repo, tempClient, nil)

	_, errs := reconciler.ReconcileRunningWorkflows(context.Background())

	require.Len(t, errs, 1, "a failed thread reap is an error the caller must see, not a silent skip")
	assert.Contains(t, errs[0].Error(), "reap orphaned threads")
	assert.Equal(t, 1, repo.reapThreadsCalls)
	// The rest of the pass still ran: the reap is a backstop, not a gate on
	// the detectors that repair live workflows.
	assert.Contains(t, repo.callOrder, "list-"+db.WorkflowStatusRunning.String(),
		"the pass must continue past a failed thread reap")
}
