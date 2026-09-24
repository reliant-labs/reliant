// Copyright (c) 2025 Reliant Labs
package reconciliation

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

// A reset forks the new run from a point INSIDE the current history, so a run
// already at Temporal's history cap is born at the cap and dies again within a
// few events. The reconciler's stuck-task recovery must never reset one; it
// goes straight to terminate + mark failed, which routes the next user
// message to the coarse fresh restart (empty history).
func TestReconciler_StuckActivity_AtHistorySizeLimit_DoesNotReset(t *testing.T) {
	repo := newMockRepo()
	desc := makeStuckActivityDescribeResp("run-1", "act-1", 5*time.Minute)
	desc.WorkflowExecutionInfo.HistoryLength = 38000
	desc.WorkflowExecutionInfo.HistorySizeBytes = 49 * 1024 * 1024
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{"wf-1": {resp: desc}},
		historyEvents:     makeHistoryWithActivity("act-1"),
	}
	tempClient.setPollersActive(true)

	reconciler := NewReconciler(repo, tempClient, stuckTestConfig(2))
	wf := runningWorkflow()

	reconciler.ReconcileWorkflow(context.Background(), wf)
	result := reconciler.ReconcileWorkflow(context.Background(), wf)

	require.NoError(t, result.Error)
	assert.False(t, result.RecoveredByReset)
	assert.Empty(t, tempClient.resetCalls, "a run at the history cap must not be reset")
	require.Len(t, tempClient.terminateCalls, 1)
	assert.Equal(t, db.Failed(), repo.updatedStatuses["wf-1"])
}

func TestReconciler_StuckActivity_AtHistoryCountLimit_DoesNotReset(t *testing.T) {
	repo := newMockRepo()
	desc := makeStuckActivityDescribeResp("run-1", "act-1", 5*time.Minute)
	desc.WorkflowExecutionInfo.HistoryLength = 51000
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{"wf-1": {resp: desc}},
		historyEvents:     makeHistoryWithActivity("act-1"),
	}
	tempClient.setPollersActive(true)

	reconciler := NewReconciler(repo, tempClient, stuckTestConfig(2))
	wf := runningWorkflow()

	reconciler.ReconcileWorkflow(context.Background(), wf)
	result := reconciler.ReconcileWorkflow(context.Background(), wf)

	require.NoError(t, result.Error)
	assert.Empty(t, tempClient.resetCalls)
	require.Len(t, tempClient.terminateCalls, 1)
}

// resumableRootRepo is a mockRepo whose chat's root run died FAILED with its
// checkpoint still recorded — the state the next user message resumes from.
type resumableRootRepo struct {
	*mockRepo
	root       *db.Workflow
	checkpoint *db.WorkflowCheckpoint
}

func (m *resumableRootRepo) GetWorkflow(_ context.Context, id string) (*db.Workflow, error) {
	if m.root != nil && m.root.ID == id {
		return m.root, nil
	}
	return nil, nil
}

func (m *resumableRootRepo) GetWorkflowCheckpoint(_ context.Context, workflowID string) (*db.WorkflowCheckpoint, error) {
	if m.checkpoint != nil && m.checkpoint.WorkflowID == workflowID {
		return m.checkpoint, nil
	}
	return nil, nil
}

// A background spawn is a goroutine in its ROOT's execution, so a root killed
// at the history cap reaps every spawn's child row — which is exactly what
// makes them look "stranded" here. But the root is resumable, and the resume
// relaunches those spawns; a "lost in transit" report written now would take
// each spawn's single terminal-report slot and bury its real result.
func TestRepairStrandedBackgroundSpawns_SkipsSpawnsOfResumableRoot(t *testing.T) {
	root := "chat-1"
	base := newMockRepo()
	base.chats = map[string]*db.Chat{"chat-1": {ID: "chat-1", WorkflowID: &root}}
	base.strandedBackgroundSpawns = []*db.StrandedBackgroundSpawn{
		strandedBackgroundSpawn("tc-1", "chat-1", "chat-1", "child-thread-1", db.Failed()),
	}
	repo := &resumableRootRepo{
		mockRepo:   base,
		root:       &db.Workflow{ID: root, ChatID: "chat-1", Status: db.Failed()},
		checkpoint: &db.WorkflowCheckpoint{WorkflowID: root, ChatID: "chat-1", NodeID: "agent_loop", LoopIteration: 96},
	}
	reconciler := NewReconciler(repo, &mockReconcilerTemporalClient{}, DefaultConfig())

	n, err := reconciler.repairStrandedBackgroundSpawns(context.Background(), &passStats{})
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.Empty(t, base.enqueuedAgentMessages,
		"a spawn the resume will relaunch must not get a lost-in-transit report")
}

// Once the root is no longer resumable (it completed, or its checkpoint was
// dropped), the repair runs as before.
func TestRepairStrandedBackgroundSpawns_RepairsWhenRootNotResumable(t *testing.T) {
	root := "chat-1"
	base := newMockRepo()
	base.chats = map[string]*db.Chat{"chat-1": {ID: "chat-1", WorkflowID: &root}}
	base.strandedBackgroundSpawns = []*db.StrandedBackgroundSpawn{
		strandedBackgroundSpawn("tc-1", "chat-1", "chat-1", "child-thread-1", db.Failed()),
	}
	repo := &resumableRootRepo{
		mockRepo: base,
		root:     &db.Workflow{ID: root, ChatID: "chat-1", Status: db.Failed()},
	}
	reconciler := NewReconciler(repo, &mockReconcilerTemporalClient{}, DefaultConfig())

	_, err := reconciler.repairStrandedBackgroundSpawns(context.Background(), &passStats{})
	require.NoError(t, err)
	assert.Len(t, base.enqueuedAgentMessages, 1)
}
